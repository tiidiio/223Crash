package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/tiidiio/223Gaming/223Crash/internal/engine"
	"github.com/tiidiio/223Gaming/223Crash/internal/game"
	"github.com/tiidiio/223Gaming/223Crash/internal/mongobridge"
)

func main() {
	logger := log.New(os.Stdout, "[223CRASH] ", log.LstdFlags|log.Lmicroseconds)
	logger.Println("========================================")
	logger.Println("223CRASH - B2B CRASH GAME ENGINE")
	logger.Println("Moteur Crash B2B Provably Fair")
	logger.Println("Fondé par Tidiane Diallo")
	logger.Println("========================================")

	mongoURI := os.Getenv("MONGODB_URI")
	if mongoURI == "" {
		logger.Fatal("MONGODB_URI n'est pas défini")
	}
	dbName := os.Getenv("MONGODB_DB")
	if dbName == "" {
		dbName = "223crash"
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bootCtx, bootCancel := context.WithTimeout(ctx, 20*time.Second)
	store, err := mongobridge.NewStore(bootCtx, mongoURI, dbName)
	bootCancel()
	if err != nil {
		logger.Fatalf("connexion mongobridge impossible: %v", err)
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		if err := store.Close(closeCtx); err != nil {
			logger.Printf("erreur fermeture store mongo: %v", err)
		}
	}()

	publisher := mongobridge.NewPublisher(store)

	cfg := engine.DefaultConfig()
	eng, err := engine.NewCrashEngine(cfg)
	if err != nil {
		logger.Fatalf("initialisation du moteur impossible: %v", err)
	}

	rt := newRuntimeState()

	// Consomme les Event du moteur et les relaie vers Mongo AVANT de démarrer Run,
	// pour ne jamais rater le premier ROUND_CREATED.
	eventsDone := make(chan struct{})
	go func() {
		defer close(eventsDone)
		relayEvents(ctx, logger, eng, publisher, rt, cfg.BettingDuration)
	}()

	handlers := mongobridge.CommandHandlers{
		OnJoin: func(ctx context.Context, cmd mongobridge.Command) {
			handleJoin(ctx, logger, eng, publisher, rt, cmd)
		},
		OnPlaceBet: func(ctx context.Context, cmd mongobridge.Command) {
			handlePlaceBet(ctx, logger, eng, publisher, cmd)
		},
		OnCashout: func(ctx context.Context, cmd mongobridge.Command) {
			handleCashout(ctx, logger, eng, publisher, cmd)
		},
	}
	subscriber := mongobridge.NewSubscriber(store, handlers)

	subDone := make(chan error, 1)
	go func() {
		subDone <- subscriber.Watch(ctx)
	}()

	errCh := make(chan error, 1)
	go func() {
		errCh <- eng.Run(ctx)
	}()

	logger.Println("moteur 223CRASH démarré")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	sig := <-sigCh
	logger.Printf("signal reçu: %s", sig)
	logger.Println("arrêt gracieux du moteur...")

	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := shutdownEngine(shutdownCtx, errCh); err != nil {
		logger.Printf("erreur pendant l'arrêt du moteur: %v", err)
		os.Exit(1)
	}

	<-eventsDone
	<-subDone

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

// runtimeState conserve, hors du moteur, l'instant de démarrage du round RUNNING
// courant — nécessaire pour calculer elapsedMs dans PublishTick, que l'Event du
// moteur n'expose pas directement.
type runtimeState struct {
	mu             sync.RWMutex
	roundStartedAt time.Time
	roundID        string
}

func newRuntimeState() *runtimeState {
	return &runtimeState{}
}

func (r *runtimeState) setRoundStart(roundID string, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.roundID = roundID
	r.roundStartedAt = at
}

func (r *runtimeState) elapsedMs(roundID string, at time.Time) int64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.roundID != roundID || r.roundStartedAt.IsZero() {
		return 0
	}
	ms := at.Sub(r.roundStartedAt).Milliseconds()
	if ms < 0 {
		return 0
	}
	return ms
}

// relayEvents consomme eng.Events() et publie vers Mongo. Ne bloque jamais le moteur :
// le channel Events() est déjà non-bloquant côté émission (voir CrashEngine.emit).
func relayEvents(
	ctx context.Context,
	logger *log.Logger,
	eng *engine.CrashEngine,
	publisher *mongobridge.Publisher,
	rt *runtimeState,
	bettingDuration time.Duration,
) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-eng.Events():
			if !ok {
				return
			}

			pubCtx, pubCancel := context.WithTimeout(ctx, 5*time.Second)

			switch ev.Type {
			case engine.EventBettingOpen:
				// startsAt = fin de la fenêtre de mise = début de la phase RUNNING.
				startsAt := ev.Timestamp.Add(bettingDuration)
				if err := publisher.PublishRoundStart(pubCtx, ev.RoundID, ev.ServerSeedHash, startsAt); err != nil {
					logger.Printf("publish ROUND_START échoué (round=%s): %v", ev.RoundID, err)
				}

			case engine.EventRoundRunning:
				rt.setRoundStart(ev.RoundID, ev.Timestamp)

			case engine.EventTick:
				elapsed := rt.elapsedMs(ev.RoundID, ev.Timestamp)
				if err := publisher.PublishTick(pubCtx, ev.RoundID, ev.Multiplier, elapsed); err != nil {
					logger.Printf("publish TICK échoué (round=%s): %v", ev.RoundID, err)
				}

			case engine.EventCrash:
				if err := publisher.PublishCrash(pubCtx, ev.RoundID, ev.CrashPoint, ev.ServerSeed, ev.ClientSeed, ev.Nonce); err != nil {
					logger.Printf("publish CRASH échoué (round=%s): %v", ev.RoundID, err)
				}
				for _, bet := range ev.LostBets {
					if err := publisher.PublishPlayerEvent(pubCtx, bet.UserID, "BET_LOST", bson.M{
						"roundId": ev.RoundID,
						"betId":   bet.ID,
						"panel":   bet.Panel.String(),
						"amount":  bet.Amount,
					}); err != nil {
						logger.Printf("publish BET_LOST échoué (bet=%s): %v", bet.ID, err)
					}
				}
			}

			pubCancel()
		}
	}
}

