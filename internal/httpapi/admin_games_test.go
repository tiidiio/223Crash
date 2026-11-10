package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tiidiio/223Gaming/internal/gamecatalog"
)

func TestAdminGamesHandler(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/admin/games", nil)
	rec := httptest.NewRecorder()

	adminGamesHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	if rec.Header().Get("Content-Type") != "application/json" {
		t.Error("expected application/json")
	}

	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("expected Cache-Control: no-store")
	}

	var response struct {
		Available bool               `json:"available"`
		Items     []gamecatalog.Game `json:"items"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if !response.Available {
		t.Fatal("catalog should be available")
	}

	if len(response.Items) != 1 {
		t.Fatalf("got %d items, want 1", len(response.Items))
	}

	if response.Items[0].ID != "223crash" {
		t.Errorf("unexpected game ID: %q", response.Items[0].ID)
	}
}

func TestAdminGamesRouteRequiresAuthentication(t *testing.T) {
	req := httptest.NewRequest(
		http.MethodGet,
		"/api/admin/games",
		nil,
	)

	rec := httptest.NewRecorder()

	NewMux().ServeHTTP(rec, req)

	// NewMux has no authentication store configured.
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}

	var response map[string]any

	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if response["error"] != "authentication_unavailable" {
		t.Errorf("unexpected response: %v", response)
	}
}

func TestAdminGamesRouteRejectsOtherMethods(t *testing.T) {
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/admin/games",
		nil,
	)

	rec := httptest.NewRecorder()

	NewMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}
