package main

import (
	"context"
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
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
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

type operatorAllowlist struct {
	codes map[string]struct{}
}

type wsSecurityCfg struct {
	allowedOperators atomic.Pointer[operatorAllowlist]
	allowedOrigins   map[string]struct{}
	maxTTL           time.Duration
	maxConns         int64
	maxBetUnits      float64
	conns            atomic.Int64
}

var (
	wsSecOnce  sync.Once
	wsSecCfg   *wsSecurityCfg
	operatorDB *mongo.Database
)

// setOperatorDatabase injecte la base avant toute initialisation WS.
func setOperatorDatabase(db *mongo.Database) {
	operatorDB = db
}

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

func loadActiveOperatorCodes() ([]string, error) {
	if operatorDB == nil {
		return nil, errors.New("base MongoDB des opérateurs non initialisée")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cursor, err := operatorDB.Collection("operators").Find(
		ctx,
		bson.D{{Key: "status", Value: "active"}},
		options.Find().SetProjection(bson.D{{Key: "code", Value: 1}}),
	)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var rows []struct {
		Code string `bson:"code"`
	}
	if err := cursor.All(ctx, &rows); err != nil {
		return nil, err
	}

	codes := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.Code != "" {
			codes = append(codes, row.Code)
		}
	}
	return codes, nil
}

// refreshAllowedOperators publie un snapshot atomique des seuls opérateurs actifs.
// Toute erreur de lecture ou donnée invalide entraîne immédiatement un refus global.
func refreshAllowedOperators(s *wsSecurityCfg) error {
	codes, err := loadActiveOperatorCodes()
	if err != nil {
		s.allowedOperators.Store(&operatorAllowlist{
			codes: map[string]struct{}{},
		})
		return err
	}

	next := make(map[string]struct{}, len(codes))
	for _, code := range codes {
		if !wsIDPattern.MatchString(code) {
			s.allowedOperators.Store(&operatorAllowlist{
				codes: map[string]struct{}{},
			})
			return errors.New("code opérateur MongoDB invalide")
		}
		next[code] = struct{}{}
	}

	// Un résultat MongoDB valide mais vide reste une liste vide : deny all.
	s.allowedOperators.Store(&operatorAllowlist{codes: next})
	log.Printf("sécurité WS: autorisations opérateurs actualisées (%d actif(s))", len(next))
	return nil
}

// runOperatorRefresh actualise périodiquement les autorisations.
// Une panne MongoDB vide immédiatement le snapshot dans refreshAllowedOperators.
func runOperatorRefresh(ctx context.Context, s *wsSecurityCfg, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := refreshAllowedOperators(s); err != nil {
				log.Printf("sécurité WS: actualisation MongoDB impossible, tous les opérateurs refusés: %v", err)
			}
		}
	}
}

func loadWSSecurity() *wsSecurityCfg {
	c := &wsSecurityCfg{
		allowedOrigins: map[string]struct{}{},
		maxTTL:         24 * time.Hour,
		maxConns:       5000,
		maxBetUnits:    10_000_000,
	}

	// État initial : aucun opérateur autorisé avant confirmation MongoDB.
	c.allowedOperators.Store(&operatorAllowlist{
		codes: map[string]struct{}{},
	})

	for _, o := range wsSplitCSV(os.Getenv("ALLOWED_ORIGINS")) {
		c.allowedOrigins[strings.ToLower(strings.TrimRight(o, "/"))] = struct{}{}
	}
	if len(c.allowedOrigins) == 0 {
		log.Println("sécurité WS: ALLOWED_ORIGINS vide — toutes les origines navigateur sont acceptées")
	}

	if v := os.Getenv("JWT_MAX_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			log.Fatal("sécurité WS: JWT_MAX_TTL invalide")
		}
		c.maxTTL = d
	}

	if v := os.Getenv("WS_MAX_CONNS"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			log.Fatal("sécurité WS: WS_MAX_CONNS invalide")
		}
		c.maxConns = n
	}

	if v := os.Getenv("MAX_BET_UNITS"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f <= 0 {
			log.Fatal("sécurité WS: MAX_BET_UNITS invalide")
		}
		c.maxBetUnits = f
	}

	log.Printf("sécurité WS: origines=%d, ttl max=%s, conns max=%d, mise max=%.0f",
		len(c.allowedOrigins), c.maxTTL, c.maxConns, c.maxBetUnits)

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
	snapshot := s.allowedOperators.Load()
	if snapshot == nil {
		return nil, errors.New("autorisations opérateur indisponibles")
	}
	if _, ok := snapshot.codes[claims.OperatorID]; !ok {
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
