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

	"github.com/tiidiio/223Gaming/223Crash/internal/provider"
)

// Outcome classe le résultat d'un appel Send de façon à distinguer un rejet métier certain
// d'un résultat réseau indéterminé. Cette distinction est essentielle : un timeout ne signifie
// PAS "l'opération n'a pas eu lieu" — il peut signifier "l'opération a eu lieu mais la réponse
// a été perdue". Confondre les deux dans un simple `error` ferait perdre cette information au
// Ledger, qui doit pouvoir décider Confirm/Fail/PENDING en toute connaissance de cause.
type Outcome int

const (
	// OutcomeAccepted : l'opérateur a explicitement confirmé le traitement (HTTP 200).
	OutcomeAccepted Outcome = iota
	// OutcomeRejected : rejet certain — la requête n'a jamais été envoyée (erreur de
	// construction locale) ou l'opérateur l'a explicitement refusée (HTTP 4xx). Aucun doute :
	// l'opération n'a pas été appliquée côté opérateur.
	OutcomeRejected
	// OutcomeUnknown : résultat indéterminé — timeout, connexion coupée, DNS, HTTP 5xx, ou tout
	// code de statut inattendu. L'opérateur a pu recevoir et traiter la requête avant que la
	// réponse ne soit perdue. Ne JAMAIS traiter comme un rejet.
	OutcomeUnknown
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

// Send envoie un callback signé à l'opérateur et classe le résultat en Outcome — voir le
// commentaire sur le type Outcome pour la sémantique exacte de chaque valeur. Utilisé
// directement pour DEBIT (synchrone, doit réussir avant d'accepter un pari) et par la Queue
// pour CREDIT/ROLLBACK (avec retry en cas de résultat indéterminé — voir Tranche 3bis).
func (c *Client) Send(ctx context.Context, cb Callback) (int64, Outcome, error) {
	body, err := json.Marshal(cb)
	if err != nil {
		return 0, OutcomeRejected, fmt.Errorf("wallet: marshal callback: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/wallet/callback", bytes.NewReader(body))
	if err != nil {
		return 0, OutcomeRejected, fmt.Errorf("wallet: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Signature", c.sign(body))
	req.Header.Set("Idempotency-Key", cb.IdempotencyKey)

	resp, err := c.http.Do(req)
	if err != nil {
		// La requête a pu partir (partiellement ou totalement) sans que la réponse ne revienne
		// — timeout, connexion coupée, DNS. Impossible de savoir si l'opérateur a traité
		// l'opération : résultat indéterminé, jamais un rejet.
		return 0, OutcomeUnknown, fmt.Errorf("wallet: send %s: %w", cb.Type, err)
	}
	defer resp.Body.Close()

	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	switch {
	case resp.StatusCode == http.StatusOK:
		// L'opérateur a explicitement confirmé le traitement — c'est acquis quoi qu'il arrive
		// ensuite à la lecture ou au parsing du corps (seul le solde retourné serait perdu).
		if readErr != nil {
			return 0, OutcomeAccepted, fmt.Errorf("wallet: %s accepté (HTTP 200) mais corps illisible: %w", cb.Type, readErr)
		}
		var out CallbackResponse
		if err := json.Unmarshal(raw, &out); err != nil {
			return 0, OutcomeAccepted, fmt.Errorf("wallet: %s accepté (HTTP 200) mais réponse invalide: %w", cb.Type, err)
		}
		return out.Balance, OutcomeAccepted, nil

	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		// Rejet métier explicite de l'opérateur (fonds insuffisants, validation, etc.) —
		// aucun doute possible : l'opération n'a pas été appliquée.
		return 0, OutcomeRejected, fmt.Errorf("wallet: %s rejeté (HTTP %d): %s", cb.Type, resp.StatusCode, string(raw))

	default:
		// 5xx ou tout code inattendu : l'opérateur a pu traiter l'opération avant d'échouer en
		// répondant. Ne jamais conclure à un rejet sur cette seule base.
		return 0, OutcomeUnknown, fmt.Errorf("wallet: %s résultat indéterminé (HTTP %d): %s", cb.Type, resp.StatusCode, string(raw))
	}
}

// Debit tente un débit synchrone. À appeler AVANT d'accepter un pari côté engine —
// si ça échoue de façon certaine (OutcomeRejected) ou indéterminée (OutcomeUnknown), le pari
// ne doit jamais être placé.
//
// gameID identifie le Game Product à l'origine du débit (valeur opaque du Provider Core,
// jamais interprétée par ce package). txnID corrèle ce Callback avec l'entrée Ledger
// correspondante, ouverte par l'appelant via ledger.Begin() avant cet appel.
func (c *Client) Debit(ctx context.Context, gameID provider.GameID, txnID provider.TransactionID, operatorID, playerID, roundID, betID string, amount int64) (int64, Outcome, error) {
	return c.Send(ctx, Callback{
		ID:             fmt.Sprintf("debit-%s-%s-%s", operatorID, playerID, betID),
		Type:           CallbackDebit,
		GameID:         gameID,
		TransactionID:  txnID,
		OperatorID:     operatorID,
		PlayerID:       playerID,
		RoundID:        roundID,
		BetID:          betID,
		Amount:         amount,
		IdempotencyKey: fmt.Sprintf("debit:%s:%s:%s", operatorID, playerID, betID),
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
