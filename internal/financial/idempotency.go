package financial

import (
	"errors"
	"sync"
)

// IdempotencyManager gère la protection contre les doubles paiements et la reprise sur ambiguïté.
type IdempotencyManager struct {
	mu           sync.Mutex
	transactions map[string]Transaction
}

// NewIdempotencyManager instancie le gestionnaire.
func NewIdempotencyManager() *IdempotencyManager {
	return &IdempotencyManager{
		transactions: make(map[string]Transaction),
	}
}

// RegisterOrGet vérifie l'existence d'une clé d'idempotence.
func (m *IdempotencyManager) RegisterOrGet(tx Transaction) (Transaction, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if tx.IdempotencyKey == "" {
		return Transaction{}, false, errors.New("clé d'idempotence obligatoire")
	}

	if existing, found := m.transactions[tx.IdempotencyKey]; found {
		return existing, true, nil
	}

	tx.Status = StatusPending
	m.transactions[tx.IdempotencyKey] = tx
	return tx, false, nil
}

// UpdateStatus met à jour l'état d'une transaction.
func (m *IdempotencyManager) UpdateStatus(idempotencyKey string, status TxStatus, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	tx, found := m.transactions[idempotencyKey]
	if !found {
		return errors.New("transaction introuvable pour cette clé d'idempotence")
	}

	if tx.Status == StatusSuccess && status == StatusAmbiguous {
		return errors.New("violation d'invariant : impossible de passer une transaction SUCCESS en AMBIGUOUS")
	}

	tx.Status = status
	tx.ErrorReason = reason
	m.transactions[idempotencyKey] = tx
	return nil
}
