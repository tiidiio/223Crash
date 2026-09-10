package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"sync"
	"time"

	"github.com/tiidiio/223Gaming/223Crash/internal/game"
)

var (
	ErrEngineNotRunning = errors.New("engine is not running")
	ErrNoActiveRound    = errors.New("no active round")
	ErrRoundNotRunning  = errors.New("round is not running")
	ErrCashoutTooLate   = errors.New("cashout too late")
	ErrInvalidConfig    = errors.New("invalid engine configuration")
)

type Config struct {
	ServerSeed string
	ClientSeed string

	HouseEdge float64

	BettingDuration time.Duration
	TickInterval    time.Duration

	Logger *log.Logger

	EventBuffer     int
	BroadcastBuffer int
}

func DefaultConfig() Config {
	return Config{
		ServerSeed: "CHANGE_ME_SERVER_SEED",
		ClientSeed: "223CRASH",

		HouseEdge: 0.01,

		BettingDuration: 5 * time.Second,
		TickInterval:    100 * time.Millisecond,

		Logger: log.Default(),

		EventBuffer:     128,
		BroadcastBuffer: 256,
	}
}

type EngineStateSnapshot struct {
	RoundID string
	State   game.RoundState

	Multiplier float64

	// CrashPoint est volontairement disponible uniquement
	// dans l'état interne du moteur.
	//
	// NE PAS envoyer cette valeur au client avant le crash.
	CrashPoint float64

	Timestamp time.Time
}

type CashoutResult struct {
	BetID        string
	RoundID      string
	UserID       string
	Panel        game.BetPanel
	Amount       int64
	CashoutMulti float64
	Payout       int64
	State        game.BetState
	CashoutAt    time.Time
}

type CrashEngine struct {
	mu sync.RWMutex

	rm *game.RoundManager
	bm *game.BetManager

	serverSeed string
	clientSeed string
	houseEdge  float64

	bettingDuration time.Duration
	tickInterval    time.Duration

	logger *log.Logger

	eventBuffer     int
	broadcastBuffer int

	currentMultiplier float64
	crashPoint        float64
	nonce             uint64

	broadcast chan EngineStateSnapshot

	running bool
}

func NewCrashEngine(cfg Config) (*CrashEngine, error) {
	if cfg.ServerSeed == "" {
		return nil, ErrInvalidConfig
	}

	if cfg.ClientSeed == "" {
		return nil, ErrInvalidConfig
	}

	if cfg.HouseEdge < 0 || cfg.HouseEdge >= 1 {
		return nil, ErrInvalidConfig
	}

	if cfg.BettingDuration <= 0 {
		return nil, ErrInvalidConfig
	}

	if cfg.TickInterval <= 0 {
		return nil, ErrInvalidConfig
	}

	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}

	if cfg.EventBuffer <= 0 {
		cfg.EventBuffer = 128
	}

	if cfg.BroadcastBuffer <= 0 {
		cfg.BroadcastBuffer = 256
	}

	return &CrashEngine{
		rm: game.NewRoundManager(cfg.EventBuffer),
		bm: game.NewBetManager(),

		serverSeed: cfg.ServerSeed,
		clientSeed: cfg.ClientSeed,
		houseEdge:  cfg.HouseEdge,

		bettingDuration: cfg.BettingDuration,
		tickInterval:    cfg.TickInterval,

		logger: cfg.Logger,

		eventBuffer:     cfg.EventBuffer,
		broadcastBuffer: cfg.BroadcastBuffer,

		broadcast: make(chan EngineStateSnapshot, cfg.BroadcastBuffer),
	}, nil
}

func (e *CrashEngine) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("nil context")
	}

	e.mu.Lock()

	if e.running {
		e.mu.Unlock()
		return errors.New("engine already running")
	}

	e.running = true

	e.mu.Unlock()

	defer func() {
		e.mu.Lock()
		e.running = false
		e.mu.Unlock()
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		default:
		}

		if err := e.runRound(ctx); err != nil {
			if errors.Is(err, context.Canceled) ||
				errors.Is(err, context.DeadlineExceeded) {
				return err
			}

			e.logger.Printf("round error: %v", err)

			select {
			case <-ctx.Done():
				return ctx.Err()

			case <-time.After(250 * time.Millisecond):
			}
		}
	}
}

