package game

import (
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"
)

var (
	ErrNoActiveRound   = errors.New("no active round")
	ErrRoundInProgress = errors.New("round already in progress")
	ErrInvalidState    = errors.New("invalid round state")
	ErrInvalidTime     = errors.New("invalid transition timestamp")
)

// RoundState représente l'état du cycle de vie d'un round.
type RoundState string

const (
	StateCreated       RoundState = "CREATED"
	StateBettingOpen   RoundState = "BETTING_OPEN"
	StateBettingClosed RoundState = "BETTING_CLOSED"
	StateRunning       RoundState = "RUNNING"
	StateCrashed       RoundState = "CRASHED"
	StateSettled       RoundState = "SETTLED"
)

// Round représente l'état complet d'un round.
type Round struct {
	ID string

	State RoundState

	CreatedAt       time.Time
	BettingOpenedAt time.Time
	BettingClosedAt time.Time
	StartedAt       time.Time
	CrashedAt       time.Time
	SettledAt       time.Time
}

// RoundEvent représente un changement d'état observable par les autres composants.
type RoundEvent struct {
	RoundID   string
	State     RoundState
	Timestamp time.Time
}

// RoundManager est responsable uniquement de l'état du round.
// Il ne contient pas la logique du moteur/ticker.
type RoundManager struct {
	mu sync.RWMutex

	current *Round

	nextID uint64

	events chan RoundEvent
}

// NewRoundManager crée un RoundManager.
func NewRoundManager(eventBuffer int) *RoundManager {
	if eventBuffer < 1 {
		eventBuffer = 64
	}

	return &RoundManager{
		events: make(chan RoundEvent, eventBuffer),
	}
}

// CreateRound crée un nouveau round.
//
// Un round précédent doit être Settled avant de pouvoir en créer un nouveau.
func (m *RoundManager) CreateRound(now time.Time) (*Round, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if now.IsZero() {
		return nil, ErrInvalidTime
	}

	if m.current != nil && m.current.State != StateSettled {
		return nil, ErrRoundInProgress
	}

	m.nextID++

	round := &Round{
		ID:        formatRoundID(m.nextID),
		State:     StateCreated,
		CreatedAt: now,
	}

	m.current = round

	m.publishEventLocked(RoundEvent{
		RoundID:   round.ID,
		State:     StateCreated,
		Timestamp: now,
	})

	return cloneRound(round), nil
}

// OpenBetting ouvre les paris.
func (m *RoundManager) OpenBetting(now time.Time) error {
	return m.transition(
		StateCreated,
		StateBettingOpen,
		now,
	)
}

// CloseBetting ferme les paris.
func (m *RoundManager) CloseBetting(now time.Time) error {
	return m.transition(
		StateBettingOpen,
		StateBettingClosed,
		now,
	)
}

// Start démarre le round.
func (m *RoundManager) Start(now time.Time) error {
	return m.transition(
		StateBettingClosed,
		StateRunning,
		now,
	)
}

// Crash termine la phase de jeu.
func (m *RoundManager) Crash(now time.Time) error {
	return m.transition(
		StateRunning,
		StateCrashed,
		now,
	)
}

// Settle finalise le round.
func (m *RoundManager) Settle(now time.Time) error {
	return m.transition(
		StateCrashed,
		StateSettled,
		now,
	)
}

// Reset supprime le round courant.
//
// Cette opération est volontairement stricte :
// seul un round SETTLED peut être reset.
func (m *RoundManager) Reset() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.current == nil {
		return ErrNoActiveRound
	}

	if m.current.State != StateSettled {
		return ErrRoundInProgress
	}

	m.current = nil

	return nil
}

// CurrentRound retourne une copie du round courant.
//
// Une copie est retournée pour empêcher un appelant externe
// de modifier l'état interne du RoundManager.
func (m *RoundManager) CurrentRound() (*Round, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.current == nil {
		return nil, ErrNoActiveRound
	}

	return cloneRound(m.current), nil
}

// GetState retourne l'état actuel du round.
func (m *RoundManager) GetState() (RoundState, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.current == nil {
		return "", ErrNoActiveRound
	}

	return m.current.State, nil
}

// Events retourne le canal des événements.
//
// Les consommateurs peuvent l'utiliser plus tard pour le Gateway,
// WebSocket, Redis, NATS, etc.
func (m *RoundManager) Events() <-chan RoundEvent {
	return m.events
}

// transition effectue une transition atomique.
//
// IMPORTANT : l'événement est préparé sous verrou puis publié
// sans jamais exécuter de callback utilisateur sous le mutex.
func (m *RoundManager) transition(
	from RoundState,
	to RoundState,
	now time.Time,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.current == nil {
		return ErrNoActiveRound
	}

	if now.IsZero() {
		return ErrInvalidTime
	}

	if m.current.State != from {
		return fmt.Errorf(
			"%w: expected=%s actual=%s",
			ErrInvalidState,
			from,
			m.current.State,
		)
	}

	if !isValidTimestamp(m.current, now) {
		return ErrInvalidTime
	}

	m.current.State = to

	switch to {
	case StateBettingOpen:
		m.current.BettingOpenedAt = now

	case StateBettingClosed:
		m.current.BettingClosedAt = now

	case StateRunning:
		m.current.StartedAt = now

	case StateCrashed:
		m.current.CrashedAt = now

	case StateSettled:
		m.current.SettledAt = now
	}

	m.publishEventLocked(RoundEvent{
		RoundID:   m.current.ID,
		State:     to,
		Timestamp: now,
	})

	return nil
}

// isValidTimestamp garantit que le temps ne remonte jamais
// dans le cycle de vie du round.
func isValidTimestamp(round *Round, now time.Time) bool {
	timestamps := []time.Time{
		round.CreatedAt,
		round.BettingOpenedAt,
		round.BettingClosedAt,
		round.StartedAt,
		round.CrashedAt,
		round.SettledAt,
	}

	for _, timestamp := range timestamps {
		if !timestamp.IsZero() && now.Before(timestamp) {
			return false
		}
	}

	return true
}

// publishEventLocked publie un événement sans bloquer indéfiniment
// le RoundManager.
//
// En production, un événement ne doit jamais pouvoir bloquer
// le moteur de jeu.
func (m *RoundManager) publishEventLocked(event RoundEvent) {
	select {
	case m.events <- event:
	default:
		// Le canal est plein.
		//
		// Pour la première version du moteur, on ne bloque pas
		// le cycle du round.
		//
		// Une stratégie durable de backpressure / métriques pourra
		// être ajoutée au niveau EventBus.
	}
}

// cloneRound retourne une copie indépendante du round.
func cloneRound(round *Round) *Round {
	if round == nil {
		return nil
	}

	copy := *round

	return &copy
}

// formatRoundID génère un identifiant stable pour le round.
func formatRoundID(id uint64) string {
	return "round-" + strconv.FormatUint(id, 10)
}
