package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/tiidiio/223Gaming/internal/adminauth"
	"github.com/tiidiio/223Gaming/internal/adminstore"
	"github.com/tiidiio/223Gaming/internal/productadapter"
	"github.com/tiidiio/223Gaming/internal/ws"
)

type response struct {
	Status  string `json:"status"`
	Service string `json:"service"`
	Version string `json:"version,omitempty"`
}

func NewMux() *http.ServeMux {
	mux, err := NewMuxWithStore(nil)
	if err != nil {
		panic(err)
	}
	return mux
}

// NewMuxWithStore conserve la signature historique. Sans ConsoleDeps, le Ledger est
// "not_configured" : les routes ledger/transactions répondent 503 explicite.
func NewMuxWithStore(store *adminstore.Store) (*http.ServeMux, error) {
	return NewMuxWithDeps(store, ConsoleDeps{})
}

func NewMuxWithDeps(store *adminstore.Store, deps ConsoleDeps) (*http.ServeMux, error) {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", healthHandler)
	mux.HandleFunc("GET /readyz", readyHandler(store))
	mux.HandleFunc("GET /api/v1/provider", providerHandler)
	mux.HandleFunc("GET /api/v1/studio", studioHandler)

	// Intégration réelle du contrat de session produit (v1)
	adapter := productadapter.NewDefaultProductAdapter()
	sessionHandler := newProductSessionHandler(adapter)
	mux.HandleFunc("POST /api/v1/session/handshake", sessionHandler.handleHandshake)

	mux.Handle(
		"GET /provider/",
		http.StripPrefix(
			"/provider/",
			http.FileServer(http.Dir("provider-console")),
		),
	)

	var authService *adminauth.Service

	if store != nil {
		var err error
		authService, err = adminauth.NewService(store)
		if err != nil {
			return nil, fmt.Errorf(
				"initialize administrative authentication: %w",
				err,
			)
		}
	}

	auth := newAdminHTTP(authService)

	mux.HandleFunc("POST /api/admin/auth/login", auth.login)
	mux.HandleFunc("GET /api/admin/auth/me", auth.me)

	mux.Handle(
		"POST /api/admin/auth/logout",
		auth.protect(http.HandlerFunc(auth.logout)),
	)

	var operatorRepo operatorRepository
	if store != nil {
		operatorRepo = store
	}

	var mongoProbe consolePinger
	if store != nil {
		mongoProbe = store
	}

	console := &adminConsole{
		mongo:         mongoProbe,
		ledger:        deps.Ledger,
		ledgerReason:  deps.LedgerUnavailableReason,
		runtimes:      deps.Runtimes,
		operators:     operatorRepo,
		authAvailable: authService != nil,
	}

	mux.Handle(
		"GET /api/admin/health",
		auth.protect(http.HandlerFunc(console.health)),
	)

	mux.Handle(
		"GET /api/admin/overview",
		auth.protect(http.HandlerFunc(console.overview)),
	)

	mux.Handle(
		"GET /api/admin/games",
		auth.protect(http.HandlerFunc(console.games)),
	)

	// Supervision des runtimes de produits (état mesuré, jamais déduit du catalogue).
	mux.Handle(
		"GET /api/admin/runtimes",
		auth.protect(http.HandlerFunc(console.runtimesList)),
	)

	mux.Handle(
		"GET /api/admin/runtimes/{gameId}",
		auth.protect(http.HandlerFunc(console.runtimeGet)),
	)

	mux.Handle(
		"POST /api/admin/runtimes/{gameId}/refresh",
		auth.protect(http.HandlerFunc(console.runtimeRefresh)),
	)

	mux.Handle(
		"GET /api/admin/operators",
		auth.protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			adminOperatorsListHandler(w, r, operatorRepo)
		})),
	)

	mux.Handle(
		"POST /api/admin/operators",
		auth.protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			adminOperatorsCreateHandler(w, r, operatorRepo)
		})),
	)

	// Lecture seule du Provider Ledger persistant (provider_ledger_entries).
	mux.Handle(
		"GET /api/admin/ledger",
		auth.protect(http.HandlerFunc(console.ledgerStats)),
	)

	mux.Handle(
		"GET /api/admin/transactions",
		auth.protect(http.HandlerFunc(console.transactions)),
	)

	mux.Handle(
		"GET /api/admin/transactions/{transactionId}",
		auth.protect(http.HandlerFunc(console.transaction)),
	)

	for _, resource := range []string{
		"recovery",
		"reconciliation",
		"monitoring",
		"reports",
		"admins",
		"audit",
	} {
		mux.Handle(
			"GET /api/admin/"+resource,
			auth.protect(http.HandlerFunc(unavailableHandler)),
		)
	}

	wsHub := ws.NewHub()
	go wsHub.Run()

	mux.HandleFunc("/ws", NewWebSocketHandler(wsHub))
	mux.HandleFunc("/api/v1/ws", NewWebSocketHandler(wsHub))

	return mux, nil
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, response{
		Status:  "ok",
		Service: "223gaming",
	})
}

func readyHandler(store *adminstore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"status": "not_ready",
				"mongo":  "not_configured",
			})
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		if err := store.Ping(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"status": "not_ready",
				"mongo":  "disconnected",
			})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"status": "ready",
			"mongo":  "connected",
		})
	}
}

func providerHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, response{
		Status:  "ok",
		Service: "223GAMING PROVIDER",
		Version: "0.1.0",
	})
}

func studioHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, response{
		Status:  "operational",
		Service: "223GAMING STUDIO",
		Version: "0.1.0",
	})
}

func unavailableHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusNotImplemented, map[string]any{
		"available": false,
		"status":    "not_implemented",
		"message":   "Module administratif non encore intégré.",
	})
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)

	_ = json.NewEncoder(w).Encode(data)
}
