package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	v1 "github.com/tiidiio/223Crash/contracts/product/v1"
)

func TestProductSessionHandshakeIntegration(t *testing.T) {
	mux := NewMux()

	reqPayload := v1.SessionHandshakeRequest{
		Token:     "operator-auth-token-xyz",
		ProductID: "223crash",
		Mode:      "demo",
		ClientIP:  "127.0.0.1",
	}

	bodyBytes, err := json.Marshal(reqPayload)
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/session/handshake", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}
}

func TestProductSessionHandshakeInvalidProduct(t *testing.T) {
	mux := NewMux()

	reqPayload := v1.SessionHandshakeRequest{
		Token:     "token-xyz",
		ProductID: "unknown-game",
		Mode:      "demo",
	}

	bodyBytes, _ := json.Marshal(reqPayload)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/session/handshake", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for structured adapter response", rec.Code)
	}

	var resp v1.SessionHandshakeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.IsValid {
		t.Fatalf("expected is_valid = false for unknown game")
	}
}
