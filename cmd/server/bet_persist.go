package main

import (
	"context"
	"sync"

	"github.com/tiidiio/223Gaming/provider/ledger"
)

// BetStore garde le lien betID (engine) -> debit TransactionID (unique, jamais réutilisé)
// Fix BUG 1 : la clé de crédit ne doit JAMAIS être basée sur betID (bet-N repart à 0 au restart)
// mais sur debitTxnID (ObjectID unique Mongo).
type BetStore interface {
	Remember(ctx context.Context, betID string, debitTxnID ledger.TransactionID, operatorID, playerID string) error
	GetDebitTxnID(betID string) (ledger.TransactionID, bool)
}

type memBetStore struct {
	mu   sync.RWMutex
	m    map[string]ledger.TransactionID
}

func NewMemBetStore() BetStore {
	return &memBetStore{m: make(map[string]ledger.TransactionID)}
}

func (s *memBetStore) Remember(_ context.Context, betID string, debitTxnID ledger.TransactionID, _, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[betID] = debitTxnID
	return nil
}

func (s *memBetStore) GetDebitTxnID(betID string) (ledger.TransactionID, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.m[betID]
	return v, ok
}

// MongoBetStore à implémenter si besoin - pour l'instant mem suffit pour build
// NewMongoBetStore est appelé depuis main.go si tu veux persister
func NewMongoBetStore() BetStore { return NewMemBetStore() }
