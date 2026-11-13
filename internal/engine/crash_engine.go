package engine

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/tiidiio/223Crash/internal/game"
)

var (
	ErrEngineAlreadyRunning = errors.New("engine: already running")
	ErrNoActiveCycle        = errors.New("engine: no active round cycle")
	ErrChainCorrupted       = errors.New("engine: fairness chain corrompue")
)

// NonceStore fournit des nonces strictement croissants pour la génération provably fair.
// L'implémentation par défaut (inMemoryNonceStore) redémarre à 0 à chaque process — en
// production, injecter l'implémentation Mongo ($inc atomique) via Config.NonceStore.
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

// ChainRecord est l'entrée persistée de la chaîne de fairness (un enregistrement par round).
// ServerSeed est stocké côté serveur dès le commit (jamais exposé avant le crash) afin qu'un
// round interrompu puisse être révélé et vérifié après redémarrage.
type ChainRecord struct {
	RoundID        string
	Nonce          int64
	ServerSeed     string
	ServerSeedHash string
	ClientSeed     string
	CrashPoint     float64
	Revealed       bool
	Aborted        bool // round interrompu avant le crash (redémarrage) : révélé par la reprise
	CommittedAt    time.Time
	RevealedAt     time.Time
}

// ChainStore persiste la chaîne de fairness. Optionnel (nil = chaîne non persistée).
type ChainStore interface {
	// LoadLast retourne l'enregistrement de plus grand nonce, ou (nil, nil) si la chaîne est vide.
	LoadLast(ctx context.Context) (*ChainRecord, error)
	// Commit persiste le round (seed + hash) AVANT toute publication du hash.
	Commit(ctx context.Context, rec ChainRecord) error
	// Reveal marque le round comme révélé. Idempotent.
	Reveal(ctx context.Context, nonce int64, crashPoint float64, aborted bool) error
}

// Config regroupe les paramètres de timing et de courbe du moteur.
type Config struct {
	BettingDuration   time.Duration
	CrashedDuration   time.Duration
	TickInterval      time.Duration
	GrowthFactor      float64 // multiplier(t) = e^(GrowthFactor * elapsedMs)
	EventBuffer       int
	InitialClientSeed string     // seed genesis si la chaîne est vide
	NonceStore        NonceStore // optionnel — défaut : compteur in-memory
	ChainStore        ChainStore // optionnel — persistance de la chaîne de seeds
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
	EventRoundCreated  EventType = "ROUND_CREATED"
	EventBettingOpen   EventType = "BETTING_OPEN"
	EventBettingClosed EventType = "BETTING_CLOSED"
	EventRoundRunning  EventType = "ROUND_RUNNING"
	EventTick          EventType = "TICK" // seul type pouvant être abandonné si le buffer est plein
	EventAutoCashout   EventType = "AUTO_CASHOUT"
	EventCrash         EventType = "CRASH"
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
	CashedOutBets  []game.Bet // peuplé uniquement sur EventAutoCashout (payées à leur cible exacte)
	Timestamp      time.Time
}

// CrashEngine orchestre le cycle de vie d'un round.
//
// Frontière temporelle unique : crashDur = ceil(ln(crashPoint)/GrowthFactor) en ns depuis
// roundStartedAt (horloge monotone). Tout CASHOUT à elapsed >= crashDur est refusé, quel que
// soit le moment où la boucle de ticks se réveille. CASHOUT, auto-cashout et CRASH sont
// sérialisés par phaseMu : un cashout est soit entièrement avant le crash, soit refusé.
type CrashEngine struct {
	cfg Config

	rounds     *game.RoundManager
	bets       *game.BetManager
	nonceStore NonceStore
	chain      ChainStore

	// phaseMu sérialise cashout / auto-cashout / crash. Protège live, roundStartedAt, crashDur.
	phaseMu        sync.Mutex
	live           bool
	roundStartedAt time.Time
	crashDur       time.Duration

	mu             sync.RWMutex
	running        bool
	lastServerSeed string

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
		chain:      cfg.ChainStore,
		events:     make(chan Event, cfg.EventBuffer),
	}, nil
}

