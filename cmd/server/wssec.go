package main

import (
	"errors"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
)

const (
	wsWriteWait      = 10 * time.Second
	wsPongWait       = 60 * time.Second
	wsPingInterval   = 25 * time.Second
	wsMaxMessage     = 4096
	wsMsgRate        = 10.0
	wsMsgBurst       = 20.0
	wsMaxViolations  = 100
	wsMinAutoCashout = 1.01
	wsMaxAutoCashout = 1000000.0
	wsMaxTokenLen    = 2048
	wsClockLeeway    = 5 * time.Second
)

// Les identifiants entrent dans des clés d'idempotence "a:b:c" : ':' est interdit.
var wsIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

type wsSecurityCfg struct {
	allowedOperators map[string]struct{}
	allowedOrigins   map[string]struct{}
	maxTTL           time.Duration
	maxConns         int64
	maxBetUnits      float64
	conns            atomic.Int64
}

var (
	wsSecOnce sync.Once
	wsSecCfg  *wsSecurityCfg
)

func wsSecurity() *wsSecurityCfg {
	wsSecOnce.Do(func() { wsSecCfg = loadWSSecurity() })
	return wsSecCfg
}

func wsSplitCSV(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func loadWSSecurity() *wsSecurityCfg {
	c := &wsSecurityCfg{
		allowedOperators: map[string]struct{}{},
		allowedOrigins:   map[string]struct{}{},
		maxTTL:           24 * time.Hour,
		maxConns:         5000,
		maxBetUnits:      10_000_000,
	}
	ops := wsSplitCSV(os.Getenv("ALLOWED_OPERATORS"))
	if len(ops) == 0 {
		ops = wsSplitCSV(os.Getenv("OPERATOR_ID"))
	}
	if len(ops) == 0 {
		log.Fatal("sécurité WS: ALLOWED_OPERATORS ou OPERATOR_ID requis (fail-closed)")
	}
	for _, o := range ops {
		if !wsIDPattern.MatchString(o) {
			log.Fatalf("sécurité WS: identifiant opérateur invalide dans la configuration: %q", o)
		}
		c.allowedOperators[o] = struct{}{}
	}
	for _, o := range wsSplitCSV(os.Getenv("ALLOWED_ORIGINS")) {
		c.allowedOrigins[strings.ToLower(strings.TrimRight(o, "/"))] = struct{}{}
	}
	if len(c.allowedOrigins) == 0 {
		log.Println("sécurité WS: ALLOWED_ORIGINS vide — toutes les origines navigateur sont acceptées")
	}
	if v := os.Getenv("JWT_MAX_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			log.Fatalf("sécurité WS: JWT_MAX_TTL invalide")
		}
		c.maxTTL = d
	}
	if v := os.Getenv("WS_MAX_CONNS"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			log.Fatalf("sécurité WS: WS_MAX_CONNS invalide")
		}
		c.maxConns = n
	}
	if v := os.Getenv("MAX_BET_UNITS"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f <= 0 {
			log.Fatalf("sécurité WS: MAX_BET_UNITS invalide")
		}
		c.maxBetUnits = f
	}
	log.Printf("sécurité WS: %d opérateur(s), %d origine(s), ttl max %s, conns max %d, mise max %.0f",
		len(c.allowedOperators), len(c.allowedOrigins), c.maxTTL, c.maxConns, c.maxBetUnits)
	return c
}

// wsCheckSecretStrength : avertit si JWT_SECRET < 32 octets ; fatal si APP_ENV=production.
func wsCheckSecretStrength(secret string) {
	if len(secret) >= 32 {
		return
	}
	if os.Getenv("APP_ENV") == "production" {
		log.Fatal("JWT_SECRET trop court (< 32 octets) en production")
	}
	log.Println("ATTENTION: JWT_SECRET < 32 octets (refusé si APP_ENV=production)")
}

func (s *wsSecurityCfg) validateToken(tokenStr string, secret []byte) (*wsClaims, error) {
	if tokenStr == "" || len(tokenStr) > wsMaxTokenLen {
		return nil, errors.New("token absent ou hors limites")
	}
	claims := &wsClaims{}
	tok, err := jwt.ParseWithClaims(tokenStr, claims,
		func(*jwt.Token) (interface{}, error) { return secret, nil },
		jwt.WithValidMethods([]string{"HS512"}),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(wsClockLeeway),
	)
	if err != nil || !tok.Valid {
		return nil, errors.New("token invalide")
	}
	if !wsIDPattern.MatchString(claims.Subject) {
		return nil, errors.New("sub invalide")
	}
	if _, ok := s.allowedOperators[claims.OperatorID]; !ok {
		return nil, errors.New("operatorId non autorisé")
	}
	exp, err := claims.GetExpirationTime()
	if err != nil || exp == nil || time.Until(exp.Time) > s.maxTTL+wsClockLeeway {
		return nil, errors.New("durée de validité excessive")
	}
	return claims, nil
}

func (s *wsSecurityCfg) acquire() bool {
	if s.conns.Add(1) > s.maxConns {
		s.conns.Add(-1)
		return false
	}
	return true
}

func (s *wsSecurityCfg) release() { s.conns.Add(-1) }

func wsCheckOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true // clients non navigateur
	}
	if u, err := url.Parse(o); err == nil && u.Host != "" && strings.EqualFold(u.Host, r.Host) {
		return true
	}
	s := wsSecurity()
	if len(s.allowedOrigins) == 0 {
		return true
	}
	_, ok := s.allowedOrigins[strings.ToLower(strings.TrimRight(o, "/"))]
	return ok
}

func wsConfigureConn(conn *websocket.Conn) {
	conn.SetReadLimit(wsMaxMessage)
	_ = conn.SetReadDeadline(time.Now().Add(wsPongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(wsPongWait))
	})
}

// wsRateLimiter : token bucket, utilisé par la seule goroutine readPump du client.
type wsRateLimiter struct {
	rate, burst, tokens float64
	last                time.Time
	viol                int
}

func newWSRateLimiter(rate, burst float64) *wsRateLimiter {
	return &wsRateLimiter{rate: rate, burst: burst, tokens: burst, last: time.Now()}
}

func (l *wsRateLimiter) allow() bool {
	now := time.Now()
	l.tokens += now.Sub(l.last).Seconds() * l.rate
	if l.tokens > l.burst {
		l.tokens = l.burst
	}
	l.last = now
	if l.tokens >= 1 {
		l.tokens--
		l.viol = 0
		return true
	}
	l.viol++
	return false
}
