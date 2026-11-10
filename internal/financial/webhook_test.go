package financial

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func verifyWebhookTestSignature(secret string, timestampStr string, body []byte, signature string) bool {
	var timestamp int64
	_, err := fmt.Sscanf(timestampStr, "%d", &timestamp)
	if err != nil {
		return false
	}
	expectedSig := generateWebhookSignature(secret, timestamp, body)
	return hmac.Equal([]byte(expectedSig), []byte(signature))
}

func TestDispatchRoundEnded_SuccessAndSignature(t *testing.T) {
	const secret = "test_webhook_secret_abc"
	
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST method, got %s", r.Method)
		}
		if r.Header.Get("X-Event-Type") != "ROUND_ENDED" {
			t.Errorf("expected X-Event-Type ROUND_ENDED, got %s", r.Header.Get("X-Event-Type"))
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}

		ts := r.Header.Get("X-Timestamp")
		sig := r.Header.Get("X-Signature")

		if !verifyWebhookTestSignature(secret, ts, body, sig) {
			t.Errorf("webhook HMAC signature verification failed")
		}

		var payload RoundEndedWebhookPayload
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("failed to unmarshal webhook payload: %v", err)
		}

		if payload.RoundID != "round_crash_888" || payload.CrashPoint != 3.45 {
			t.Errorf("unexpected payload content: %+v", payload)
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"received"}`))
	}))
	defer server.Close()

	dispatcher := NewWebhookDispatcher(2 * time.Second)
	payload := RoundEndedWebhookPayload{
		EventID:      "evt_1001",
		EventType:    "ROUND_ENDED",
		OperatorID:   "op_123",
		RoundID:      "round_crash_888",
		CrashPoint:   3.45,
		Timestamp:    time.Now().Unix(),
		TotalPlayers: 14,
	}

	err := dispatcher.DispatchRoundEnded(context.Background(), server.URL, secret, payload)
	if err != nil {
		t.Fatalf("unexpected error dispatching webhook: %v", err)
	}
}

func TestDispatchRoundEnded_OperatorError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal operator error"))
	}))
	defer server.Close()

	dispatcher := NewWebhookDispatcher(2 * time.Second)
	payload := RoundEndedWebhookPayload{
		EventID:    "evt_1002",
		EventType:  "ROUND_ENDED",
		OperatorID: "op_123",
		RoundID:    "round_crash_999",
		CrashPoint: 1.10,
	}

	err := dispatcher.DispatchRoundEnded(context.Background(), server.URL, "secret", payload)
	if err == nil {
		t.Fatalf("expected error due to HTTP 500 response, got nil")
	}
}