// Run exécute la boucle de cycles de round jusqu'à annulation du contexte.
// Au démarrage, la chaîne de fairness est rechargée et un éventuel round interrompu est révélé.
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

	if err := e.recoverChain(ctx); err != nil {
		return fmt.Errorf("engine: reprise de la chaîne de fairness: %w", err)
	}

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

// recoverChain recharge le dernier maillon de la chaîne. Un round non révélé (process arrêté en
// cours de round) est révélé comme interrompu après contrôle d'intégrité (hash + crash point),
// puis la chaîne continue : clientSeed(N) = SHA-256(serverSeed(N-1)).
func (e *CrashEngine) recoverChain(ctx context.Context) error {
	if e.chain == nil {
		return nil
	}
	rec, err := e.chain.LoadLast(ctx)
	if err != nil {
		return fmt.Errorf("lecture: %w", err)
	}
	if rec == nil {
		return nil
	}
	if HashServerSeed(rec.ServerSeed) != rec.ServerSeedHash {
		return fmt.Errorf("%w: hash du round %s ne correspond pas au seed", ErrChainCorrupted, rec.RoundID)
	}
	cp, err := CrashPoint(rec.ServerSeed, rec.ClientSeed, rec.Nonce)
	if err != nil {
		return err
	}
	if math.Abs(cp-rec.CrashPoint) >= verifyTolerance {
		return fmt.Errorf("%w: crash point du round %s ne se recalcule pas", ErrChainCorrupted, rec.RoundID)
	}
	if !rec.Revealed {
		if err := e.chain.Reveal(ctx, rec.Nonce, cp, true); err != nil {
			return fmt.Errorf("révélation du round interrompu %s: %w", rec.RoundID, err)
		}
	}
	e.mu.Lock()
	e.lastServerSeed = rec.ServerSeed
	e.mu.Unlock()
	return nil
}

