package runtimeprobe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func serve(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}

func jsonHandler(code int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}
}

func TestCheck(t *testing.T) {
	cases := []struct {
		name       string
		handler    http.HandlerFunc
		wantStatus string
		wantReason string
	}{
		{"ok", jsonHandler(200, `{"status":"ok","mongo":"connected"}`), StateOK, ""},
		{"ok sans champ mongo", jsonHandler(200, `{"status":"ok"}`), StateOK, ""},
		{"mongo down malgre 200", jsonHandler(200, `{"status":"ok","mongo":"down"}`), StateDown, "runtime_dependency_down"},
		{"status pas ok", jsonHandler(200, `{"status":"starting"}`), StateDown, "runtime_not_ok"},
		{"json invalide", jsonHandler(200, `<html>`), StateDown, "runtime_health_invalid"},
		{"http 500", jsonHandler(500, `{"status":"ok"}`), StateDown, "runtime_http_500"},
		{"http 404", jsonHandler(404, `{}`), StateDown, "runtime_http_404"},
		{"redirection non suivie", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/ailleurs", http.StatusFound)
		}, StateDown, "runtime_http_302"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := serve(t, tc.handler)
			res := New("223crash", s.URL).Check(context.Background())
			if res.Status != tc.wantStatus || res.Reason != tc.wantReason {
				t.Fatalf("obtenu %q/%q, attendu %q/%q", res.Status, res.Reason, tc.wantStatus, tc.wantReason)
			}
			if res.CheckedAt.IsZero() {
				t.Fatal("CheckedAt non renseigné")
			}
		})
	}
}

func TestCheckChemin(t *testing.T) {
	var got string
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path
		jsonHandler(200, `{"status":"ok"}`)(w, r)
	})
	New("223crash", s.URL).Check(context.Background())
	if got != "/healthz" {
		t.Fatalf("chemin interrogé %q", got)
	}
}

func TestNonConfigure(t *testing.T) {
	res := New("223crash", "").Check(context.Background())
	if res.Status != StateUnavailable || res.Reason != "runtime_url_not_configured" {
		t.Fatalf("obtenu %q/%q", res.Status, res.Reason)
	}
}

func TestURLInvalide(t *testing.T) {
	for _, u := range []string{"ftp://x", "pas une url", "http://"} {
		res := New("223crash", u).Check(context.Background())
		if res.Status != StateUnavailable || res.Reason != "runtime_url_invalid" {
			t.Fatalf("%q: obtenu %q/%q", u, res.Status, res.Reason)
		}
	}
}

func TestInjoignable(t *testing.T) {
	s := httptest.NewServer(http.NotFoundHandler())
	url := s.URL
	s.Close() // runtime arrêté
	res := New("223crash", url).Check(context.Background())
	if res.Status != StateDown || res.Reason != "runtime_unreachable" {
		t.Fatalf("obtenu %q/%q", res.Status, res.Reason)
	}
}

func TestTimeout(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	})
	res := New("223crash", s.URL).WithTimeout(50 * time.Millisecond).Check(context.Background())
	if res.Status != StateDown || res.Reason != "runtime_timeout" {
		t.Fatalf("obtenu %q/%q", res.Status, res.Reason)
	}
}

func TestReasonSansURL(t *testing.T) {
	s := httptest.NewServer(http.NotFoundHandler())
	url := s.URL
	s.Close()
	res := New("223crash", url).Check(context.Background())
	if strings.Contains(res.Reason, "127.0.0.1") {
		t.Fatal("la raison ne doit pas exposer l'URL")
	}
}

func TestEnvName(t *testing.T) {
	if got := EnvName("223crash"); got != "GAME_RUNTIME_URL_223CRASH" {
		t.Fatalf("obtenu %q", got)
	}
}
