package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tiidiio/223Gaming/223Crash/internal/engine"
)

func main() {
	logger := log.New(os.Stdout, "[223CRASH] ", log.LstdFlags|log.Lmicroseconds)

	logger.Println("========================================")
	logger.Println("223CRASH - B2B CRASH GAME ENGINE")
	logger.Println("Moteur Crash B2B Provably Fair")
	logger.Println("Fondé par Tidiane Diallo")
	logger.Println("========================================")

	cfg := engine.DefaultConfig()

	eng, err := engine.NewCrashEngine(cfg)
	if err != nil {
		logger.Fatalf("initialisation du moteur impossible: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- eng.Run(ctx)
	}()

	logger.Println("moteur 223CRASH démarré")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(
		sigCh,
		syscall.SIGINT,
		syscall.SIGTERM,
	)
	defer signal.Stop(sigCh)

	sig := <-sigCh

	logger.Printf("signal reçu: %s", sig)
	logger.Println("arrêt gracieux du moteur...")

	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(
		context.Background(),
		10*time.Second,
	)
	defer shutdownCancel()

	if err := shutdownEngine(shutdownCtx, errCh); err != nil {
		logger.Printf("erreur pendant l'arrêt: %v", err)
		os.Exit(1)
	}

	logger.Println("223CRASH arrêté proprement")
}

func shutdownEngine(ctx context.Context, errCh <-chan error) error {
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
