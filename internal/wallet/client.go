package wallet

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

// Client parle au endpoint wallet d'un opérateur : signature HMAC-SHA256 du body,
// idempotency key pour permettre les retries sans double-débit/double-crédit côté opérateur.
type Client struct {
	baseURL string
	secret  []byte
	http    *http.Client
}

func NewClient(baseURL, hmacSecret string) *Client {
	return &Client{
		baseURL: baseURL,
		secret:  []byte(hmacSecret),
		http:    &http.Client{Timeout: 5 * time.Second},
	}
}

func (c *Client) sign(body []byte) string {
	mac := hmac.New(sha256.New, c.secret)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// Send envoie un callback signé à l'opérateur et retourne le nouveau solde.
// Utilisé directement pour DEBIT (synchrone, doit réussir avant d'accepter un pari)
// et par la Queue pour CREDIT/ROLLBACK (avec retry en cas d'échec réseau).
func (c *Client) Send(ctx context.Context, cb Callback) (int64, error) {
	body, err := json.Marshal(cb)
	if err != nil {
		return 0, fmt.Errorf("wallet: marshal callback: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/wallet/callback", bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("wallet: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Signature", c.sign(body))
	req.Header.Set("Idempotency-Key", cb.IdempotencyKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("wallet: send %s: %w", cb.Type, err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("wallet: %s rejeté (HTTP %d): %s", cb.Type, resp.StatusCode, string(raw))
	}

	var out CallbackResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return 0, fmt.Errorf("wallet: réponse invalide: %w", err)
	}
	return out.Balance, nil
}

// Debit tente un débit synchrone. À appeler AVANT d'accepter un pari côté engine —
// si ça échoue (fonds insuffisants ou opérateur down), le pari ne doit jamais être placé.
func (c *Client) Debit(ctx context.Context, operatorID, playerID, roundID, betID string, amount int64) (int64, error) {
	return c.Send(ctx, Callback{
		ID:             fmt.Sprintf("debit-%s", betID),
		Type:           CallbackDebit,
		OperatorID:     operatorID,
		PlayerID:       playerID,
		RoundID:        roundID,
		BetID:          betID,
		Amount:         amount,
		IdempotencyKey: fmt.Sprintf("debit:%s", betID),
		CreatedAt:      time.Now(),
	})
}

// GetBalance interroge le solde courant (utilisé sur JOIN, remplace le placeholder 0).
func (c *Client) GetBalance(ctx context.Context, operatorID, playerID string) (int64, error) {
	url := fmt.Sprintf("%s/wallet/balance?operatorId=%s&playerId=%s", c.baseURL, operatorID, playerID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("wallet: get balance: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("wallet: balance HTTP %d", resp.StatusCode)
	}
	var out CallbackResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, err
	}
	return out.Balance, nil
}
