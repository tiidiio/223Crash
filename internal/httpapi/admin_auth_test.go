package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAdminProtectedRoutesUnavailableWithoutStore(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
	}{
		{"admin health", http.MethodGet, "/api/admin/health"},
		{"admin overview", http.MethodGet, "/api/admin/overview"},
		{"admin operators", http.MethodGet, "/api/admin/operators"},
		{"admin audit", http.MethodGet, "/api/admin/audit"},
		{"admin logout", http.MethodPost, "/api/admin/auth/logout"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			rec := httptest.NewRecorder()

			NewMux().ServeHTTP(rec, req)

			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status: got %d, want %d",
					rec.Code, http.StatusServiceUnavailable)
			}
		})
	}
}

func TestAdminMeWithoutSession(t *testing.T) {
	req := httptest.NewRequest(
		http.MethodGet,
		"/api/admin/auth/me",
		nil,
	)
	rec := httptest.NewRecorder()

	NewMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want %d",
			rec.Code, http.StatusUnauthorized)
	}

	cookies := rec.Result().Cookies()
	found := false

	for _, cookie := range cookies {
		if cookie.Name == adminCookieName {
			found = true

			if cookie.MaxAge >= 0 {
				t.Errorf("expected expired session cookie, got MaxAge=%d",
					cookie.MaxAge)
			}

			if !cookie.HttpOnly {
				t.Error("expected HttpOnly session cookie")
			}
		}
	}

	if !found {
		t.Error("expected session cookie to be cleared")
	}
}

func TestAdminLoginUnavailableWithoutStore(t *testing.T) {
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/admin/auth/login",
		nil,
	)
	req.Header.Set("Origin", "http://example.test")

	rec := httptest.NewRecorder()

	NewMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d, want %d",
			rec.Code, http.StatusServiceUnavailable)
	}
}

func TestValidOrigin(t *testing.T) {
	tests := []struct {
		name   string
		host   string
		origin string
		valid  bool
	}{
		{
			name:   "same HTTP origin",
			host:   "localhost:8081",
			origin: "http://localhost:8081",
			valid:  true,
		},
		{
			name:   "same HTTPS origin",
			host:   "provider.example.com",
			origin: "https://provider.example.com",
			valid:  true,
		},
		{
			name:   "different host",
			host:   "localhost:8081",
			origin: "http://evil.example",
			valid:  false,
		},
		{
			name:   "missing origin",
			host:   "localhost:8081",
			origin: "",
			valid:  false,
		},
		{
			name:   "origin with path",
			host:   "localhost:8081",
			origin: "http://localhost:8081/admin",
			valid:  false,
		},
		{
			name:   "origin with trailing slash",
			host:   "localhost:8081",
			origin: "http://localhost:8081/",
			valid:  false,
		},
		{
			name:   "origin with query",
			host:   "localhost:8081",
			origin: "http://localhost:8081?x=1",
			valid:  false,
		},
		{
			name:   "origin with fragment",
			host:   "localhost:8081",
			origin: "http://localhost:8081#admin",
			valid:  false,
		},
		{
			name:   "origin with credentials",
			host:   "localhost:8081",
			origin: "http://user@localhost:8081",
			valid:  false,
		},
		{
			name:   "unsupported scheme",
			host:   "localhost:8081",
			origin: "ftp://localhost:8081",
			valid:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(
				http.MethodPost,
				"/api/admin/auth/logout",
				nil,
			)
			req.Host = tt.host

			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}

			if got := validOrigin(req); got != tt.valid {
				t.Fatalf("validOrigin() = %v, want %v",
					got, tt.valid)
			}
		})
	}
}

func TestLoginLimiter(t *testing.T) {
	limiter := &loginLimiter{entries: make(map[string]loginAttempt)}
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	ip := "192.0.2.10"

	// Les requêtes autorisées ne consomment pas de tentative.
	for i := 0; i < 10; i++ {
		if !limiter.allow(ip, now) {
			t.Fatalf("request %d should be allowed before authentication failure", i+1)
		}
	}

	// Cinq authentifications invalides sont autorisées.
	for i := 0; i < 5; i++ {
		if !limiter.allow(ip, now) {
			t.Fatalf("attempt %d should be allowed", i+1)
		}
		limiter.recordFailure(ip, now)
	}

	if limiter.allow(ip, now) {
		t.Fatal("request after five failed authentications should be rejected")
	}

	if !limiter.allow(ip, now.Add(15*time.Minute)) {
		t.Fatal("request should be allowed after the 15-minute window")
	}
}
func TestLoginLimiterSeparatesIPs(t *testing.T) {
	limiter := &loginLimiter{entries: make(map[string]loginAttempt)}
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	ip1 := "192.0.2.10"
	ip2 := "192.0.2.11"

	for i := 0; i < 5; i++ {
		if !limiter.allow(ip1, now) {
			t.Fatalf("IP 1 attempt %d should be allowed", i+1)
		}
		limiter.recordFailure(ip1, now)
	}

	if limiter.allow(ip1, now) {
		t.Fatal("IP 1 should be blocked after five failed authentications")
	}

	if !limiter.allow(ip2, now) {
		t.Fatal("IP 2 should have an independent limit")
	}
}
