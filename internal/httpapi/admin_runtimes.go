package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tiidiio/223Crash/internal/gamecatalog"
	"github.com/tiidiio/223Crash/internal/runtimeregistry"
)

// RuntimeRegistry est la vue du registre de runtimes dont la console a besoin.
// *runtimeregistry.Registry l'implémente.
type RuntimeRegistry interface {
	IDs() []string
	Get(ctx context.Context, id string) (runtimeregistry.Snapshot, error)
	List(ctx context.Context) []runtimeregistry.Snapshot
	Refresh(ctx context.Context, id string) (runtimeregistry.Snapshot, bool, error)
}

var _ RuntimeRegistry = (*runtimeregistry.Registry)(nil)

type runtimeItemDTO struct {
	GameID            string                   `json:"game_id"`
	Name              string                   `json:"name"`
	CatalogStatus     string                   `json:"catalog_status"`
	IntegrationStatus string                   `json:"integration_status"`
	Runtime           runtimeregistry.Snapshot `json:"runtime"`
}

func catalogIndex() map[string]gamecatalog.Game {
	idx := make(map[string]gamecatalog.Game)
	for _, g := range gamecatalog.List() {
		idx[g.ID] = g
	}
	return idx
}

func toRuntimeItem(idx map[string]gamecatalog.Game, s runtimeregistry.Snapshot) runtimeItemDTO {
	item := runtimeItemDTO{GameID: s.GameID, Name: s.GameID, Runtime: s}
	if g, ok := idx[s.GameID]; ok {
		item.Name = g.Name
		item.CatalogStatus = g.Status
		item.IntegrationStatus = g.IntegrationStatus
	}
	return item
}

// runtimeSnapshots ne renvoie jamais nil : [] si aucun registre n'est configuré.
func (c *adminConsole) runtimeSnapshots(ctx context.Context) []runtimeregistry.Snapshot {
	if c.runtimes == nil {
		return []runtimeregistry.Snapshot{}
	}
	return c.runtimes.List(ctx)
}

// legacyRuntime conserve le format historique "runtime" (objet unique) attendu par le
// frontend actuel. Aucun GameID n'est codé en dur : le premier produit suivi est utilisé.
func (c *adminConsole) legacyRuntime(ctx context.Context) map[string]any {
	if c.runtimes == nil {
		return map[string]any{"status": "unavailable", "reason": "no_runtime_registry_configured"}
	}
	ids := c.runtimes.IDs()
	if len(ids) == 0 {
		return map[string]any{"status": "unavailable", "reason": "no_product_registered"}
	}
	snap, err := c.runtimes.Get(ctx, ids[0])
	if err != nil {
		return map[string]any{"gameId": ids[0], "status": "unavailable", "reason": "runtime_check_failed"}
	}
	out := map[string]any{
		"gameId": snap.GameID,
		"status": strings.ToLower(string(snap.RuntimeStatus)),
	}
	if snap.FailureReason != "" {
		out["reason"] = snap.FailureReason
	}
	return out
}

// runtimesComponent agrège tous les runtimes. Non requis : un runtime indisponible
// n'empêche pas le Provider Core d'être opérationnel.
func (c *adminConsole) runtimesComponent(ctx context.Context) componentStatus {
	if c.runtimes == nil {
		return componentStatus{Status: "not_configured", Required: false, Detail: "no_runtime_registry_configured"}
	}
	snaps := c.runtimes.List(ctx)
	if len(snaps) == 0 {
		return componentStatus{Status: "unavailable", Required: false, Detail: "no_product_registered"}
	}

	available, bad := 0, 0
	var maxLatency int64
	for _, s := range snaps {
		switch s.RuntimeStatus {
		case runtimeregistry.StateAvailable:
			available++
		case runtimeregistry.StateUnavailable, runtimeregistry.StateDegraded:
			bad++
		}
		if s.LatencyMs > maxLatency {
			maxLatency = s.LatencyMs
		}
	}

	status := "unavailable"
	switch {
	case bad > 0:
		status = "down"
	case available == len(snaps):
		status = "ok"
	}
	return componentStatus{
		Status:    status,
		Required:  false,
		LatencyMs: &maxLatency,
		Detail:    fmt.Sprintf("available=%d/%d", available, len(snaps)),
	}
}

func (c *adminConsole) requireRuntimes(w http.ResponseWriter) bool {
	if c.runtimes == nil {
		writeConsoleError(
			w,
			http.StatusServiceUnavailable,
			"runtimes_unavailable",
			"Le registre de runtimes n'est pas configuré dans ce processus.",
		)
		return false
	}
	return true
}

func (c *adminConsole) runtimesList(w http.ResponseWriter, r *http.Request) {
	if !c.requireRuntimes(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	idx := catalogIndex()
	snaps := c.runtimes.List(ctx)
	items := make([]runtimeItemDTO, 0, len(snaps))
	for _, s := range snaps {
		items = append(items, toRuntimeItem(idx, s))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"object":      "list",
		"generatedAt": time.Now().UTC(),
		"count":       len(items),
		"items":       items,
	})
}

func runtimeGameID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := strings.TrimSpace(r.PathValue("gameId"))
	if id == "" || len(id) > 64 {
		writeConsoleError(w, http.StatusBadRequest, "invalid_game_id", "Identifiant de jeu invalide.")
		return "", false
	}
	return id, true
}

func (c *adminConsole) runtimeGet(w http.ResponseWriter, r *http.Request) {
	if !c.requireRuntimes(w) {
		return
	}
	id, ok := runtimeGameID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	snap, err := c.runtimes.Get(ctx, id)
	if err != nil {
		writeRuntimeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": toRuntimeItem(catalogIndex(), snap)})
}

func (c *adminConsole) runtimeRefresh(w http.ResponseWriter, r *http.Request) {
	if !c.requireRuntimes(w) {
		return
	}
	id, ok := runtimeGameID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	snap, refreshed, err := c.runtimes.Refresh(ctx, id)
	if err != nil {
		writeRuntimeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"refreshed": refreshed,
		"item":      toRuntimeItem(catalogIndex(), snap),
	})
}

func writeRuntimeError(w http.ResponseWriter, err error) {
	if errors.Is(err, runtimeregistry.ErrUnknownGame) {
		writeConsoleError(w, http.StatusNotFound, "game_not_found", "Produit inconnu.")
		return
	}
	writeConsoleError(w, http.StatusInternalServerError, "runtime_check_failed", "Vérification du runtime impossible.")
}
