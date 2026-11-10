// Package runtimeregistry agrège l'état réel des runtimes de produits de jeu.
//
// Il ne connaît que des GameID opaques et des sondes (runtimeprobe). Il n'importe
// aucun package de jeu. Un produit au catalogue n'est jamais AVAILABLE sans une
// sonde qui l'a mesuré.
package runtimeregistry

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/tiidiio/223Gaming/internal/runtimeprobe"
)

type State string

const (
	StateAvailable   State = "AVAILABLE"
	StateStarting    State = "STARTING" // réservé : jamais déduit sans preuve explicite
	StateUnavailable State = "UNAVAILABLE"
	StateDegraded    State = "DEGRADED"
	StateUnknown     State = "UNKNOWN"
	StateMaintenance State = "MAINTENANCE"
)

const (
	DefaultTTL        = 5 * time.Second
	DefaultMinRefresh = 2 * time.Second
)

var ErrUnknownGame = errors.New("runtimeregistry: unknown game id")

// Checker est satisfait par *runtimeprobe.Probe.
type Checker interface {
	ProductID() string
	Check(ctx context.Context) runtimeprobe.Result
}

// Snapshot est l'état publié d'un runtime. FailureReason ne contient jamais d'URL.
type Snapshot struct {
	GameID          string     `json:"game_id"`
	RuntimeStatus   State      `json:"runtime_status"`
	HealthStatus    string     `json:"health_status"`
	ReadinessStatus string     `json:"readiness_status"`
	LastCheckedAt   time.Time  `json:"last_checked_at"`
	LastSuccessAt   *time.Time `json:"last_success_at"`
	LatencyMs       int64      `json:"latency_ms"`
	FailureReason   string     `json:"failure_reason,omitempty"`
}

type Options struct {
	TTL        time.Duration
	MinRefresh time.Duration
	// Maintenance : GameID -> true si la maintenance est explicitement déclarée.
	Maintenance map[string]bool
	Now         func() time.Time
}

type Registry struct {
	mu          sync.Mutex
	checkers    map[string]Checker
	order       []string
	snaps       map[string]Snapshot
	lastSuccess map[string]time.Time
	inflight    map[string]*flight
	ttl         time.Duration
	minRefresh  time.Duration
	maint       map[string]bool
	now         func() time.Time
}

func New(checkers []Checker, o Options) *Registry {
	r := &Registry{
		checkers:    make(map[string]Checker),
		snaps:       make(map[string]Snapshot),
		lastSuccess: make(map[string]time.Time),
		inflight:    make(map[string]*flight),
		ttl:         o.TTL,
		minRefresh:  o.MinRefresh,
		maint:       make(map[string]bool),
		now:         o.Now,
	}
	if r.ttl <= 0 {
		r.ttl = DefaultTTL
	}
	if r.minRefresh <= 0 {
		r.minRefresh = DefaultMinRefresh
	}
	if r.now == nil {
		r.now = time.Now
	}
	for id, v := range o.Maintenance {
		if v {
			r.maint[strings.TrimSpace(id)] = true
		}
	}
	for _, c := range checkers {
		if c == nil {
			continue
		}
		id := strings.TrimSpace(c.ProductID())
		if id == "" {
			continue
		}
		if _, dup := r.checkers[id]; dup {
			continue
		}
		r.checkers[id] = c
		r.order = append(r.order, id)
	}
	return r
}

// IDs retourne les GameID suivis, dans l'ordre d'enregistrement.
func (r *Registry) IDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.order...)
}

// flight est une sonde en cours pour un GameID. Les appelants concurrents attendent
// son résultat au lieu de lancer une seconde sonde.
type flight struct {
	done chan struct{}
	snap Snapshot
}

var errProbeAborted = errors.New("runtimeregistry: probe aborted")

// Get retourne l'état, depuis le cache s'il est frais, sinon par une nouvelle sonde.
func (r *Registry) Get(ctx context.Context, id string) (Snapshot, error) {
	snap, _, err := r.probe(ctx, id, r.ttl)
	return snap, err
}

// List retourne l'état de tous les produits. Les sondes sont exécutées l'une après
// l'autre, chacune bornée par son propre timeout et jamais sous le verrou du registre.
func (r *Registry) List(ctx context.Context) []Snapshot {
	ids := r.IDs()
	out := make([]Snapshot, 0, len(ids))
	for _, id := range ids {
		s, _, err := r.probe(ctx, id, r.ttl)
		if err != nil {
			return out
		}
		out = append(out, s)
	}
	return out
}