// runRoundCycle : Created -> BettingOpen -> BettingClosed -> Running -> Crashed -> Settled.
func (e *CrashEngine) runRoundCycle(ctx context.Context) error {
	now := time.Now()

	round, err := e.rounds.CreateRound(now)
	if err != nil {
		return fmt.Errorf("création du round: %w", err)
	}

	// Commit persisté AVANT toute publication du hash.
	if err := e.prepareFairness(ctx, round.ID); err != nil {
		return fmt.Errorf("préparation provably fair: %w", err)
	}

	if err := e.bets.OpenRound(round.ID); err != nil {
		return fmt.Errorf("ouverture des mises: %w", err)
	}

	if err := e.emit(ctx, EventRoundCreated, round.ID, game.StateCreated, now); err != nil {
		return err
	}

	if err := e.rounds.OpenBetting(now); err != nil {
		return fmt.Errorf("transition betting_open: %w", err)
	}
	if err := e.emit(ctx, EventBettingOpen, round.ID, game.StateBettingOpen, now); err != nil {
		return err
	}

	if err := e.sleep(ctx, e.cfg.BettingDuration); err != nil {
		return err
	}

	closeNow := time.Now()
	if err := e.bets.CloseBetting(); err != nil {
		return fmt.Errorf("fermeture des mises: %w", err)
	}
	if err := e.rounds.CloseBetting(closeNow); err != nil {
		return fmt.Errorf("transition betting_closed: %w", err)
	}
	if err := e.emit(ctx, EventBettingClosed, round.ID, game.StateBettingClosed, closeNow); err != nil {
		return err
	}

	// --- RUNNING : démarrage atomique vis-à-vis de Cashout ---
	e.mu.RLock()
	crashPoint := e.crashPoint
	e.mu.RUnlock()
	crashDur := crashDuration(crashPoint, e.cfg.GrowthFactor)

	e.phaseMu.Lock()
	startNow := time.Now()
	if err := e.bets.StartRound(); err != nil {
		e.phaseMu.Unlock()
		return fmt.Errorf("démarrage des mises: %w", err)
	}
	if err := e.rounds.Start(startNow); err != nil {
		e.phaseMu.Unlock()
		return fmt.Errorf("transition running: %w", err)
	}
	e.roundStartedAt = startNow
	e.crashDur = crashDur
	e.live = true
	e.phaseMu.Unlock()

	e.mu.Lock()
	e.multiplier = 1.00
	e.mu.Unlock()

	if err := e.emit(ctx, EventRoundRunning, round.ID, game.StateRunning, startNow); err != nil {
		return err
	}

	if err := e.runTickLoop(ctx, round.ID, startNow, crashDur); err != nil {
		return err
	}

	// --- CRASHED ---
	crashNow, auto, lostBets, err := e.crashBets(crashPoint)
	if err != nil {
		return err
	}
	if err := e.rounds.Crash(crashNow); err != nil {
		return fmt.Errorf("transition crashed: %w", err)
	}

	e.mu.Lock()
	e.multiplier = crashPoint
	revealedSeed := e.serverSeed
	e.lastServerSeed = revealedSeed
	hash, clientSeed, nonce := e.serverSeedHash, e.clientSeed, e.nonce
	e.mu.Unlock()

	if len(auto) > 0 {
		if err := e.send(ctx, Event{
			Type: EventAutoCashout, RoundID: round.ID, State: game.StateRunning,
			Multiplier: crashPoint, ServerSeedHash: hash, ClientSeed: clientSeed, Nonce: nonce,
			CrashPoint: crashPoint, CashedOutBets: auto, Timestamp: crashNow,
		}); err != nil {
			return err
		}
	}

	if err := e.send(ctx, Event{
		Type: EventCrash, RoundID: round.ID, State: game.StateCrashed,
		Multiplier: crashPoint, ServerSeedHash: hash, ServerSeed: revealedSeed,
		ClientSeed: clientSeed, Nonce: nonce, CrashPoint: crashPoint,
		LostBets: lostBets, Timestamp: crashNow,
	}); err != nil {
		return err
	}

	// Révélation persistée (retry borné). En cas d'échec définitif, la reprise au redémarrage
	// révélera le round depuis le seed commité.
	if err := e.revealPersist(ctx, nonce, crashPoint); err != nil {
		return err
	}

	// --- SETTLED ---
	settleNow := time.Now()
	if err := e.rounds.Settle(settleNow); err != nil {
		return fmt.Errorf("transition settled: %w", err)
	}
	if err := e.emit(ctx, EventRoundSettled, round.ID, game.StateSettled, settleNow); err != nil {
		return err
	}

	if err := e.sleep(ctx, e.cfg.CrashedDuration); err != nil {
		return err
	}

	if err := e.bets.ResetRound(); err != nil {
		return fmt.Errorf("reset du bet manager: %w", err)
	}
	return nil
}

// crashBets exécute, sous phaseMu, la séquence atomique : dernier balayage auto-cashout
// (cibles <= crashPoint, payées à la cible), fermeture du live, règlement des mises perdantes.
func (e *CrashEngine) crashBets(crashPoint float64) (time.Time, []game.Bet, []game.Bet, error) {
	e.phaseMu.Lock()
	defer e.phaseMu.Unlock()

	now := time.Now()
	auto := e.sweepAutoLocked(crashPoint, now)
	e.live = false
	lost, err := e.bets.Crash(now)
	if err != nil {
		return now, auto, nil, fmt.Errorf("règlement des mises perdantes: %w", err)
	}
	return now, auto, lost, nil
}

// sweepAutoLocked paie à leur cible exacte les mises dont 0 < cible <= limit. phaseMu requis.
// Règle de gain publique : crash >= cible (P = 0,97/cible). Cible tronquée à 2 décimales,
// comme le multiplicateur de paiement du BetManager.
func (e *CrashEngine) sweepAutoLocked(limit float64, now time.Time) []game.Bet {
	var paid []game.Bet
	for _, b := range e.bets.RoundBets() {
		if b.State != game.BetPending || b.CashoutTarget <= 0 {
			continue
		}
		target := math.Floor(b.CashoutTarget*100+1e-9) / 100
		if target < 1.00 || target > limit {
			continue
		}
		settled, err := e.bets.Cashout(b.ID, target, now)
		if err != nil {
			continue // déjà encaissée (cashout manuel concurrent) : jamais de double paiement
		}
		paid = append(paid, *settled)
	}
	return paid
}

