package main

import (
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var wsTestSecret = []byte("0123456789abcdef0123456789abcdef-test-only")

func wsTestCfg() *wsSecurityCfg {
	return &wsSecurityCfg{
		allowedOperators: map[string]struct{}{"opA": {}},
		allowedOrigins:   map[string]struct{}{},
		maxTTL:           2 * time.Hour,
		maxConns:         2,
		maxBetUnits:      1000,
	}
}

func wsSign(t *testing.T, m jwt.SigningMethod, secret []byte, claims jwt.MapClaims) string {
	t.Helper()
	s, err := jwt.NewWithClaims(m, claims).SignedString(secret)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestWSValidateToken(t *testing.T) {
	cfg := wsTestCfg()
	exp := func(d time.Duration) int64 { return time.Now().Add(d).Unix() }
	cases := []struct {
		name   string
		method jwt.SigningMethod
		secret []byte
		claims jwt.MapClaims
		ok     bool
	}{
		{"valide", jwt.SigningMethodHS512, wsTestSecret, jwt.MapClaims{"sub": "p1", "operatorId": "opA", "exp": exp(time.Hour)}, true},
		{"sans exp", jwt.SigningMethodHS512, wsTestSecret, jwt.MapClaims{"sub": "p1", "operatorId": "opA"}, false},
		{"expiré", jwt.SigningMethodHS512, wsTestSecret, jwt.MapClaims{"sub": "p1", "operatorId": "opA", "exp": exp(-time.Hour)}, false},
		{"HS256 refusé", jwt.SigningMethodHS256, wsTestSecret, jwt.MapClaims{"sub": "p1", "operatorId": "opA", "exp": exp(time.Hour)}, false},
		{"mauvais secret", jwt.SigningMethodHS512, []byte("autre-secret-autre-secret-autre-secret"), jwt.MapClaims{"sub": "p1", "operatorId": "opA", "exp": exp(time.Hour)}, false},
		{"opérateur inconnu", jwt.SigningMethodHS512, wsTestSecret, jwt.MapClaims{"sub": "p1", "operatorId": "opZ", "exp": exp(time.Hour)}, false},
		{"opérateur vide", jwt.SigningMethodHS512, wsTestSecret, jwt.MapClaims{"sub": "p1", "operatorId": "", "exp": exp(time.Hour)}, false},
		{"sub avec ':'", jwt.SigningMethodHS512, wsTestSecret, jwt.MapClaims{"sub": "p1:x", "operatorId": "opA", "exp": exp(time.Hour)}, false},
		{"sub vide", jwt.SigningMethodHS512, wsTestSecret, jwt.MapClaims{"sub": "", "operatorId": "opA", "exp": exp(time.Hour)}, false},
		{"TTL excessif", jwt.SigningMethodHS512, wsTestSecret, jwt.MapClaims{"sub": "p1", "operatorId": "opA", "exp": exp(48 * time.Hour)}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := cfg.validateToken(wsSign(t, tc.method, tc.secret, tc.claims), wsTestSecret)
			if (err == nil) != tc.ok {
				t.Fatalf("attendu ok=%v, err=%v", tc.ok, err)
			}
		})
	}
	if _, err := cfg.validateToken(strings.Repeat("a", wsMaxTokenLen+1), wsTestSecret); err == nil {
		t.Fatal("token trop long accepté")
	}
	if _, err := cfg.validateToken("", wsTestSecret); err == nil {
		t.Fatal("token vide accepté")
	}
}

func TestWSConnLimit(t *testing.T) {
	cfg := wsTestCfg()
	if !cfg.acquire() || !cfg.acquire() {
		t.Fatal("2 premières connexions refusées")
	}
	if cfg.acquire() {
		t.Fatal("3e connexion acceptée au-delà du plafond")
	}
	cfg.release()
	if !cfg.acquire() {
		t.Fatal("slot libéré non réutilisable")
	}
}

func TestWSRateLimiter(t *testing.T) {
	l := newWSRateLimiter(0, 3)
	for i := 0; i < 3; i++ {
		if !l.allow() {
			t.Fatalf("message %d refusé dans le burst", i)
		}
	}
	if l.allow() || l.viol != 1 {
		t.Fatal("dépassement non détecté")
	}
	l2 := newWSRateLimiter(1000, 1)
	l2.allow()
	time.Sleep(5 * time.Millisecond)
	if !l2.allow() {
		t.Fatal("refill non effectué")
	}
}

func TestWSTrySendNeverPanicsAfterClose(t *testing.T) {
	for i := 0; i < 200; i++ {
		c := &client{send: make(chan []byte, 1), done: make(chan struct{})}
		var wg sync.WaitGroup
		for g := 0; g < 4; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for k := 0; k < 50; k++ {
					c.trySend([]byte("x"))
				}
			}()
		}
		c.closeOnce.Do(func() { close(c.done) })
		c.closeOnce.Do(func() { close(c.done) })
		wg.Wait()
	}
}

func TestWSCheckOrigin(t *testing.T) {
	cfg := wsTestCfg()
	cfg.allowedOrigins["https://op.example"] = struct{}{}
	wsSecOnce.Do(func() {})
	wsSecCfg = cfg

	mk := func(origin string) bool {
		r := httptest.NewRequest("GET", "/", nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		return wsCheckOrigin(r)
	}
	if !mk("") {
		t.Error("client sans Origin refusé")
	}
	if !mk("https://op.example") {
		t.Error("origine autorisée refusée")
	}
	if !mk("https://op.example/") {
		t.Error("origine autorisée (slash final) refusée")
	}
	if mk("https://evil.example") {
		t.Error("origine non autorisée acceptée")
	}
	if !mk("http://example.com") { // même host que httptest.NewRequest
		t.Error("même host refusé")
	}
	wsSecCfg = wsTestCfg() // liste vide : mode permissif
	if !mk("https://evil.example") {
		t.Error("allowlist vide doit rester permissive")
	}
}
