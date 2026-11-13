package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/tiidiio/223Crash/internal/gamecatalog"
)

// adminGamesHandler : variante sans registre de runtimes (utilisée par les tests).
func adminGamesHandler(w http.ResponseWriter, r *http.Request) {
	(&adminConsole{}).games(w, r)
}

// games distingue l'enregistrement du produit (catalogue statique) de son état runtime,
// mesuré par le registre. Sans registre, "runtime" le déclare explicitement.
func (c *adminConsole) games(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	writeJSON(w, http.StatusOK, map[string]any{
		"available": true,
		"source":    "static_catalog",
		"items":     gamecatalog.List(),
		"runtime":   c.legacyRuntime(ctx),
		"runtimes":  c.runtimeSnapshots(ctx),
	})
}