// runTickLoop diffuse la courbe, exécute les auto-cashouts et rend la main à l'instant exact du crash.
func (e *CrashEngine) runTickLoop(ctx context.Context, roundID string, startedAt time.Time, crashDur time.Duration) error {
	ticker := time.NewTicker(e.cfg.TickInterval)
	defer ticker.Stop()

	remaining := crashDur - time.Since(startedAt)
	if remaining < 0 {
		remaining = 0
	}
	crashTimer := time.NewTimer(remaining)
	defer crashTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-crashTimer.C:
			return nil
		case tickTime := <-ticker.C:
			elapsed := tickTime.Sub(startedAt)
			if elapsed < 0 {
				elapsed = 0
			}
			if elapsed >= crashDur {
				return nil
			}
			raw := multiplierAt(elapsed, e.cfg.GrowthFactor)

			auto := e.autoCashoutTick(startedAt, crashDur, raw)

			e.mu.Lock()
			e.multiplier = raw
			e.mu.Unlock()

			if len(auto) > 0 {
				e.mu.RLock()
				ev := Event{
					Type: EventAutoCashout, RoundID: roundID, State: game.StateRunning,
					Multiplier: raw, ServerSeedHash: e.serverSeedHash, ClientSeed: e.clientSeed,
					Nonce: e.nonce, CashedOutBets: auto, Timestamp: tickTime,
				}
				e.mu.RUnlock()
				if err := e.send(ctx, ev); err != nil {
					return err
				}
			}
			e.emitTick(roundID, tickTime)
		}
	}
}

// autoCashoutTick paie les cibles atteintes (cible <= multiplicateur courant), uniquement si
// le round est encore vivant et en deçà de la frontière du crash.
func (e *CrashEngine) autoCashoutTick(startedAt time.Time, crashDur time.Duration, current float64) []game.Bet {
	e.phaseMu.Lock()
	defer e.phaseMu.Unlock()
	now := time.Now()
	if !e.live || now.Sub(startedAt) >= crashDur {
		return nil
	}
	return e.sweepAutoLocked(current, now)
}

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

// prepareFairness génère les données du round, les persiste (commit) puis les expose.
func (e *CrashEngine) prepareFairness(ctx context.Context, roundID string) error {
	nonce, err := e.nonceStore.NextNonce(ctx)
	if err != nil {
		return fmt.Errorf("obtention du nonce: %w", err)
	}

	e.mu.RLock()
	last := e.lastServerSeed
	e.mu.RUnlock()

	var clientSeed string
	switch {
	case last != "":
		clientSeed = NextClientSeed(last)
	case e.cfg.InitialClientSeed != "":
		clientSeed = e.cfg.InitialClientSeed
	default:
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
	hash := HashServerSeed(serverSeed)

	if e.chain != nil {
		if err := e.chain.Commit(ctx, ChainRecord{
			RoundID: roundID, Nonce: nonce, ServerSeed: serverSeed, ServerSeedHash: hash,
			ClientSeed: clientSeed, CrashPoint: crashPoint, CommittedAt: time.Now(),
		}); err != nil {
			return fmt.Errorf("commit de la chaîne: %w", err)
		}
	}

	e.mu.Lock()
	e.serverSeed = serverSeed
	e.serverSeedHash = hash
	e.clientSeed = clientSeed
	e.nonce = nonce
	e.crashPoint = crashPoint
	e.multiplier = 1.00
	e.mu.Unlock()
	return nil
}

func (e *CrashEngine) revealPersist(ctx context.Context, nonce int64, crashPoint float64) error {
	if e.chain == nil {
		return nil
	}
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if err = e.chain.Reveal(ctx, nonce, crashPoint, false); err == nil {
			return nil
		}
		if sErr := e.sleep(ctx, time.Duration(attempt+1)*200*time.Millisecond); sErr != nil {
			return sErr
		}
	}
	return fmt.Errorf("persistance de la révélation (nonce %d): %w", nonce, err)
}

func (e *CrashEngine) snapshot(t EventType, roundID string, state game.RoundState, now time.Time) Event {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return Event{
		Type: t, RoundID: roundID, State: state, Multiplier: e.multiplier,
		ServerSeedHash: e.serverSeedHash, ClientSeed: e.clientSeed, Nonce: e.nonce,
		CrashPoint: 0, Timestamp: now,
	}
}

