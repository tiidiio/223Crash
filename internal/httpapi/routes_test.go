package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProviderRoutes(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		wantStatus int
		service    string
		status     string
	}{
		{
			name:       "health endpoint",
			path:       "/healthz",
			wantStatus: http.StatusOK,
			service:    "223gaming",
			status:     "ok",
		},
		{
			name:       "provider endpoint",
			path:       "/api/v1/provider",
			wantStatus: http.StatusOK,
			service:    "223GAMING PROVIDER",
			status:     "ok",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			rec := httptest.NewRecorder()

			NewMux().ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status: got %d, want %d",
					rec.Code, tt.wantStatus)
			}

			var got response
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("invalid JSON: %v", err)
			}

			if got.Service != tt.service {
				t.Errorf("service: got %q, want %q",
					got.Service, tt.service)
			}

			if got.Status != tt.status {
				t.Errorf("status: got %q, want %q",
					got.Status, tt.status)
			}

			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Error("expected Cache-Control: no-store")
			}
		})
	}
}

func TestUnknownRoute(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/unknown", nil)
	rec := httptest.NewRecorder()

	NewMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", rec.Code)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/healthz", nil)
	rec := httptest.NewRecorder()

	NewMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("got %d, want 405", rec.Code)
	}
}

func TestStudioCompatibilityRoute(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/studio", nil)
	rec := httptest.NewRecorder()

	NewMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
	}

	var got response
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}

	if got.Status != "operational" {
		t.Errorf("status: got %q, want operational", got.Status)
	}
	if got.Service != "223GAMING STUDIO" {
		t.Errorf("service: got %q, want 223GAMING STUDIO", got.Service)
	}
	if got.Version != "0.1.0" {
		t.Errorf("version: got %q, want 0.1.0", got.Version)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("expected Cache-Control: no-store")
	}
}

func TestReadinessWithoutStore(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()

	NewMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d, want 503", rec.Code)
	}

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if got["status"] != "not_ready" {
		t.Errorf("status: got %v, want not_ready", got["status"])
	}

	if got["mongo"] != "not_configured" {
		t.Errorf("mongo: got %v, want not_configured", got["mongo"])
	}
}

func TestHealthRemainsAvailableWithoutStore(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	NewMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
}