func (e *CrashEngine) runRound(ctx context.Context) error {
	now := time.Now()

	round, err := e.rm.CreateRound(now)
	if err != nil {
		return err
	}

	if err := e.bm.OpenRound(round.ID); err != nil {
		return err
	}

	if err := e.rm.OpenBetting(now); err != nil {
		return err
	}

	e.setMultiplier(1.00)

	e.publish(EngineStateSnapshot{
		RoundID:    round.ID,
		State:      game.StateBettingOpen,
		Multiplier: 1.00,
		Timestamp:  now,
	})

	if err := waitContext(ctx, e.bettingDuration); err != nil {
		return err
	}

	now = time.Now()

	if err := e.rm.CloseBetting(now); err != nil {
		return err
	}

	if err := e.bm.CloseBetting(); err != nil {
		return err
	}

	crashPoint, err := CrashPoint(
		e.serverSeed,
		e.clientSeed,
		e.currentNonce(),
		e.houseEdge,
	)
	if err != nil {
		return fmt.Errorf("crash point computation failed: %w", err)
	}

	if crashPoint < 1.00 {
		crashPoint = 1.00
	}

	e.setCrashPoint(crashPoint)

	now = time.Now()

	if err := e.rm.Start(now); err != nil {
		return err
	}

	if err := e.bm.StartRound(); err != nil {
		return err
	}

	e.publish(EngineStateSnapshot{
		RoundID:    round.ID,
		State:      game.StateRunning,
		Multiplier: 1.00,
		Timestamp:  now,
	})

	if err := e.runUntilCrash(ctx, round.ID, crashPoint); err != nil {
		return err
	}

	now = time.Now()

	// CRITICAL :
	// le crash et le passage des paris PENDING -> LOST
	// se font dans la même étape logique du moteur.
	//
	// Après cette opération, aucun cashout ne doit être accepté.
	lostBets, err := e.crashRound(now)
	if err != nil {
		return err
	}

	for _, bet := range lostBets {
		e.logger.Printf(
			"bet lost: id=%s user=%s panel=%s amount=%d",
			bet.ID,
			bet.UserID,
			bet.Panel.String(),
			bet.Amount,
		)
	}

	e.publish(EngineStateSnapshot{
		RoundID:    round.ID,
		State:      game.StateCrashed,
		Multiplier: crashPoint,
		CrashPoint: crashPoint,
		Timestamp:  now,
	})

	if err := e.rm.Settle(time.Now()); err != nil {
		return err
	}

	if err := e.bm.ResetRound(); err != nil {
		return err
	}

	e.publish(EngineStateSnapshot{
		RoundID:    round.ID,
		State:      game.StateSettled,
		Multiplier: crashPoint,
		CrashPoint: crashPoint,
		Timestamp:  time.Now(),
	})

	e.nextNonce()

	e.setCrashPoint(0)
	e.setMultiplier(1.00)

	return nil
}

func (e *CrashEngine) runUntilCrash(
	ctx context.Context,
	roundID string,
	crashPoint float64,
) error {

	ticker := time.NewTicker(e.tickInterval)
	defer ticker.Stop()

	startedAt := time.Now()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case now := <-ticker.C:
			elapsed := now.Sub(startedAt).Seconds()

			multiplier := calculateMultiplier(elapsed)

			if multiplier >= crashPoint {
				e.setMultiplier(crashPoint)
				return nil
			}

			e.setMultiplier(multiplier)

			e.publish(EngineStateSnapshot{
				RoundID:    roundID,
				State:      game.StateRunning,
				Multiplier: multiplier,
				Timestamp:  now,
			})
		}
	}
}

// Cashout est l'API interne appelée par le Gateway plus tard.
//
// IMPORTANT :
// le Gateway ne fournit PAS le multiplicateur.
//
// Il fournit seulement le betID.
//
// Le moteur récupère lui-même son multiplicateur autoritaire,
// puis le transmet au BetManager.
func (e *CrashEngine) Cashout(
	betID string,
) (CashoutResult, error) {

	if betID == "" {
		return CashoutResult{}, game.ErrBetNotFound
	}

	// On prend le verrou du moteur avant de lire le multiplicateur.
	//
	// Cela sérialise le cashout avec la transition de crash.
	e.mu.Lock()
	defer e.mu.Unlock()

	if !e.running {
		return CashoutResult{}, ErrEngineNotRunning
	}

	round, err := e.rm.CurrentRound()
	if err != nil {
		return CashoutResult{}, err
	}

	if round.State != game.StateRunning {
		if round.State == game.StateCrashed {
			return CashoutResult{}, ErrCashoutTooLate
		}

		return CashoutResult{}, ErrRoundNotRunning
	}

	// SOURCE UNIQUE DU MULTIPLICATEUR :
	// l'état interne du moteur.
	//
	// Aucune donnée client n'entre ici.
	authoritativeMultiplier := e.currentMultiplier

	now := time.Now()

	bet, err := e.bm.Cashout(
		betID,
		authoritativeMultiplier,
		now,
	)
	if err != nil {
		return CashoutResult{}, err
	}

	return CashoutResult{
		BetID:        bet.ID,
		RoundID:      bet.RoundID,
		UserID:       bet.UserID,
		Panel:        bet.Panel,
		Amount:       bet.Amount,
		CashoutMulti: bet.CashoutMulti,
		Payout:       bet.Payout,
		State:        bet.State,
		CashoutAt:    bet.CashoutAt,
	}, nil
}