// Refresh force une nouvelle sonde, sauf si la dernière date de moins de MinRefresh.
// Le booléen indique si le résultat a été mesuré pendant cet appel.
func (r *Registry) Refresh(ctx context.Context, id string) (Snapshot, bool, error) {
	return r.probe(ctx, id, r.minRefresh)
}

// probe retourne le snapshot en cache s'il a moins de maxAge ; sinon il exécute une
// sonde HORS verrou. Une seule sonde par GameID est en cours à la fois.
func (r *Registry) probe(ctx context.Context, id string, maxAge time.Duration) (Snapshot, bool, error) {
	r.mu.Lock()
	checker, ok := r.checkers[id]
	if !ok {
		r.mu.Unlock()
		return Snapshot{}, false, ErrUnknownGame
	}
	if s, ok := r.snaps[id]; ok && r.now().Sub(s.LastCheckedAt) < maxAge {
		r.mu.Unlock()
		return s, false, nil
	}
	if f, ok := r.inflight[id]; ok {
		r.mu.Unlock()
		select {
		case <-f.done:
			if f.snap.GameID == "" {
				return Snapshot{}, false, errProbeAborted
			}
			return f.snap, true, nil
		case <-ctx.Done():
			return Snapshot{}, false, ctx.Err()
		}
	}
	f := &flight{done: make(chan struct{})}
	r.inflight[id] = f
	maint := r.maint[id]
	r.mu.Unlock()

	finished := false
	defer func() {
		if !finished {
			// Panique dans le Checker : ne jamais laisser les appelants bloqués.
			r.mu.Lock()
			delete(r.inflight, id)
			r.mu.Unlock()
			close(f.done)
		}
	}()

	// Le réseau n'est touché qu'ici, sans verrou. La sonde est bornée par son propre
	// timeout ; elle ne dépend pas de l'annulation de la requête du leader.
	snap := r.measure(context.WithoutCancel(ctx), id, checker, maint)

	r.mu.Lock()
	snap = r.publishLocked(id, snap)
	delete(r.inflight, id)
	r.mu.Unlock()

	f.snap = snap
	finished = true
	close(f.done)
	return snap, true, nil
}

// measure exécute la sonde. Il ne prend aucun verrou.
func (r *Registry) measure(ctx context.Context, id string, c Checker, maint bool) Snapshot {
	snap := Snapshot{GameID: id, LastCheckedAt: r.now().UTC()}
	if maint {
		snap.RuntimeStatus = StateMaintenance
		snap.HealthStatus = "unknown"
		snap.ReadinessStatus = "not_ready"
		snap.FailureReason = "maintenance_declared"
		return snap
	}
	res := c.Check(ctx)
	if !res.CheckedAt.IsZero() {
		snap.LastCheckedAt = res.CheckedAt.UTC()
	}
	snap.LatencyMs = res.LatencyMs
	snap.RuntimeStatus, snap.HealthStatus, snap.ReadinessStatus, snap.FailureReason = classify(res)
	return snap
}

// publishLocked enregistre le snapshot et conserve le dernier succès. mu doit être tenu.
func (r *Registry) publishLocked(id string, snap Snapshot) Snapshot {
	if snap.RuntimeStatus == StateAvailable {
		r.lastSuccess[id] = snap.LastCheckedAt
	}
	if t, ok := r.lastSuccess[id]; ok {
		tt := t
		snap.LastSuccessAt = &tt
	}
	r.snaps[id] = snap
	return snap
}

// classify traduit un résultat de sonde en état. Jamais d'AVAILABLE hors StateOK.
func classify(res runtimeprobe.Result) (State, string, string, string) {
	switch res.Status {
	case runtimeprobe.StateOK:
		// /healthz ok ne prouve pas la vivacité de la boucle de jeu : readiness non rapportée.
		return StateAvailable, "healthy", "not_reported", ""
	case runtimeprobe.StateUnavailable:
		return StateUnknown, "unknown", "unknown", res.Reason
	case runtimeprobe.StateDown:
		switch res.Reason {
		case "runtime_dependency_down", "runtime_health_invalid", "runtime_not_ok":
			return StateDegraded, "unhealthy", "not_ready", res.Reason
		default:
			return StateUnavailable, "unreachable", "not_ready", res.Reason
		}
	default:
		return StateUnknown, "unknown", "unknown", "invalid_probe_status"
	}
}
