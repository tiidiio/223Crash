package wallet

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

// ErrBalanceUnavailable est enveloppée dans l'erreur retournée par Send lorsque l'opérateur a
// confirmé l'opération (HTTP 200, OutcomeAccepted) mais que le solde de la réponse n'a pas pu
// être lu ou décodé. L'opération EST appliquée ; seul le solde est inconnu. Dans ce cas la
// valeur int64 retournée par Send (0) n'est PAS un solde : elle ne doit jamais être enregistrée.
// Utiliser BalanceOrNil pour obtenir le solde à transmettre à ledger.Confirm.
var ErrBalanceUnavailable = errors.New("wallet: solde indisponible dans la réponse de l'opérateur")

// BalanceOrNil convertit le couple (balance, err) retourné par Send en solde optionnel pour
// ledger.Confirm : nil quand le solde est indisponible (jamais un faux 0), &balance sinon.
func BalanceOrNil(balance int64, err error) *int64 {
	if errors.Is(err, ErrBalanceUnavailable) {
		return nil
	}
	return &balance
}

// jsonMarshal est le point d'injection de l'encodage du corps (tests uniquement : json.Marshal
// d'un Callback ne peut pas échouer avec les types actuels).
var jsonMarshal = json.Marshal

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
//
// Contrat du solde retourné : il n'est valide que si l'erreur ne contient pas
// ErrBalanceUnavailable. Un OutcomeAccepted avec une erreur non-nil signifie "opération
// appliquée, solde inconnu" — jamais "solde = 0".
func (c *Client) Send(ctx context.Context, cb Callback) (int64, Outcome, error) {
	body, err := jsonMarshal(cb)
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
		// ensuite à la lecture ou au parsing du corps. Seul le solde est alors perdu : on le
		// signale par ErrBalanceUnavailable pour que l'appelant ne l'enregistre pas comme 0.
		if readErr != nil {
			return 0, OutcomeAccepted, fmt.Errorf("wallet: %s accepté (HTTP 200) mais corps illisible: %w: %w", cb.Type, ErrBalanceUnavailable, readErr)
		}
		var out CallbackResponse
		if err := json.Unmarshal(raw, &out); err != nil {
			return 0, OutcomeAccepted, fmt.Errorf("wallet: %s accepté (HTTP 200) mais réponse invalide: %w: %w", cb.Type, ErrBalanceUnavailable, err)
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
// Les identifiants sont encodés dans la query string : un operatorID ou playerID contenant
// '&', '=', '#' ou des espaces ne peut ni injecter de paramètre ni tronquer la requête.
func (c *Client) GetBalance(ctx context.Context, operatorID, playerID string) (int64, error) {
	q := url.Values{}
	q.Set("operatorId", operatorID)
	q.Set("playerId", playerID)
	target := c.baseURL + "/wallet/balance?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, fmt.Errorf("wallet: get balance: build request: %w", err)
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
		return 0, fmt.Errorf("wallet: get balance: décodage: %w", err)
	}
	return out.Balance, nil
}