func (e *CrashEngine) PlaceBet(
	userID string,
	panel game.BetPanel,
	amount int64,
	cashoutTarget float64,
) (game.Bet, error) {

	e.mu.RLock()
	defer e.mu.RUnlock()

	round, err := e.rm.CurrentRound()
	if err != nil {
		return game.Bet{}, err
	}

	if round.State != game.StateBettingOpen {
		return game.Bet{}, game.ErrBettingClosed
	}

	bet, err := e.bm.PlaceBet(
		userID,
		panel,
		amount,
		cashoutTarget,
	)
	if err != nil {
		return game.Bet{}, err
	}

	return *bet, nil
}

func (e *CrashEngine) CurrentState() (EngineStateSnapshot, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	round, err := e.rm.CurrentRound()
	if err != nil {
		return EngineStateSnapshot{}, err
	}

	multiplier := e.currentMultiplier

	// Le crash point est masqué pendant RUNNING.
	//
	// Il ne doit jamais être transmis au client avant le crash.
	crashPoint := float64(0)

	if round.State == game.StateCrashed ||
		round.State == game.StateSettled {
		crashPoint = e.crashPoint
	}

	return EngineStateSnapshot{
		RoundID:    round.ID,
		State:      round.State,
		Multiplier: multiplier,
		CrashPoint: crashPoint,
		Timestamp:  time.Now(),
	}, nil
}

func (e *CrashEngine) Broadcast() <-chan EngineStateSnapshot {
	return e.broadcast
}

func (e *CrashEngine) RoundManager() *game.RoundManager {
	return e.rm
}

func (e *CrashEngine) BetManager() *game.BetManager {
	return e.bm
}

func (e *CrashEngine) currentNonce() uint64 {
	e.mu.RLock()
	defer e.mu.RUnlock()

	return e.nonce
}

func (e *CrashEngine) nextNonce() {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.nonce++
}

func (e *CrashEngine) setMultiplier(multiplier float64) {
	e.mu.Lock()
	e.currentMultiplier = multiplier
	e.mu.Unlock()
}

func (e *CrashEngine) setCrashPoint(crashPoint float64) {
	e.mu.Lock()
	e.crashPoint = crashPoint
	e.mu.Unlock()
}

func (e *CrashEngine) getMultiplier() float64 {
	e.mu.RLock()
	defer e.mu.RUnlock()

	return e.currentMultiplier
}

func (e *CrashEngine) crashRound(now time.Time) ([]game.Bet, error) {
	// Le verrou du moteur est pris par Cashout().
	//
	// runRound n'a pas besoin de fournir de multiplicateur.
	//
	// On marque d'abord le round comme CRASHED.
	if err := e.rm.Crash(now); err != nil {
		return nil, err
	}

	// Puis le BetManager ferme la possibilité de cashout
	// et transforme tous les PENDING en LOST.
	return e.bm.Crash(now)
}

func (e *CrashEngine) publish(snapshot EngineStateSnapshot) {
	select {
	case e.broadcast <- snapshot:
	default:
		// Le moteur temps réel ne doit jamais être bloqué
		// par un consommateur lent.
		//
		// Une couche de métriques devra être ajoutée
		// pour compter ces pertes d'événements.
	}
}

func calculateMultiplier(elapsedSeconds float64) float64 {
	if elapsedSeconds < 0 {
		elapsedSeconds = 0
	}

	multiplier := math.Exp(0.10 * elapsedSeconds)

	multiplier = math.Floor(multiplier*100) / 100

	if multiplier < 1.00 {
		return 1.00
	}

	if multiplier > 50000.00 {
		return 50000.00
	}

	return multiplier
}

func waitContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()

	case <-timer.C:
		return nil
	}
}
