package engine

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/tiidiio/223Gaming/223Crash/internal/game"
)

var (
	ErrEngineAlreadyRunning = errors.New("engine: already running")
	ErrNoActiveCycle        = errors.New("engine: no active round cycle")
)

// NonceStore fournit des nonces strictement croissants pour la génération provably fair.
// L'implémentation par défaut (inMemoryNonceStore) redémarre à 0 à chaque process — pour un
// historique auditable persistant entre restarts, injecter une implémentation adossée à Mongo
// via Config.NonceStore (ex: $inc atomique sur un document compteur).
type NonceStore interface {
	NextNonce(ctx context.Context) (int64, error)
}

type inMemoryNonceStore struct {
	mu      sync.Mutex
	counter int64
}

func (s *inMemoryNonceStore) NextNonce(ctx context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counter++
	return s.counter, nil
}

// Config regroupe les paramètres de timing et de courbe du moteur.
type Config struct {
	BettingDuration   time.Duration
	CrashedDuration   time.Duration
	TickInterval      time.Duration
	GrowthFactor      float64 // multiplier(t) = e^(GrowthFactor * elapsedMs)
	EventBuffer       int
	InitialClientSeed string     // seed genesis si aucun round précédent n'a encore révélé de serverSeed
	NonceStore        NonceStore // optionnel — défaut : compteur in-memory (voir NonceStore)
}

// DefaultConfig retourne la configuration standard 223CRASH.
func DefaultConfig() Config {
	return Config{
		BettingDuration:   5 * time.Second,
		CrashedDuration:   3 * time.Second,
		TickInterval:      100 * time.Millisecond,
		GrowthFactor:      0.00006,
		EventBuffer:       256,
		InitialClientSeed: "",
	}
}

// EventType identifie la nature d'un Event émis par le moteur.
type EventType string

const (
	EventRoundCreated  EventType = "ROUND_CREATED" // serverSeedHash publié (commit), serverSeed secret
	EventBettingOpen   EventType = "BETTING_OPEN"
	EventBettingClosed EventType = "BETTING_CLOSED"
	EventRoundRunning  EventType = "ROUND_RUNNING"
	EventTick          EventType = "TICK"
	EventCrash         EventType = "CRASH" // serverSeed révélé, crashPoint confirmé, mises perdantes réglées
	EventRoundSettled  EventType = "ROUND_SETTLED"
)

// Event est diffusé par le moteur vers ses consommateurs (WS gateway).
type Event struct {
	Type           EventType
	RoundID        string
	State          game.RoundState
	Multiplier     float64
	ServerSeedHash string
	ServerSeed     string // vide sauf sur EventCrash (révélation)
	ClientSeed     string
	Nonce          int64
	CrashPoint     float64
	LostBets       []game.Bet // peuplé uniquement sur EventCrash
	Timestamp      time.Time
}

// CrashEngine orchestre le cycle de vie complet d'un round : génération provably fair,
// transitions FSM (via game.RoundManager), gestion des mises (via game.BetManager),
// et diffusion de la courbe du multiplicateur en temps réel.
type CrashEngine struct {
	cfg Config

	rounds     *game.RoundManager
	bets       *game.BetManager
	nonceStore NonceStore

	mu             sync.RWMutex
	running        bool
	lastServerSeed string // dernier serverSeed révélé, pour dériver le clientSeed du round suivant

	// État du round courant, protégé par mu.
	roundStartedAt time.Time
	serverSeed     string
	serverSeedHash string
	clientSeed     string
	nonce          int64
	crashPoint     float64
	multiplier     float64

	events chan Event
}

// NewCrashEngine construit un CrashEngine prêt à être lancé via Run.
func NewCrashEngine(cfg Config) (*CrashEngine, error) {
	if cfg.BettingDuration <= 0 {
		return nil, fmt.Errorf("engine: BettingDuration invalide")
	}
	if cfg.CrashedDuration <= 0 {
		return nil, fmt.Errorf("engine: CrashedDuration invalide")
	}
	if cfg.TickInterval <= 0 {
		return nil, fmt.Errorf("engine: TickInterval invalide")
	}
	if cfg.GrowthFactor <= 0 {
		return nil, fmt.Errorf("engine: GrowthFactor invalide")
	}
	if cfg.EventBuffer < 1 {
		cfg.EventBuffer = 256
	}

	nonceStore := cfg.NonceStore
	if nonceStore == nil {
		nonceStore = &inMemoryNonceStore{}
	}

	return &CrashEngine{
		cfg:        cfg,
		rounds:     game.NewRoundManager(cfg.EventBuffer),
		bets:       game.NewBetManager(),
		nonceStore: nonceStore,
		events:     make(chan Event, cfg.EventBuffer),
	}, nil
}

// Run exécute la boucle infinie de cycles de round jusqu'à annulation du contexte.
// Un seul appel concurrent à Run est autorisé.
func (e *CrashEngine) Run(ctx context.Context) error {
	e.mu.Lock()
	if e.running {
		e.mu.Unlock()
		return ErrEngineAlreadyRunning
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

		if err := e.runRoundCycle(ctx); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			return fmt.Errorf("engine: cycle de round échoué: %w", err)
		}
	}
}

