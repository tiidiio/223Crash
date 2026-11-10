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

// Request/Response Structs matching OpenAPI 3.0 Contract

type BalanceRequest struct {
	OperatorID string `json:"operator_id"`
	PlayerID   string `json:"player_id"`
	Currency   string `json:"currency"`
	Timestamp  int64  `json:"timestamp,omitempty"`
}

type BalanceResponse struct {
	Success   bool   `json:"success"`
	Balance   int64  `json:"balance"`
	Currency  string `json:"currency"`
	ErrorCode string `json:"error_code,omitempty"`
	Message   string `json:"message,omitempty"`
}

type DebitRequest struct {
	OperatorID     string                 `json:"operator_id"`
	PlayerID       string                 `json:"player_id"`
	RoundID        string                 `json:"round_id"`
	BetID          string                 `json:"bet_id"`
	TransactionID  string                 `json:"transaction_id"`
	IdempotencyKey string                 `json:"idempotency_key"`
	Currency       string                 `json:"currency"`
	Amount         int64                  `json:"amount"`
	Metadata       map[string]interface{} `json:"metadata,omitempty"`
}

type CreditRequest struct {
	OperatorID     string                 `json:"operator_id"`
	PlayerID       string                 `json:"player_id"`
	RoundID        string                 `json:"round_id"`
	BetID          string                 `json:"bet_id"`
	TransactionID  string                 `json:"transaction_id"`
	IdempotencyKey string                 `json:"idempotency_key"`
	Currency       string                 `json:"currency"`
	Amount         int64                  `json:"amount"`
	Multiplier     float64                `json:"multiplier,omitempty"`
	Metadata       map[string]interface{} `json:"metadata,omitempty"`
}

type RollbackRequest struct {
	OperatorID            string `json:"operator_id"`
	PlayerID              string `json:"player_id"`
	RoundID               string `json:"round_id"`
	TransactionID         string `json:"transaction_id"`
	OriginalTransactionID string `json:"original_transaction_id"`
	IdempotencyKey        string `json:"idempotency_key"`
	Currency              string `json:"currency"`
	Amount                int64  `json:"amount"`
}

type TransactionResponse struct {
	Success       bool   `json:"success"`
	TransactionID string `json:"transaction_id"`
	Status        string `json:"status"`
	Balance       int64  `json:"balance,omitempty"`
	Currency      string `json:"currency,omitempty"`
	ErrorCode     string `json:"error_code,omitempty"`
	Message       string `json:"message,omitempty"`
}

type StatusRequest struct {
	OperatorID     string `json:"operator_id"`
	PlayerID       string `json:"player_id"`
	TransactionID  string `json:"transaction_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

type StatusResponse struct {
	TransactionID         string `json:"transaction_id"`
	Status                string `json:"status"` // SUCCESS, FAILED, PENDING, UNKNOWN
	OriginalTransactionID string `json:"original_transaction_id,omitempty"`
	Amount                int64  `json:"amount,omitempty"`
	Currency              string `json:"currency,omitempty"`
	UpdatedAt             int64  `json:"updated_at,omitempty"`
}

// WalletClient interface for B2B Provider Core
type WalletClient interface {
	GetBalance(ctx context.Context, baseURL, apiKey, hmacSecret string, req BalanceRequest) (*BalanceResponse, error)
	Debit(ctx context.Context, baseURL, apiKey, hmacSecret string, req DebitRequest) (*TransactionResponse, error)
	Credit(ctx context.Context, baseURL, apiKey, hmacSecret string, req CreditRequest) (*TransactionResponse, error)
	Rollback(ctx context.Context, baseURL, apiKey, hmacSecret string, req RollbackRequest) (*TransactionResponse, error)
	GetStatus(ctx context.Context, baseURL, apiKey, hmacSecret string, req StatusRequest) (*StatusResponse, error)
}

// HTTPWalletClient handles signed HTTP communication with Operator Seamless Wallets
type HTTPWalletClient struct {
	httpClient *http.Client
}

func NewHTTPWalletClient(timeout time.Duration) *HTTPWalletClient {
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	return &HTTPWalletClient{
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

// HMAC-SHA256 Request Signing
func generateSignature(secret string, timestamp int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	message := fmt.Sprintf("%d.%s", timestamp, string(body))
	mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}

func (c *HTTPWalletClient) doSignedRequest(ctx context.Context, method, url, apiKey, hmacSecret, idempotencyKey string, payload interface{}, target interface{}) error {
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal request payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewBuffer(bodyBytes))
	if err != nil {
		return fmt.Errorf("failed to create http request: %w", err)
	}

	timestamp := time.Now().Unix()
	signature := generateSignature(hmacSecret, timestamp, bodyBytes)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)
	req.Header.Set("X-Timestamp", fmt.Sprintf("%d", timestamp))
	req.Header.Set("X-Signature", signature)
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("network error during request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode >= 400 && resp.StatusCode != http.StatusConflict && resp.StatusCode != http.StatusUnprocessableEntity {
		return fmt.Errorf("operator wallet returned HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	if err := json.Unmarshal(respBody, target); err != nil {
		return fmt.Errorf("failed to decode wallet response: %w", err)
	}

	return nil
}

func (c *HTTPWalletClient) GetBalance(ctx context.Context, baseURL, apiKey, hmacSecret string, req BalanceRequest) (*BalanceResponse, error) {
	url := fmt.Sprintf("%s/wallet/balance", baseURL)
	var resp BalanceResponse
	err := c.doSignedRequest(ctx, http.MethodPost, url, apiKey, hmacSecret, "", req, &resp)
	return &resp, err
}

func (c *HTTPWalletClient) Debit(ctx context.Context, baseURL, apiKey, hmacSecret string, req DebitRequest) (*TransactionResponse, error) {
	url := fmt.Sprintf("%s/wallet/debit", baseURL)
	var resp TransactionResponse
	err := c.doSignedRequest(ctx, http.MethodPost, url, apiKey, hmacSecret, req.IdempotencyKey, req, &resp)
	return &resp, err
}

func (c *HTTPWalletClient) Credit(ctx context.Context, baseURL, apiKey, hmacSecret string, req CreditRequest) (*TransactionResponse, error) {
	url := fmt.Sprintf("%s/wallet/credit", baseURL)
	var resp TransactionResponse
	err := c.doSignedRequest(ctx, http.MethodPost, url, apiKey, hmacSecret, req.IdempotencyKey, req, &resp)
	return &resp, err
}

func (c *HTTPWalletClient) Rollback(ctx context.Context, baseURL, apiKey, hmacSecret string, req RollbackRequest) (*TransactionResponse, error) {
	url := fmt.Sprintf("%s/wallet/rollback", baseURL)
	var resp TransactionResponse
	err := c.doSignedRequest(ctx, http.MethodPost, url, apiKey, hmacSecret, req.IdempotencyKey, req, &resp)
	return &resp, err
}

func (c *HTTPWalletClient) GetStatus(ctx context.Context, baseURL, apiKey, hmacSecret string, req StatusRequest) (*StatusResponse, error) {
	url := fmt.Sprintf("%s/wallet/transaction/status", baseURL)
	var resp StatusResponse
	err := c.doSignedRequest(ctx, http.MethodPost, url, apiKey, hmacSecret, req.IdempotencyKey, req, &resp)
	return &resp, err
}
