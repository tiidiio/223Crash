package runtimeregistry

import (
	"context"
	"testing"
	"time"

	"github.com/tiidiio/223Crash/internal/runtimeprobe"
)

type fake struct {
	id    string
	res   runtimeprobe.Result
	calls int
}

func (f *fake) ProductID() string { return f.id }
func (f *fake) Check(context.Context) runtimeprobe.Result {
	f.calls++
	return f.res
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newReg(c *clock, m map[string]bool, fs ...*fake) *Registry {
	cs := make([]Checker, 0, len(fs))
	for _, f := range fs {
		cs = append(cs, f)
	}
	return New(cs, Options{Now: c.now, Maintenance: m})
}

func get(t *testing.T, r *Registry, id string) Snapshot {
	t.Helper()
	s, err := r.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get(%s): %v", id, err)
	}
	return s
}

func TestAvailable(t *testing.T) {
	c := &clock{time.Unix(1000, 0)}
	f := &fake{id: "g1", res: runtimeprobe.Result{Status: runtimeprobe.StateOK, LatencyMs: 12}}
	s := get(t, newReg(c, nil, f), "g1")
	if s.RuntimeStatus != StateAvailable || s.HealthStatus != "healthy" || s.LatencyMs != 12 || s.LastSuccessAt == nil {
		t.Fatalf("inattendu: %+v", s)
	}
	if s.ReadinessStatus != "not_reported" {
		t.Fatalf("readiness ne doit pas être inventée: %s", s.ReadinessStatus)
	}
}

func TestUnavailableAndTimeout(t *testing.T) {
	for _, reason := range []string{"runtime_unreachable", "runtime_timeout", "runtime_http_503"} {
		c := &clock{time.Unix(1000, 0)}
		f := &fake{id: "g1", res: runtimeprobe.Result{Status: runtimeprobe.StateDown, Reason: reason}}
		s := get(t, newReg(c, nil, f), "g1")
		if s.RuntimeStatus != StateUnavailable || s.FailureReason != reason || s.LastSuccessAt != nil {
			t.Fatalf("%s: %+v", reason, s)
		}
	}
}

func TestDegradedWhenDependencyDownOrNotReady(t *testing.T) {
	for _, reason := range []string{"runtime_dependency_down", "runtime_health_invalid", "runtime_not_ok"} {
		c := &clock{time.Unix(1000, 0)}
		f := &fake{id: "g1", res: runtimeprobe.Result{Status: runtimeprobe.StateDown, Reason: reason}}
		s := get(t, newReg(c, nil, f), "g1")
		if s.RuntimeStatus != StateDegraded || s.ReadinessStatus != "not_ready" {
			t.Fatalf("%s: %+v", reason, s)
		}
	}
}

func TestNotConfiguredIsUnknownNeverAvailable(t *testing.T) {
	c := &clock{time.Unix(1000, 0)}
	f := &fake{id: "g1", res: runtimeprobe.Result{Status: runtimeprobe.StateUnavailable, Reason: "runtime_url_not_configured"}}
	s := get(t, newReg(c, nil, f), "g1")
	if s.RuntimeStatus != StateUnknown {
		t.Fatalf("inattendu: %+v", s)
	}
}

func TestInvalidAdapterStatusIsUnknown(t *testing.T) {
	c := &clock{time.Unix(1000, 0)}
	f := &fake{id: "g1", res: runtimeprobe.Result{Status: "banana"}}
	s := get(t, newReg(c, nil, f), "g1")
	if s.RuntimeStatus != StateUnknown || s.FailureReason != "invalid_probe_status" {
		t.Fatalf("inattendu: %+v", s)
	}
}

func TestMaintenanceSkipsProbe(t *testing.T) {
	c := &clock{time.Unix(1000, 0)}
	f := &fake{id: "g1", res: runtimeprobe.Result{Status: runtimeprobe.StateOK}}
	s := get(t, newReg(c, map[string]bool{"g1": true}, f), "g1")
	if s.RuntimeStatus != StateMaintenance || f.calls != 0 {
		t.Fatalf("inattendu: %+v calls=%d", s, f.calls)
	}
}

func TestCacheAndRefreshRateLimit(t *testing.T) {
	c := &clock{time.Unix(1000, 0)}
	f := &fake{id: "g1", res: runtimeprobe.Result{Status: runtimeprobe.StateOK}}
	r := newReg(c, nil, f)
	get(t, r, "g1")
	get(t, r, "g1")
	if f.calls != 1 {
		t.Fatalf("cache ignoré: %d appels", f.calls)
	}
	if _, did, _ := r.Refresh(context.Background(), "g1"); did {
		t.Fatal("refresh trop rapproché aurait dû être refusé")
	}
	c.t = c.t.Add(3 * time.Second)
	if _, did, _ := r.Refresh(context.Background(), "g1"); !did || f.calls != 2 {
		t.Fatalf("refresh attendu: calls=%d", f.calls)
	}
}

func TestLastSuccessKeptAfterFailure(t *testing.T) {
	c := &clock{time.Unix(1000, 0)}
	f := &fake{id: "g1", res: runtimeprobe.Result{Status: runtimeprobe.StateOK}}
	r := newReg(c, nil, f)
	ok := get(t, r, "g1")
	c.t = c.t.Add(10 * time.Second)
	f.res = runtimeprobe.Result{Status: runtimeprobe.StateDown, Reason: "runtime_unreachable"}
	s := get(t, r, "g1")
	if s.RuntimeStatus != StateUnavailable || s.LastSuccessAt == nil || !s.LastSuccessAt.Equal(*ok.LastSuccessAt) {
		t.Fatalf("inattendu: %+v", s)
	}
}

func TestMultipleProductsIndependent(t *testing.T) {
	c := &clock{time.Unix(1000, 0)}
	a := &fake{id: "a", res: runtimeprobe.Result{Status: runtimeprobe.StateOK}}
	b := &fake{id: "b", res: runtimeprobe.Result{Status: runtimeprobe.StateDown, Reason: "runtime_unreachable"}}
	l := newReg(c, nil, a, b).List(context.Background())
	if len(l) != 2 || l[0].GameID != "a" || l[0].RuntimeStatus != StateAvailable ||
		l[1].GameID != "b" || l[1].RuntimeStatus != StateUnavailable {
		t.Fatalf("inattendu: %+v", l)
	}
}

func TestUnknownGame(t *testing.T) {
	r := newReg(&clock{time.Unix(1000, 0)}, nil)
	if _, err := r.Get(context.Background(), "nope"); err != ErrUnknownGame {
		t.Fatalf("err=%v", err)
	}
	if _, _, err := r.Refresh(context.Background(), "nope"); err != ErrUnknownGame {
		t.Fatalf("err=%v", err)
	}
}