// emit publie un événement d'état critique : jamais abandonné, interrompu seulement par ctx.
func (e *CrashEngine) emit(ctx context.Context, t EventType, roundID string, state game.RoundState, now time.Time) error {
	ev := e.snapshot(t, roundID, state, now)
	if t == EventRoundCreated {
		ev.Multiplier = 1.00
	}
	return e.send(ctx, ev)
}

func (e *CrashEngine) send(ctx context.Context, ev Event) error {
	select {
	case e.events <- ev:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// emitTick : le TICK est le seul événement abandonnable (le suivant porte un multiplicateur plus récent).
func (e *CrashEngine) emitTick(roundID string, now time.Time) {
	ev := e.snapshot(EventTick, roundID, game.StateRunning, now)
	select {
	case e.events <- ev:
	default:
	}
}

// CurrentFairness retourne hash engagé, client seed et nonce du round courant (jamais le server seed).
func (e *CrashEngine) CurrentFairness() (serverSeedHash, clientSeed string, nonce int64) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.serverSeedHash, e.clientSeed, e.nonce
}

// Events retourne le canal des événements diffusés par le moteur.
func (e *CrashEngine) Events() <-chan Event { return e.events }

// CurrentRound retourne une copie du round FSM courant.
func (e *CrashEngine) CurrentRound() (*game.Round, error) { return e.rounds.CurrentRound() }

// CurrentMultiplier retourne le multiplicateur du dernier tick (affichage / STATE_SYNC uniquement :
// les paiements n'utilisent jamais cette valeur).
func (e *CrashEngine) CurrentMultiplier() float64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.multiplier
}

// PlaceBet délègue au BetManager la pose d'une mise sur un panel donné (1 à 4).
func (e *CrashEngine) PlaceBet(userID string, panel game.BetPanel, amount int64, cashoutTarget float64) (*game.Bet, error) {
	return e.bets.PlaceBet(userID, panel, amount, cashoutTarget)
}

// GetPanelBet retourne la mise active d'un joueur sur un panel du round courant.
func (e *CrashEngine) GetPanelBet(userID string, panel game.BetPanel) (game.Bet, error) {
	return e.bets.GetPanelBet(userID, panel)
}

// Cashout encaisse une mise au multiplicateur calculé à l'instant de l'appel (horloge monotone).
// Refusé si le round n'est pas en cours ou si elapsed >= crashDur : aucune fenêtre après le crash.
func (e *CrashEngine) Cashout(betID string) (*game.Bet, error) {
	e.phaseMu.Lock()
	defer e.phaseMu.Unlock()

	if !e.live {
		return nil, game.ErrRoundNotRunning
	}
	now := time.Now()
	elapsed := now.Sub(e.roundStartedAt)
	if elapsed >= e.crashDur {
		return nil, game.ErrRoundNotRunning
	}
	return e.bets.Cashout(betID, multiplierAt(elapsed, e.cfg.GrowthFactor), now)
}

// UserRoundBets retourne les mises actives d'un joueur sur le round courant.
func (e *CrashEngine) UserRoundBets(userID string) []game.Bet { return e.bets.UserRoundBets(userID) }

// multiplierAtElapsed calcule le multiplicateur brut pour un temps écoulé en ms (conservé : API existante).
func multiplierAtElapsed(elapsedMs int64, growthFactor float64) float64 {
	return math.Exp(growthFactor * float64(elapsedMs))
}

// multiplierAt calcule e^(g * t) avec t en ms fractionnaires (résolution ns).
func multiplierAt(elapsed time.Duration, growthFactor float64) float64 {
	return math.Exp(growthFactor * float64(elapsed) / float64(time.Millisecond))
}

// crashDuration : plus petit instant (ns) où e^(g*t) >= crashPoint, soit ceil(ln(cp)/g).
func crashDuration(crashPoint, growthFactor float64) time.Duration {
	if crashPoint <= 1.0 {
		return 0
	}
	d := time.Duration(math.Ceil(math.Log(crashPoint) / growthFactor * float64(time.Millisecond)))
	for multiplierAt(d, growthFactor) < crashPoint { // garde-fou arrondis flottants
		d++
	}
	return d
}