func handleJoin(
	ctx context.Context,
	logger *log.Logger,
	eng *engine.CrashEngine,
	publisher *mongobridge.Publisher,
	rt *runtimeState,
	cmd mongobridge.Command,
) {
	round, err := eng.CurrentRound()
	if err != nil {
		logger.Printf("JOIN: CurrentRound échoué (player=%s): %v", cmd.PlayerID, err)
		return
	}

	hash, clientSeed, nonce := eng.CurrentFairness()
	multiplier := eng.CurrentMultiplier()
	activeBets := eng.UserRoundBets(cmd.PlayerID)

	betsData := make([]bson.M, 0, len(activeBets))
	for _, b := range activeBets {
		betsData = append(betsData, bson.M{
			"betId":  b.ID,
			"panel":  b.Panel.String(),
			"amount": b.Amount,
			"state":  b.State,
		})
	}

	if err := publisher.PublishPlayerEvent(ctx, cmd.PlayerID, "STATE_SYNC", bson.M{
		"roundId":        round.ID,
		"state":          round.State,
		"multiplier":     multiplier,
		"serverSeedHash": hash,
		"clientSeed":     clientSeed,
		"nonce":          nonce,
		"bets":           betsData,
	}); err != nil {
		logger.Printf("publish STATE_SYNC échoué (player=%s): %v", cmd.PlayerID, err)
	}
}

func handlePlaceBet(
	ctx context.Context,
	logger *log.Logger,
	eng *engine.CrashEngine,
	publisher *mongobridge.Publisher,
	cmd mongobridge.Command,
) {
	if cmd.Slot < 1 || cmd.Slot > 4 {
		publishRejection(ctx, logger, publisher, cmd.PlayerID, "panel invalide")
		return
	}

	// ASSUMPTION à confirmer : cmd.Amount est déjà dans l'unité minimale de la
	// devise (ex: centimes), transmis en float64 côté gateway. Arrondi défensif.
	amount := int64(math.Round(cmd.Amount))

	cashoutTarget := 0.0
	if cmd.AutoCashout != nil {
		cashoutTarget = *cmd.AutoCashout
	}

	bet, err := eng.PlaceBet(cmd.PlayerID, game.BetPanel(cmd.Slot), amount, cashoutTarget)
	if err != nil {
		publishRejection(ctx, logger, publisher, cmd.PlayerID, err.Error())
		return
	}

	if err := publisher.PublishPlayerEvent(ctx, cmd.PlayerID, "BET_ACCEPTED", bson.M{
		"betId":   bet.ID,
		"roundId": bet.RoundID,
		"panel":   bet.Panel.String(),
		"amount":  bet.Amount,
	}); err != nil {
		logger.Printf("publish BET_ACCEPTED échoué (player=%s): %v", cmd.PlayerID, err)
	}
}

func handleCashout(
	ctx context.Context,
	logger *log.Logger,
	eng *engine.CrashEngine,
	publisher *mongobridge.Publisher,
	cmd mongobridge.Command,
) {
	betID, err := findActiveBetID(eng, cmd.PlayerID, cmd.Slot)
	if err != nil {
		publishRejection(ctx, logger, publisher, cmd.PlayerID, err.Error())
		return
	}

	bet, err := eng.Cashout(betID)
	if err != nil {
		publishRejection(ctx, logger, publisher, cmd.PlayerID, err.Error())
		return
	}

	if err := publisher.PublishPlayerEvent(ctx, cmd.PlayerID, "CASHOUT_CONFIRMED", bson.M{
		"betId":        bet.ID,
		"roundId":      bet.RoundID,
		"panel":        bet.Panel.String(),
		"cashoutMulti": bet.CashoutMulti,
		"payout":       bet.Payout,
	}); err != nil {
		logger.Printf("publish CASHOUT_CONFIRMED échoué (player=%s): %v", cmd.PlayerID, err)
	}
}

// findActiveBetID retrouve l'ID de mise actif d'un joueur sur un panel donné.
// Nécessaire car mongobridge.Command ne transporte pas de betID pour CASHOUT
// (seulement playerId + slot) — à ajuster si le format de commande gateway évolue.
func findActiveBetID(eng *engine.CrashEngine, playerID string, slot int) (string, error) {
	if slot < 1 || slot > 4 {
		return "", fmt.Errorf("panel invalide: %d", slot)
	}
	panel := game.BetPanel(slot)

	for _, b := range eng.UserRoundBets(playerID) {
		if b.Panel == panel {
			return b.ID, nil
		}
	}
	return "", fmt.Errorf("aucune mise active pour player=%s panel=%d", playerID, slot)
}

func publishRejection(
	ctx context.Context,
	logger *log.Logger,
	publisher *mongobridge.Publisher,
	playerID, reason string,
) {
	if err := publisher.PublishPlayerEvent(ctx, playerID, "COMMAND_REJECTED", bson.M{
		"reason": reason,
	}); err != nil {
		logger.Printf("publish COMMAND_REJECTED échoué (player=%s): %v", playerID, err)
	}
}
