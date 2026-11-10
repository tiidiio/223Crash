package httpapi

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tiidiio/223Gaming/internal/adminstore"
	"github.com/tiidiio/223Gaming/internal/gamecatalog"
	"github.com/tiidiio/223Gaming/provider/ledger"
)

const (
	adminServiceName = "223GAMING PROVIDER ADMIN API"
	adminAPIVersion  = "0.4.0"
	probeTimeout     = 3 * time.Second
)

// LedgerReader est la vue en lecture seule du Provider Ledger dont la console a besoin.
// *ledger.Reader l'implémente. Aucune écriture financière n'est exposée ici.
type LedgerReader interface {
	Ping(ctx context.Context) error
	List(ctx context.Context, q ledger.ListQuery) (*ledger.ListPage, error)
	GetByTransactionID(ctx context.Context, id ledger.TransactionID) (*ledger.Entry, error)
	Stats(ctx context.Context) (*ledger.Stats, error)
}

var _ LedgerReader = (*ledger.Reader)(nil)

type consolePinger interface {
	Ping(ctx context.Context) error
}

// ConsoleDeps regroupe les dépendances optionnelles de la console. Un champ nil signifie
// "composant non configuré" : il est signalé comme tel, jamais simulé.
type ConsoleDeps struct {
	Ledger                  LedgerReader
	LedgerUnavailableReason string
	Runtimes                RuntimeRegistry
}

type adminConsole struct {
	mongo         consolePinger
	ledger        LedgerReader
	ledgerReason  string
	runtimes      RuntimeRegistry
	operators     operatorRepository
	authAvailable bool
}