// runRoundCycle exécute un cycle complet : Created -> BettingOpen -> BettingClosed -> Running -> Crashed -> Settled.
func (e *CrashEngine) runRoundCycle(ctx context.Context) error {
	now := time.Now()

	round, err := e.rounds.CreateRound(now)
	if err != nil {
		return fmt.Errorf("création du round: %w", err)
	}

	if err := e.prepareFairness(ctx, round.ID); err != nil {
		return fmt.Errorf("préparation provably fair: %w", err)
	}

	if err := e.bets.OpenRound(round.ID); err != nil {
		return fmt.Errorf("ouverture des mises: %w", err)
	}

	e.emitRoundCreated(round.ID, now)

	// --- Phase BETTING_OPEN ---
	if err := e.rounds.OpenBetting(now); err != nil {
		return fmt.Errorf("transition betting_open: %w", err)
	}
	e.emit(EventBettingOpen, round.ID, game.StateBettingOpen, now)

	if err := e.sleep(ctx, e.cfg.BettingDuration); err != nil {
		return err
	}

	// --- Phase BETTING_CLOSED ---
	closeNow := time.Now()
	if err := e.bets.CloseBetting(); err != nil {
		return fmt.Errorf("fermeture des mises: %w", err)
	}
	if err := e.rounds.CloseBetting(closeNow); err != nil {
		return fmt.Errorf("transition betting_closed: %w", err)
	}
	e.emit(EventBettingClosed, round.ID, game.StateBettingClosed, closeNow)

	// --- Phase RUNNING ---
	startNow := time.Now()
	if err := e.bets.StartRound(); err != nil {
		return fmt.Errorf("démarrage des mises: %w", err)
	}
	if err := e.rounds.Start(startNow); err != nil {
		return fmt.Errorf("transition running: %w", err)
	}

	e.mu.Lock()
	e.roundStartedAt = startNow
	e.multiplier = 1.00
	crashPoint := e.crashPoint
	e.mu.Unlock()

	e.emit(EventRoundRunning, round.ID, game.StateRunning, startNow)

	if err := e.runTickLoop(ctx, round.ID, startNow, crashPoint); err != nil {
		return err
	}

	// --- Phase CRASHED ---
	crashNow := time.Now()
	lostBets, err := e.bets.Crash(crashNow)
	if err != nil {
		return fmt.Errorf("règlement des mises perdantes: %w", err)
	}
	if err := e.rounds.Crash(crashNow); err != nil {
		return fmt.Errorf("transition crashed: %w", err)
	}

	e.mu.Lock()
	e.multiplier = crashPoint
	revealedSeed := e.serverSeed
	e.lastServerSeed = revealedSeed
	e.mu.Unlock()

	e.events <- Event{
		Type:           EventCrash,
		RoundID:        round.ID,
		State:          game.StateCrashed,
		Multiplier:     crashPoint,
		ServerSeedHash: e.currentServerSeedHash(),
		ServerSeed:     revealedSeed,
		ClientSeed:     e.currentClientSeed(),
		Nonce:          e.currentNonce(),
		CrashPoint:     crashPoint,
		LostBets:       lostBets,
		Timestamp:      crashNow,
	}

	// --- Phase SETTLED ---
	settleNow := time.Now()
	if err := e.rounds.Settle(settleNow); err != nil {
		return fmt.Errorf("transition settled: %w", err)
	}
	e.emit(EventRoundSettled, round.ID, game.StateSettled, settleNow)

	if err := e.sleep(ctx, e.cfg.CrashedDuration); err != nil {
		return err
	}

	if err := e.bets.ResetRound(); err != nil {
		return fmt.Errorf("reset du bet manager: %w", err)
	}

	return nil
}

// runTickLoop diffuse le multiplicateur courant jusqu'à atteindre le crashPoint.
func (e *CrashEngine) runTickLoop(ctx context.Context, roundID string, startedAt time.Time, crashPoint float64) error {
	ticker := time.NewTicker(e.cfg.TickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case tickTime := <-ticker.C:
			elapsedMs := tickTime.Sub(startedAt).Milliseconds()
			if elapsedMs < 0 {
				elapsedMs = 0
			}

			raw := multiplierAtElapsed(elapsedMs, e.cfg.GrowthFactor)
			if raw >= crashPoint {
				return nil
			}

			e.mu.Lock()
			e.multiplier = raw
			e.mu.Unlock()

			e.emit(EventTick, roundID, game.StateRunning, tickTime)
		}
	}
}

