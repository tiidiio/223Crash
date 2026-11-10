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

const (
	testAPIKey     = "test_op_api_key_123"
	testHMACSecret = "super_secret_hmac_key_999"
)

func verifyTestSignature(secret string, timestampStr string, body []byte, signature string) bool {
	var timestamp int64
	fmt.Sscanf(timestampStr, "%d", &timestamp)
	expectedSig := generateSignature(secret, timestamp, body)
	return hmac.Equal([]byte(expectedSig), []byte(signature))
}

func TestGetBalance_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/wallet/balance" {
			t.Errorf("expected path /wallet/balance, got %s", r.URL.Path)
		}
		if r.Header.Get("X-API-Key") != testAPIKey {
			t.Errorf("invalid API key header")
		}

		body, _ := io.ReadAll(r.Body)
		sig := r.Header.Get("X-Signature")
		ts := r.Header.Get("X-Timestamp")
		if !verifyTestSignature(testHMACSecret, ts, body, sig) {
			t.Errorf("HMAC signature verification failed")
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(BalanceResponse{
			Success:  true,
			Balance:  50000,
			Currency: "EUR",
		})
	}))
	defer server.Close()

	client := NewHTTPWalletClient(2 * time.Second)
	resp, err := client.GetBalance(context.Background(), server.URL, testAPIKey, testHMACSecret, BalanceRequest{
		OperatorID: "op_123",
		PlayerID:   "player_abc",
		Currency:   "EUR",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Success || resp.Balance != 50000 || resp.Currency != "EUR" {
		t.Errorf("unexpected response content: %+v", resp)
	}
}

func TestDebit_SuccessAndIdempotencyHeader(t *testing.T) {
	idempotencyKey := "idemp_debit_998877"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/wallet/debit" {
			t.Errorf("expected path /wallet/debit, got %s", r.URL.Path)
		}
		if r.Header.Get("Idempotency-Key") != idempotencyKey {
			t.Errorf("expected Idempotency-Key %s, got %s", idempotencyKey, r.Header.Get("Idempotency-Key"))
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(TransactionResponse{
			Success:       true,
			TransactionID: "tx_debit_001",
			Status:        string(StatusSuccess),
			Balance:       45000,
			Currency:      "EUR",
		})
	}))
	defer server.Close()

	client := NewHTTPWalletClient(2 * time.Second)
	resp, err := client.Debit(context.Background(), server.URL, testAPIKey, testHMACSecret, DebitRequest{
		OperatorID:     "op_123",
		PlayerID:       "player_abc",
		RoundID:        "round_99",
		BetID:          "bet_1",
		TransactionID:  "tx_debit_001",
		IdempotencyKey: idempotencyKey,
		Currency:       "EUR",
		Amount:         5000,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != string(StatusSuccess) || resp.Balance != 45000 {
		t.Errorf("unexpected debit response: %+v", resp)
	}
}

func TestGetStatus_Reconciliation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/wallet/transaction/status" {
			t.Errorf("expected path /wallet/transaction/status, got %s", r.URL.Path)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(StatusResponse{
			TransactionID: "tx_debit_001",
			Status:        string(StatusSuccess),
			Amount:        5000,
			Currency:      "EUR",
		})
	}))
	defer server.Close()

	client := NewHTTPWalletClient(2 * time.Second)
	resp, err := client.GetStatus(context.Background(), server.URL, testAPIKey, testHMACSecret, StatusRequest{
		OperatorID:     "op_123",
		PlayerID:       "player_abc",
		TransactionID:  "tx_debit_001",
		IdempotencyKey: "idemp_debit_998877",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != string(StatusSuccess) || resp.TransactionID != "tx_debit_001" {
		t.Errorf("unexpected status response: %+v", resp)
	}
}

func TestClient_TimeoutHandling(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewHTTPWalletClient(10 * time.Millisecond)
	_, err := client.Debit(context.Background(), server.URL, testAPIKey, testHMACSecret, DebitRequest{
		OperatorID:     "op_123",
		PlayerID:       "player_abc",
		TransactionID:  "tx_timeout",
		IdempotencyKey: "idemp_timeout",
		Amount:         1000,
	})

	if err == nil {
		t.Fatalf("expected timeout error, got nil")
	}
}