type componentStatus struct {
	Status    string `json:"status"`
	Required  bool   `json:"required"`
	LatencyMs *int64 `json:"latencyMs,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

type healthReport struct {
	Status      string                     `json:"status"`
	Service     string                     `json:"service"`
	Version     string                     `json:"version"`
	GeneratedAt time.Time                  `json:"generatedAt"`
	Mongo       string                     `json:"mongo"`
	AdminAuth   string                     `json:"admin_auth"`
	Components  map[string]componentStatus `json:"components"`
}

type transactionDTO struct {
	TransactionID             string     `json:"transactionId"`
	IdempotencyKey            string     `json:"idempotencyKey"`
	GameID                    string     `json:"gameId"`
	OperatorID                string     `json:"operatorId"`
	PlayerID                  string     `json:"playerId"`
	ExternalRef               string     `json:"externalRef,omitempty"`
	ParentTransactionID       string     `json:"parentTransactionId,omitempty"`
	Type                      string     `json:"type"`
	AmountMinor               int64      `json:"amountMinor"`
	Status                    string     `json:"status"`
	OperatorBalanceAfterMinor *int64     `json:"operatorBalanceAfterMinor"`
	FailureReason             string     `json:"failureReason,omitempty"`
	CreatedAt                 time.Time  `json:"createdAt"`
	UpdatedAt                 time.Time  `json:"updatedAt"`
	ConfirmedAt               *time.Time `json:"confirmedAt"`
	FailedAt                  *time.Time `json:"failedAt"`
}

type statBucketDTO struct {
	Type            string     `json:"type"`
	Status          string     `json:"status"`
	Count           int64      `json:"count"`
	OldestCreatedAt *time.Time `json:"oldestCreatedAt"`
}

func utcPtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

func toTransactionDTO(e ledger.Entry) transactionDTO {
	return transactionDTO{
		TransactionID:             string(e.TransactionID),
		IdempotencyKey:            string(e.IdempotencyKey),
		GameID:                    string(e.GameID),
		OperatorID:                string(e.OperatorID),
		PlayerID:                  string(e.PlayerID),
		ExternalRef:               string(e.ExternalRef),
		ParentTransactionID:       string(e.ParentTransactionID),
		Type:                      string(e.Type),
		AmountMinor:               e.AmountMinor,
		Status:                    string(e.Status),
		OperatorBalanceAfterMinor: e.OperatorBalanceAfterMinor,
		FailureReason:             e.FailureReason,
		CreatedAt:                 e.CreatedAt.UTC(),
		UpdatedAt:                 e.UpdatedAt.UTC(),
		ConfirmedAt:               utcPtr(e.ConfirmedAt),
		FailedAt:                  utcPtr(e.FailedAt),
	}
}

func (c *adminConsole) probe(
	ctx context.Context,
	required bool,
	p consolePinger,
	notConfiguredDetail string,
) componentStatus {
	if p == nil {
		detail := "not_configured"
		if notConfiguredDetail != "" {
			detail = notConfiguredDetail
		}
		return componentStatus{Status: "not_configured", Required: required, Detail: detail}
	}

	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	start := time.Now()
	err := p.Ping(pctx)
	ms := time.Since(start).Milliseconds()

	if err != nil {
		return componentStatus{
			Status:    "down",
			Required:  required,
			LatencyMs: &ms,
			Detail:    "ping_failed",
		}
	}
	return componentStatus{Status: "ok", Required: required, LatencyMs: &ms}
}

func (c *adminConsole) buildHealth(ctx context.Context) (healthReport, bool) {
	authStatus := componentStatus{
		Status:   "ok",
		Required: true,
		Detail:   "session_validated",
	}
	if !c.authAvailable {
		authStatus = componentStatus{
			Status:   "not_configured",
			Required: true,
			Detail:   "authentication_unavailable",
		}
	}

	runtimes := c.runtimesComponent(ctx)

	components := map[string]componentStatus{
		"http":      {Status: "ok", Required: true},
		"mongo":     c.probe(ctx, true, c.mongo, ""),
		"adminAuth": authStatus,
		"ledger":    c.probe(ctx, true, c.ledger, c.ledgerReason),
		"runtimes":  runtimes,
		// Alias de compatibilité avec le frontend actuel ; retiré au lot 2.
		"crashRuntime": runtimes,
	}

	operational := true
	for _, comp := range components {
		if comp.Required && comp.Status != "ok" {
			operational = false
		}
	}

	status := "degraded"
	if operational {
		status = "operational"
	}

	legacyMongo := "not_configured"
	switch components["mongo"].Status {
	case "ok":
		legacyMongo = "connected"
	case "down":
		legacyMongo = "disconnected"
	}

	legacyAuth := "installed"
	if !c.authAvailable {
		legacyAuth = "unavailable"
	}

	return healthReport{
		Status:      status,
		Service:     adminServiceName,
		Version:     adminAPIVersion,
		GeneratedAt: time.Now().UTC(),
		Mongo:       legacyMongo,
		AdminAuth:   legacyAuth,
		Components:  components,
	}, operational
}

func (c *adminConsole) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	report, operational := c.buildHealth(ctx)

	code := http.StatusOK
	if !operational {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, report)
}

func (c *adminConsole) ledgerSummary(ctx context.Context) map[string]any {
	if c.ledger == nil {
		detail := "not_configured"
		if c.ledgerReason != "" {
			detail = c.ledgerReason
		}
		return map[string]any{"status": "not_configured", "detail": detail}
	}

	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	stats, err := c.ledger.Stats(sctx)
	if err != nil {
		log.Printf("admin ledger stats failed: %v", err)
		return map[string]any{"status": "unavailable", "error": "ledger_read_failed"}
	}

	buckets := make([]statBucketDTO, 0, len(stats.Buckets))
	for _, b := range stats.Buckets {
		buckets = append(buckets, statBucketDTO{
			Type:            string(b.Type),
			Status:          string(b.Status),
			Count:           b.Count,
			OldestCreatedAt: utcPtr(b.OldestCreatedAt),
		})
	}

	return map[string]any{
		"status":           "ok",
		"generatedAt":      stats.GeneratedAt,
		"totalEntries":     stats.TotalEntries,
		"buckets":          buckets,
		"amountsAvailable": false,
		"amountsNote":      "Les entrées du Ledger ne portent pas de devise : aucun montant n'est agrégé entre opérateurs.",
	}
}

func (c *adminConsole) operatorsSummary(ctx context.Context) map[string]any {
	if c.operators == nil {
		return map[string]any{"status": "not_configured"}
	}

	octx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	limit := adminstore.DefaultOperatorListLimit

	operators, err := c.operators.ListOperators(octx, limit)
	if err != nil {
		log.Printf("admin operators summary failed: %v", err)
		return map[string]any{"status": "unavailable", "error": "operator_list_failed"}
	}

	return map[string]any{
		"status":    "ok",
		"count":     len(operators),
		"truncated": int64(len(operators)) >= limit,
	}
}

func (c *adminConsole) overview(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	report, _ := c.buildHealth(ctx)

	writeJSON(w, http.StatusOK, map[string]any{
		"generatedAt": time.Now().UTC(),
		"health":      report,
		"operators":   c.operatorsSummary(ctx),
		"ledger":      c.ledgerSummary(ctx),
		"games": map[string]any{
			"registered": gamecatalog.List(),
			"runtime":    c.legacyRuntime(ctx),
			"runtimes":   c.runtimeSnapshots(ctx),
		},
		"unavailableModules": []string{
			"recovery",
			"reconciliation",
			"monitoring",
			"reports",
			"admins",
			"audit",
		},
	})
}

func (c *adminConsole) ledgerStats(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	summary := c.ledgerSummary(ctx)

	code := http.StatusOK
	if summary["status"] != "ok" {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, summary)
}

func writeConsoleError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{
		"error":   code,
		"message": message,
	})
}

func (c *adminConsole) requireLedger(w http.ResponseWriter) bool {
	if c.ledger == nil {
		writeConsoleError(
			w,
			http.StatusServiceUnavailable,
			"ledger_unavailable",
			"Le Provider Ledger n'est pas disponible dans ce processus.",
		)
		return false
	}
	return true
}

func writeLedgerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ledger.ErrInvalidCursor):
		writeConsoleError(w, http.StatusBadRequest, "invalid_cursor", "Le curseur de pagination est invalide.")
	case errors.Is(err, ledger.ErrInvalidFilter):
		writeConsoleError(w, http.StatusBadRequest, "invalid_filter", err.Error())
	case errors.Is(err, ledger.ErrEntryNotFound):
		writeConsoleError(w, http.StatusNotFound, "transaction_not_found", "Transaction introuvable.")
	case errors.Is(err, ledger.ErrReaderUnavailable):
		writeConsoleError(w, http.StatusServiceUnavailable, "ledger_unavailable", "Le Provider Ledger n'est pas disponible.")
	default:
		log.Printf("admin ledger read failed: %v", err)
		writeConsoleError(w, http.StatusInternalServerError, "ledger_read_failed", "Impossible de lire le Provider Ledger.")
	}
}

func (c *adminConsole) transactions(w http.ResponseWriter, r *http.Request) {
	if !c.requireLedger(w) {
		return
	}

	params := r.URL.Query()

	query := ledger.ListQuery{
		OperatorID: ledger.OperatorID(strings.TrimSpace(params.Get("operatorId"))),
		PlayerID:   ledger.PlayerID(strings.TrimSpace(params.Get("playerId"))),
		GameID:     ledger.GameID(strings.TrimSpace(params.Get("gameId"))),
		Type:       ledger.EntryType(strings.ToUpper(strings.TrimSpace(params.Get("type")))),
		Status:     ledger.EntryStatus(strings.ToUpper(strings.TrimSpace(params.Get("status")))),
		Cursor:     strings.TrimSpace(params.Get("cursor")),
	}

	if raw := strings.TrimSpace(params.Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 || n > ledger.MaxListLimit {
			writeConsoleError(
				w,
				http.StatusBadRequest,
				"invalid_limit",
				"La limite doit être comprise entre 1 et "+strconv.Itoa(ledger.MaxListLimit)+".",
			)
			return
		}
		query.Limit = n
	}

	if raw := strings.TrimSpace(params.Get("from")); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeConsoleError(w, http.StatusBadRequest, "invalid_filter", "from : date RFC3339 attendue.")
			return
		}
		query.From = t.UTC()
	}

	if raw := strings.TrimSpace(params.Get("to")); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeConsoleError(w, http.StatusBadRequest, "invalid_filter", "to : date RFC3339 attendue.")
			return
		}
		query.To = t.UTC()
	}

	ctx, cancel := requestContext(r)
	defer cancel()

	page, err := c.ledger.List(ctx, query)
	if err != nil {
		writeLedgerError(w, err)
		return
	}

	items := make([]transactionDTO, 0, len(page.Entries))
	for _, entry := range page.Entries {
		items = append(items, toTransactionDTO(entry))
	}

	var next any
	if page.NextCursor != "" {
		next = page.NextCursor
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"object":     "list",
		"items":      items,
		"count":      len(items),
		"limit":      page.Limit,
		"hasMore":    page.HasMore,
		"nextCursor": next,
	})
}

func (c *adminConsole) transaction(w http.ResponseWriter, r *http.Request) {
	if !c.requireLedger(w) {
		return
	}

	id := strings.TrimSpace(r.PathValue("transactionId"))

	ctx, cancel := requestContext(r)
	defer cancel()

	entry, err := c.ledger.GetByTransactionID(ctx, ledger.TransactionID(id))
	if err != nil {
		writeLedgerError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"transaction": toTransactionDTO(*entry),
	})
}
