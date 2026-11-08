package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"github.com/tiidiio/223Gaming/223Crash/internal/admin"
	"github.com/tiidiio/223Gaming/223Crash/internal/engine"
	"github.com/tiidiio/223Gaming/223Crash/internal/mongobridge"
	"github.com/tiidiio/223Gaming/223Crash/internal/provider/recovery"
	"github.com/tiidiio/223Gaming/223Crash/internal/wallet"
	"github.com/tiidiio/223Gaming/internal/provider/ledger"
)

type healthResponse struct {
	Status string `json:"status"`
	Mongo  string `json:"mongo"`
}

func main() {
	// Charge .env avant toute lecture des variables obligatoires.
	if err := godotenv.Load(".env"); err != nil {
		log.Printf("⚠️ .env non chargé: %v", err)
	}

	mongoURI := requireEnv("MONGO_URI")
	mongoDB := envOr("MONGO_DB", "223crash")
	jwtSecret := requireEnv("JWT_SECRET")
	wsCheckSecretStrength(jwtSecret)
	httpPort := envOr("HTTP_PORT", "8080")
	wsPort := envOr("WS_PORT", "3000")
	verifyPort := envOr("VERIFY_PORT", "8090")
	walletURL := requireEnv("WALLET_URL")
	walletSecret := requireEnv("WALLET_HMAC_SECRET")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Initialisation MongoDB limitée à 15 secondes.
	initCtx, cancelInit := context.WithTimeout(ctx, 15*time.Second)
	store, err := mongobridge.NewStore(initCtx, mongoURI, mongoDB)
	cancelInit()
	if err != nil {
		log.Fatalf("mongobridge: %v", err)
	}
	defer store.Close(context.Background())

	adminStore, err := admin.NewStore(ctx, store.DB, admin.Config{
		Email:         requireEnv("ADMIN_EMAIL"),
		PasswordHash:  requireEnv("ADMIN_PASSWORD_HASH"),
		SessionSecret: requireEnv("ADMIN_SESSION_SECRET"),
	})
	if err != nil {
		log.Fatalf("admin store: %v", err)
	}

	// MongoDB doit être injecté avant le premier appel à wsSecurity().
	setOperatorDatabase(store.DB)
	wsCfg := wsSecurity()

	if err := refreshAllowedOperators(wsCfg); err != nil {
		log.Printf("sécurité WS: chargement initial impossible, tous les opérateurs refusés: %v", err)
	}

	go runOperatorRefresh(ctx, wsCfg, 5*time.Second)

	publisher := mongobridge.NewPublisher(store)

	// Provider Core : le Ledger réutilise la connexion Mongo déjà ouverte par mongobridge
	// (store.DB) — pas de 3e connexion Mongo dédiée, conformément à internal/provider/ledger.New.
	provLedger, err := ledger.New(ctx, store.DB)
	if err != nil {
		log.Fatalf("provider ledger: %v", err)
	}

	checkWalletURL(walletURL)
	walletClient := wallet.NewClient(walletURL, walletSecret)
	walletQueue, err := wallet.NewQueue(ctx, store.DB, walletClient, provLedger)
	if err != nil {
		log.Fatalf("wallet queue: %v", err)
	}
	go walletQueue.Run(ctx)
	go recovery.New(provLedger, walletClient).Run(ctx)

	engineConfig := engine.DefaultConfig()
	engineConfig.NonceStore = admin.NewMongoNonceStore(store.DB)
	eng, err := engine.NewCrashEngine(engineConfig)
	if err != nil {
		log.Fatalf("engine: %v", err)
	}

	gw := newGateway(eng, jwtSecret, walletClient, walletQueue, provLedger)

	go func() {
		if err := eng.Run(ctx); err != nil && ctx.Err() == nil {
			log.Printf("engine: arrêt inattendu: %v", err)
		}
	}()

	go forwardEvents(ctx, eng, publisher, gw)

	static := http.NewServeMux()
	static.Handle("/api/admin/", admin.NewHandler(adminStore))
	registerProviderConsole(static)
	static.HandleFunc("/", serveStatic)

	static.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		pingCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		mongoStatus := "connected"
		if err := store.Ping(pingCtx); err != nil {
			mongoStatus = "down"
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		if err := json.NewEncoder(w).Encode(healthResponse{
			Status: "ok",
			Mongo:  mongoStatus,
		}); err != nil {
			log.Printf("healthz: erreur JSON: %v", err)
		}
	})

	go serve(ctx, httpPort, static, "static")

	wsMux := http.NewServeMux()
	wsMux.HandleFunc("/", gw.handleUpgrade)
	go serve(ctx, wsPort, wsMux, "ws")

	verifyMux := http.NewServeMux()
	verifyMux.HandleFunc("/verify/ping", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("pong"))
	})
	verifyMux.HandleFunc("/verify/", handleVerify(store))
	go serve(ctx, verifyPort, verifyMux, "verify")

	<-ctx.Done()
	log.Println("arrêt en cours...")
	time.Sleep(500 * time.Millisecond)
}

func requireEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("variable d'environnement requise manquante: %s", key)
	}
	return v
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func serve(ctx context.Context, port string, mux http.Handler, name string) {
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	log.Printf("%s: écoute sur :%s", name, port)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("%s: %v", name, err)
	}
}

var allowedStaticFiles = map[string]string{
	"/":              "index.html",
	"/index.html":    "index.html",
	"/style.css":     "style.css",
	"/ws.js":         "ws.js",
	"/heli-sound.js": "heli-sound.js",
	"/bets.js":       "bets.js",
}

func serveStatic(w http.ResponseWriter, r *http.Request) {
	file, ok := allowedStaticFiles[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, filepath.Clean(file))
}

func forwardEvents(ctx context.Context, eng *engine.CrashEngine, pub *mongobridge.Publisher, gw *gateway) {
	var roundStartedAt time.Time
	tickSem := make(chan struct{}, 1) // 1 publication de tick Mongo max en vol

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-eng.Events():
			if !ok {
				return
			}
			switch ev.Type {
			case engine.EventBettingOpen:
				startsAt := ev.Timestamp
				if err := pub.PublishRoundStart(ctx, ev.RoundID, ev.ServerSeedHash, startsAt); err != nil {
					log.Printf("publish ROUND_START: %v", err)
				}
				gw.broadcast("ROUND_START", map[string]any{
					"roundId":        ev.RoundID,
					"serverSeedHash": ev.ServerSeedHash,
					"startsAt":       startsAt,
				})

			case engine.EventRoundRunning:
				roundStartedAt = ev.Timestamp

			case engine.EventTick:
				elapsedMs := ev.Timestamp.Sub(roundStartedAt).Milliseconds()
				if elapsedMs < 0 {
					elapsedMs = 0
				}
				rid, mult, el := ev.RoundID, ev.Multiplier, elapsedMs
				select {
				case tickSem <- struct{}{}:
					go func() {
						defer func() { <-tickSem }()
						if err := pub.PublishTick(ctx, rid, mult, el); err != nil {
							log.Printf("publish TICK: %v", err)
						}
					}()
				default: // publication précédente encore en cours : tick sauté côté Mongo
				}
				gw.broadcast("TICK", map[string]any{
					"roundId":    ev.RoundID,
					"multiplier": ev.Multiplier,
					"elapsedMs":  elapsedMs,
				})
				gw.checkAutoCashout(ev.Multiplier)

			case engine.EventCrash:
				if err := pub.PublishCrash(ctx, ev.RoundID, ev.CrashPoint, ev.ServerSeed, ev.ClientSeed, ev.Nonce); err != nil {
					log.Printf("publish CRASH: %v", err)
				}
				gw.broadcast("CRASH", map[string]any{
					"roundId":    ev.RoundID,
					"crashPoint": ev.CrashPoint,
				})
				gw.notifyLostBets(ev.LostBets)
			}
		}
	}
}
