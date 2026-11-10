// Package runtimeprobe vérifie réellement la disponibilité du runtime d'un
// produit de jeu via son endpoint de santé HTTP.
//
// Le package est générique : il ne connaît que un identifiant de produit opaque
// et une URL. Il n'importe aucun package de jeu.
package runtimeprobe

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	StateOK          = "ok"
	StateDown        = "down"
	StateUnavailable = "unavailable"
)

const (
	DefaultTimeout    = 2 * time.Second
	DefaultHealthPath = "/healthz"
	maxBodyBytes      = 4096
)

// EnvName retourne le nom de la variable d'environnement portant l'URL du
// runtime d'un produit, par exemple GAME_RUNTIME_URL_223CRASH.
func EnvName(productID string) string {
	return "GAME_RUNTIME_URL_" + strings.ToUpper(strings.TrimSpace(productID))
}

// Result est le résultat d'un contrôle. Reason ne contient jamais l'URL.
type Result struct {
	Status    string    `json:"status"`
	Reason    string    `json:"reason,omitempty"`
	CheckedAt time.Time `json:"checkedAt"`
	LatencyMs int64     `json:"latencyMs"`
}

// Probe contrôle le runtime d'un produit.
type Probe struct {
	productID  string
	rawURL     string
	healthPath string
	timeout    time.Duration
	client     *http.Client
}

// New construit une sonde. rawURL vide = runtime non configuré.
func New(productID, rawURL string) *Probe {
	return &Probe{
		productID:  productID,
		rawURL:     strings.TrimSpace(rawURL),
		healthPath: DefaultHealthPath,
		timeout:    DefaultTimeout,
		client: &http.Client{
			// Une redirection n'est jamais suivie : elle compte comme échec.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// WithTimeout fixe le délai maximal d'un contrôle.
func (p *Probe) WithTimeout(d time.Duration) *Probe {
	if d > 0 {
		p.timeout = d
	}
	return p
}

// ProductID retourne l'identifiant opaque du produit.
func (p *Probe) ProductID() string { return p.productID }

// Check effectue un contrôle réel. Seul un runtime qui répond HTTP 200 avec un
// corps JSON {"status":"ok"} (et, si présent, "mongo":"connected") est "ok".
func (p *Probe) Check(ctx context.Context) Result {
	start := time.Now()
	status, reason := p.check(ctx)
	return Result{
		Status:    status,
		Reason:    reason,
		CheckedAt: start.UTC(),
		LatencyMs: time.Since(start).Milliseconds(),
	}
}

func (p *Probe) check(ctx context.Context) (string, string) {
	if p == nil || p.rawURL == "" {
		return StateUnavailable, "runtime_url_not_configured"
	}

	base, err := url.Parse(p.rawURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return StateUnavailable, "runtime_url_invalid"
	}
	target := base.ResolveReference(&url.URL{Path: p.healthPath}).String()

	cctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(cctx, http.MethodGet, target, nil)
	if err != nil {
		return StateUnavailable, "runtime_url_invalid"
	}
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		if errors.Is(cctx.Err(), context.DeadlineExceeded) {
			return StateDown, "runtime_timeout"
		}
		return StateDown, "runtime_unreachable"
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return StateDown, "runtime_http_" + itoa(resp.StatusCode)
	}

	var body struct {
		Status string `json:"status"`
		Mongo  string `json:"mongo"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes)).Decode(&body); err != nil {
		return StateDown, "runtime_health_invalid"
	}
	if body.Status != "ok" {
		return StateDown, "runtime_not_ok"
	}
	// Le /healthz de 223CRASH répond 200 même si Mongo est tombé : le champ
	// "mongo" doit donc être contrôlé. Absent = non applicable.
	if body.Mongo != "" && body.Mongo != "connected" {
		return StateDown, "runtime_dependency_down"
	}
	return StateOK, ""
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 && i > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