// sleep attend la durée donnée en respectant l'annulation du contexte.
func (e *CrashEngine) sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// prepareFairness génère les données provably fair du round et les stocke sous verrou.
// Le nonce provient de e.nonceStore (persistable inter-process, voir NonceStore).
func (e *CrashEngine) prepareFairness(ctx context.Context, roundID string) error {
	nonce, err := e.nonceStore.NextNonce(ctx)
	if err != nil {
		return fmt.Errorf("obtention du nonce: %w", err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	var clientSeed string
	if e.lastServerSeed != "" {
		clientSeed = NextClientSeed(e.lastServerSeed)
	} else if e.cfg.InitialClientSeed != "" {
		clientSeed = e.cfg.InitialClientSeed
	} else {
		genesis, err := GenerateServerSeed()
		if err != nil {
			return fmt.Errorf("génération du client seed genesis: %w", err)
		}
		clientSeed = genesis
	}

	serverSeed, err := GenerateServerSeed()
	if err != nil {
		return fmt.Errorf("génération du server seed: %w", err)
	}

	crashPoint, err := CrashPoint(serverSeed, clientSeed, nonce)
	if err != nil {
		return fmt.Errorf("calcul du crash point: %w", err)
	}

	e.serverSeed = serverSeed
	e.serverSeedHash = HashServerSeed(serverSeed)
	e.clientSeed = clientSeed
	e.nonce = nonce
	e.crashPoint = crashPoint
	e.multiplier = 1.00

	return nil
}

func (e *CrashEngine) emitRoundCreated(roundID string, now time.Time) {
	e.mu.RLock()
	hash := e.serverSeedHash
	clientSeed := e.clientSeed
	nonce := e.nonce
	e.mu.RUnlock()

	e.events <- Event{
		Type:           EventRoundCreated,
		RoundID:        roundID,
		State:          game.StateCreated,
		Multiplier:     1.00,
		ServerSeedHash: hash,
		ClientSeed:     clientSeed,
		Nonce:          nonce,
		Timestamp:      now,
	}
}

func (e *CrashEngine) emit(t EventType, roundID string, state game.RoundState, now time.Time) {
	e.mu.RLock()
	mult := e.multiplier
	hash := e.serverSeedHash
	clientSeed := e.clientSeed
	nonce := e.nonce
	crashPoint := e.crashPoint
	e.mu.RUnlock()

	event := Event{
		Type:           t,
		RoundID:        roundID,
		State:          state,
		Multiplier:     mult,
		ServerSeedHash: hash,
		ClientSeed:     clientSeed,
		Nonce:          nonce,
		CrashPoint:     crashPoint,
		Timestamp:      now,
	}

	select {
	case e.events <- event:
	default:
		// Le canal est plein : on ne bloque jamais la boucle du moteur.
		// Un consommateur lent (gateway) doit drainer plus vite ou augmenter EventBuffer.
	}
}

func (e *CrashEngine) currentServerSeedHash() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.serverSeedHash
}

func (e *CrashEngine) currentClientSeed() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.clientSeed
}

func (e *CrashEngine) currentNonce() int64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.nonce
}

// CurrentFairness retourne le hash du server seed engagé, le client seed et le nonce du round
// courant — exposé pour que la gateway réponde correctement à un STATE_SYNC (JOIN en cours de
// round). Ne retourne JAMAIS le server seed en clair avant le crash : utiliser les champs
// ServerSeed des Event de type EventCrash pour le reveal.
func (e *CrashEngine) CurrentFairness() (serverSeedHash, clientSeed string, nonce int64) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.serverSeedHash, e.clientSeed, e.nonce
}

// Events retourne le canal des événements diffusés par le moteur (consommé par la gateway WS).
func (e *CrashEngine) Events() <-chan Event {
	return e.events
}

// CurrentRound retourne une copie du round FSM courant.
func (e *CrashEngine) CurrentRound() (*game.Round, error) {
	return e.rounds.CurrentRound()
}

// CurrentMultiplier retourne le multiplicateur courant (thread-safe), consommé par la gateway
// pour les STATE_SYNC ou les lectures ponctuelles hors tick.
func (e *CrashEngine) CurrentMultiplier() float64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.multiplier
}

// PlaceBet délègue au BetManager la pose d'une mise sur un panel donné (1 à 4).
func (e *CrashEngine) PlaceBet(userID string, panel game.BetPanel, amount int64, cashoutTarget float64) (*game.Bet, error) {
	return e.bets.PlaceBet(userID, panel, amount, cashoutTarget)
}

// Cashout encaisse une mise au multiplicateur courant du moteur (jamais celui fourni par le client).
func (e *CrashEngine) Cashout(betID string) (*game.Bet, error) {
	now := time.Now()
	e.mu.RLock()
	mult := e.multiplier
	e.mu.RUnlock()
	return e.bets.Cashout(betID, mult, now)
}

// UserRoundBets retourne les mises actives d'un joueur sur le round courant (jusqu'à 4 panels).
func (e *CrashEngine) UserRoundBets(userID string) []game.Bet {
	return e.bets.UserRoundBets(userID)
}

// multiplierAtElapsed calcule le multiplicateur brut (non plafonné) pour un temps écoulé en ms.
func multiplierAtElapsed(elapsedMs int64, growthFactor float64) float64 {
	return math.Exp(growthFactor * float64(elapsedMs))
}
