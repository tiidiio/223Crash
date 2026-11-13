package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"github.com/tiidiio/223Crash/internal/admin"
	"github.com/tiidiio/223Crash/internal/mongobridge"
	"github.com/tiidiio/223Crash/internal/product"
	"github.com/tiidiio/223Crash/provider/ledger"
	"github.com/tiidiio/223Crash/provider/recovery"
	"github.com/tiidiio/223Crash/provider/wallet"
)

type healthResponse struct {
	Status string `json:"status"`
	Mongo  string `json:"mongo"`
}

func requireEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		log.Fatalf("env %s manquant", k)
	}
	return v
}
func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
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

	setOperatorDatabase(store.DB)
	wsCfg := wsSecurity()
	if err := refreshAllowedOperators(wsCfg); err != nil {
		log.Printf("sécurité WS: chargement initial impossible: %v", err)
	}
	go runOperatorRefresh(ctx, wsCfg, 5*time.Second)

	publisher := mongobridge.NewPublisher(store)
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

	gameEngine, gameEngineInner := product.NewGameEngineWithInner(store.DB)
	betStore := NewMongoBetStore(store.DB)
	gw := newGateway(gameEngine, jwtSecret, walletClient, walletQueue, provLedger, betStore)

	go func() {
		if err := gameEngine.Run(ctx); err != nil && ctx.Err() == nil {
			log.Printf("engine: arrêt inattendu: %v", err)
		}
	}()

	go forwardEvents(ctx, gameEngine, publisher, gw)

	static := http.NewServeMux()
	static.Handle("/api/admin/", admin.NewHandler(adminStore))
	static.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(healthResponse{Status: "ok", Mongo: "connected"})
	})
	static.Handle("/verify/", handleVerify(store))
		static.HandleFunc("/api/internal/crashruntime/live", handleRuntimeLive(gameEngineInner, gw))

	httpSrv := &http.Server{Addr: ":" + httpPort, Handler: static}
	verifySrv := &http.Server{Addr: ":" + verifyPort, Handler: http.HandlerFunc(handleVerify(store).ServeHTTP)}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		log.Printf("HTTP admin :%s", httpPort)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		log.Printf("WS gateway :%s", wsPort)
		if err := http.ListenAndServe(":"+wsPort, http.HandlerFunc(gw.handleUpgrade)); err != nil && err != http.ErrServerClosed {
			log.Fatalf("ws: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("arrêt...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	httpSrv.Shutdown(shutdownCtx)
	verifySrv.Shutdown(shutdownCtx)
	gw.Shutdown()
	wg.Wait()
}
