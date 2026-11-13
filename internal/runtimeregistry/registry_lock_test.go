package runtimeregistry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tiidiio/223Crash/internal/runtimeprobe"
)

// gateChecker bloque dans Check jusqu'à release : il simule un runtime lent.
type gateChecker struct {
	id      string
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func newGate(id string) *gateChecker {
	return &gateChecker{id: id, entered: make(chan struct{}), release: make(chan struct{})}
}

func (g *gateChecker) ProductID() string { return g.id }

func (g *gateChecker) Check(context.Context) runtimeprobe.Result {
	if g.calls.Add(1) == 1 {
		close(g.entered)
	}
	<-g.release
	return runtimeprobe.Result{Status: runtimeprobe.StateOK, CheckedAt: time.Now()}
}

func waitEntered(t *testing.T, g *gateChecker) {
	t.Helper()
	select {
	case <-g.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("la sonde n'a pas démarré")
	}
}

func TestRegistryProbeOutsideLock(t *testing.T) {
	g := newGate("slow")
	r := New([]Checker{g}, Options{})

	done := make(chan Snapshot, 1)
	go func() {
		s, _ := r.Get(context.Background(), "slow")
		done <- s
	}()
	waitEntered(t, g)

	idsDone := make(chan struct{})
	go func() {
		_ = r.IDs()
		close(idsDone)
	}()
	select {
	case <-idsDone:
	case <-time.After(time.Second):
		close(g.release)
		t.Fatal("verrou du registre tenu pendant la sonde")
	}

	close(g.release)
	select {
	case s := <-done:
		if s.RuntimeStatus != StateAvailable {
			t.Fatalf("état = %s, attendu AVAILABLE", s.RuntimeStatus)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Get n'a pas rendu la main")
	}
}

func TestRegistryCoalescesConcurrentProbes(t *testing.T) {
	g := newGate("slow")
	r := New([]Checker{g}, Options{})

	const n = 5
	var wg sync.WaitGroup
	results := make(chan State, n)
	call := func() {
		defer wg.Done()
		s, err := r.Get(context.Background(), "slow")
		if err != nil {
			results <- State("ERR:" + err.Error())
			return
		}
		results <- s.RuntimeStatus
	}

	wg.Add(1)
	go call()
	waitEntered(t, g)
	for i := 1; i < n; i++ {
		wg.Add(1)
		go call()
	}
	time.Sleep(50 * time.Millisecond)
	close(g.release)
	wg.Wait()
	close(results)

	for st := range results {
		if st != StateAvailable {
			t.Errorf("état = %s, attendu AVAILABLE", st)
		}
	}
	if c := g.calls.Load(); c != 1 {
		t.Fatalf("sondes exécutées = %d, attendu 1", c)
	}
}

func TestRegistryStopRestartTransitions(t *testing.T) {
	var up atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !up.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","mongo":"connected"}`))
	}))
	defer srv.Close()

	now := time.Now()
	r := New(
		[]Checker{runtimeprobe.New("223crash", srv.URL).WithTimeout(time.Second)},
		Options{Now: func() time.Time { return now }},
	)
	probeNow := func() Snapshot {
		now = now.Add(10 * time.Second)
		s, err := r.Get(context.Background(), "223crash")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		return s
	}

	// Moteur en erreur : jamais AVAILABLE.
	s := probeNow()
	if s.RuntimeStatus != StateUnavailable || s.FailureReason != "runtime_http_500" || s.LastSuccessAt != nil {
		t.Fatalf("500 : état=%s raison=%s lastSuccess=%v", s.RuntimeStatus, s.FailureReason, s.LastSuccessAt)
	}

	// Moteur sain : AVAILABLE.
	up.Store(true)
	s = probeNow()
	if s.RuntimeStatus != StateAvailable || s.LastSuccessAt == nil {
		t.Fatalf("sain : état=%s lastSuccess=%v", s.RuntimeStatus, s.LastSuccessAt)
	}

	// Moteur en panne après un succès : UNAVAILABLE, dernier succès conservé.
	up.Store(false)
	s = probeNow()
	if s.RuntimeStatus != StateUnavailable || s.LastSuccessAt == nil {
		t.Fatalf("panne : état=%s lastSuccess=%v", s.RuntimeStatus, s.LastSuccessAt)
	}

	// Moteur arrêté (port fermé) : UNAVAILABLE / runtime_unreachable.
	srv.Close()
	s = probeNow()
	if s.RuntimeStatus != StateUnavailable || s.FailureReason != "runtime_unreachable" {
		t.Fatalf("arrêt : état=%s raison=%s", s.RuntimeStatus, s.FailureReason)
	}
}
