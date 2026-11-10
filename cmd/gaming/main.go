package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/tiidiio/223Gaming/internal/adminstore"
	"github.com/tiidiio/223Gaming/internal/gamecatalog"
	"github.com/tiidiio/223Gaming/internal/httpapi"
	"github.com/tiidiio/223Gaming/internal/runtimeprobe"
	"github.com/tiidiio/223Gaming/internal/runtimeregistry"
	"github.com/tiidiio/223Gaming/provider/ledger"
)

func main() {
	if err := run(); err != nil {
		log.Printf("223GAMING Provider stopped: %v", err)
		os.Exit(1)
	}
}

// buildRuntimeRegistry crée une sonde par produit du catalogue. L'URL du runtime vient de
// GAME_RUNTIME_URL_<GAMEID> ; absente, le produit est UNKNOWN (jamais AVAILABLE).
// GAME_MAINTENANCE="id1,id2" déclare une maintenance explicite (environnement seulement).
func buildRuntimeRegistry() *runtimeregistry.Registry {
	games := gamecatalog.List()
	checkers := make([]runtimeregistry.Checker, 0, len(games))
	for _, g := range games {
		checkers = append(checkers, runtimeprobe.New(g.ID, os.Getenv(runtimeprobe.EnvName(g.ID))))
	}

	maintenance := make(map[string]bool)
	for _, id := range strings.Split(os.Getenv("GAME_MAINTENANCE"), ",") {
		if id = strings.TrimSpace(id); id != "" {
			maintenance[id] = true
		}
	}

	return runtimeregistry.New(checkers, runtimeregistry.Options{Maintenance: maintenance})
}

func run() error {
	const configPath = ".env.provider"

	if err := adminstore.LoadEnvFile(configPath); err != nil {
		return errors.New("cannot load provider configuration")
	}

	mongoURI := strings.TrimSpace(os.Getenv("MONGO_URI"))
	if mongoURI == "" {
		return errors.New("MONGO_URI is required")
	}

	dbName := strings.TrimSpace(os.Getenv("MONGO_DB"))
	if dbName == "" {
		dbName = "223gaming_provider"
	}

	port := strings.TrimSpace(os.Getenv("GAMING_HTTP_PORT"))
	if port == "" {
		port = "8081"
	}

	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return errors.New("GAMING_HTTP_PORT must be between 1 and 65535")
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		45*time.Second,
	)
	defer cancel()

	// Connexion n°1 : comptes administrateurs, sessions, audit, opérateurs.
	store, err := adminstore.ConnectWithRetry(ctx, mongoURI, dbName, 3)
	if err != nil {
		return errors.New("provider database connection failed")
	}
	defer store.Close(context.Background())

	log.Printf("Connected to provider database: %s", dbName)

	deps := httpapi.ConsoleDeps{}

	// Connexion n°2 : Ledger en LECTURE SEULE. Le moteur écrit provider_ledger_entries
	// dans sa propre base (LEDGER_MONGO_DB, ex. 223crash). L'auth ne s'y mélange jamais.
	ledgerDB := strings.TrimSpace(os.Getenv("LEDGER_MONGO_DB"))
	if ledgerDB == "" {
		log.Printf("Provider Ledger reader disabled: LEDGER_MONGO_DB not set")
		deps.LedgerUnavailableReason = "ledger_database_not_configured"
	} else {
		ledgerURI := strings.TrimSpace(os.Getenv("LEDGER_MONGO_URI"))
		if ledgerURI == "" {
			ledgerURI = mongoURI
		}

		ledgerClient, connErr := mongo.Connect(
			options.Client().
				ApplyURI(ledgerURI).
				SetServerSelectionTimeout(10 * time.Second),
		)
		if connErr != nil {
			log.Printf("Provider Ledger connection failed")
			deps.LedgerUnavailableReason = "ledger_connection_failed"
		} else {
			defer ledgerClient.Disconnect(context.Background())

			reader, readerErr := ledger.NewReader(ledgerClient.Database(ledgerDB))
			if readerErr != nil {
				log.Printf("Provider Ledger reader unavailable: %v", readerErr)
				deps.LedgerUnavailableReason = "reader_initialization_failed"
			} else {
				deps.Ledger = reader
				log.Printf("Provider Ledger reader connected to database: %s", ledgerDB)
			}
		}
	}

	// Registre de runtimes : état mesuré par sonde HTTP, indépendant du catalogue.
	deps.Runtimes = buildRuntimeRegistry()
	log.Printf("Runtime registry ready")

	mux, err := httpapi.NewMuxWithDeps(store, deps)
	if err != nil {
		return errors.New("provider HTTP router initialization failed")
	}

	server := &http.Server{
		Addr:              net.JoinHostPort("", port),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serverErrors := make(chan error, 1)

	go func() {
		log.Printf("223GAMING Provider listening on port %s", port)

		err := server.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(quit)

	select {
	case <-quit:
		log.Println("Provider shutdown requested")

	case err := <-serverErrors:
		_ = server.Close()
		return errors.New("provider HTTP server failed: " + err.Error())
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(
		context.Background(),
		10*time.Second,
	)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
		return errors.New("provider HTTP shutdown failed")
	}

	log.Println("223GAMING Provider stopped successfully")

	return nil
}
