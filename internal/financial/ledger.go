package financial

import (
	"errors"
	"fmt"
	"time"
)

// OperationType représente le type d'opération financière B2B.
type OperationType string

const (
	OpDebit    OperationType = "DEBIT"
	OpCredit   OperationType = "CREDIT"
	OpRollback OperationType = "ROLLBACK"
)

// TxStatus représente l'état d'une transaction financière.
type TxStatus string

const (
	StatusPending   TxStatus = "PENDING"
	StatusSuccess   TxStatus = "SUCCESS"
	StatusFailed    TxStatus = "FAILED"
	StatusAmbiguous TxStatus = "AMBIGUOUS"
)

// Transaction représente un ordre financier tracé.
type Transaction struct {
	ID             string        `json:"id"`
	IdempotencyKey string        `json:"idempotency_key"`
	PlayerID       string        `json:"player_id"`
	RoundID        string        `json:"round_id"`
	Type           OperationType `json:"type"`
	Amount         int64         `json:"amount"` // En plus petite unité (ex: centimes / FCFA)
	Currency       string        `json:"currency"`
	Status         TxStatus      `json:"status"`
	CreatedAt      time.Time     `json:"created_at"`
	UpdatedAt      time.Time     `json:"updated_at"`
	ErrorReason    string        `json:"error_reason,omitempty"`
}

// LedgerEntry représente une ligne immuable dans le registre.
type LedgerEntry struct {
	EntryID       string        `json:"entry_id"`
	TransactionID string        `json:"transaction_id"`
	PlayerID      string        `json:"player_id"`
	Type          OperationType `json:"type"`
	Amount        int64         `json:"amount"`
	BalanceAfter  int64         `json:"balance_after"`
	Timestamp     time.Time     `json:"timestamp"`
}

// Ledger gère l'immutabilité des écritures comptables.
type Ledger struct {
	entries []LedgerEntry
}

// NewLedger instancie un nouveau grand livre.
func NewLedger() *Ledger {
	return &Ledger{
		entries: make([]LedgerEntry, 0),
	}
}

// RecordEntry enregistre une entrée comptable de manière append-only.
func (l *Ledger) RecordEntry(tx Transaction, balanceAfter int64) (LedgerEntry, error) {
	if tx.Status != StatusSuccess {
		return LedgerEntry{}, errors.New("impossible d'enregistrer au Ledger une transaction non validée")
	}

	entry := LedgerEntry{
		EntryID:       fmt.Sprintf("entry_%s_%d", tx.ID, time.Now().UnixNano()),
		TransactionID: tx.ID,
		PlayerID:      tx.PlayerID,
		Type:          tx.Type,
		Amount:        tx.Amount,
		BalanceAfter:  balanceAfter,
		Timestamp:     time.Now(),
	}

	l.entries = append(l.entries, entry)
	return entry, nil
}

// GetEntries retourne toutes les entrées du grand livre.
func (l *Ledger) GetEntries() []LedgerEntry {
	return l.entries
}
