package financial

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// RoundEndedWebhookPayload represents the payload sent to operators when a 223CRASH round finishes.
type RoundEndedWebhookPayload struct {
	EventID      string  `json:"event_id"`
	EventType    string  `json:"event_type"` // e.g., "ROUND_ENDED"
	OperatorID   string  `json:"operator_id"`
	RoundID      string  `json:"round_id"`
	CrashPoint   float64 `json:"crash_point"`
	Timestamp    int64   `json:"timestamp"`
	TotalPlayers int     `json:"total_players"`
}

// WebhookDispatcher handles secure dispatching of round notifications to operator webhooks.
type WebhookDispatcher struct {
	httpClient *http.Client
}

func NewWebhookDispatcher(timeout time.Duration) *WebhookDispatcher {
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	return &WebhookDispatcher{
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

// DispatchRoundEnded sends a signed webhook notification to the operator's endpoint.
func (d *WebhookDispatcher) DispatchRoundEnded(ctx context.Context, webhookURL, hmacSecret string, payload RoundEndedWebhookPayload) error {
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal webhook payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewBuffer(bodyBytes))
	if err != nil {
		return fmt.Errorf("failed to create webhook request: %w", err)
	}

	timestamp := time.Now().Unix()
	signature := generateWebhookSignature(hmacSecret, timestamp, bodyBytes)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Timestamp", fmt.Sprintf("%d", timestamp))
	req.Header.Set("X-Signature", signature)
	req.Header.Set("X-Event-Type", payload.EventType)

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("network error dispatching webhook: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		return fmt.Errorf("operator webhook endpoint returned HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

func generateWebhookSignature(secret string, timestamp int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	message := fmt.Sprintf("%d.%s", timestamp, string(body))
	mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}
