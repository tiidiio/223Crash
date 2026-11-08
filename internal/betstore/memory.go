package betstore

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// MemoryStore est une implémentation en mémoire de Store pour les tests. Elle n'apporte aucune
// durabilité : ne jamais l'utiliser en production.
type MemoryStore struct {
	mu   sync.Mutex
	bets map[string]*Bet // clé = debitTransactionId (globalement unique, comme l'index MongoDB)
}

func NewMemory() *MemoryStore {
	return &MemoryStore{bets: make(map[string]*Bet)}
}

var _ Store = (*MemoryStore)(nil)

func (m *MemoryStore) find(operatorID, debitTxn string) (*Bet, error) {
	b, ok := m.bets[debitTxn]
	if !ok || b.OperatorID != operatorID {
		return nil, ErrNotFound
	}
	return b, nil
}

func (m *MemoryStore) touch(b *Bet) {
	b.UpdatedAt = time.Now()
	b.Version++
}

func (m *MemoryStore) Insert(_ context.Context, in Bet) (*Bet, bool, error) {
	if err := in.validate(); err != nil {
		return nil, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if ex, ok := m.bets[in.DebitTransactionID]; ok {
		if ex.OperatorID != in.OperatorID {
			return nil, false, fmt.Errorf("betstore: txn %s déjà associée à un autre opérateur", in.DebitTransactionID)
		}
		cp := *ex
		return &cp, false, nil
	}
	for _, ex := range m.bets {
		if ex.OperatorID == in.OperatorID && ex.PlayerID == in.PlayerID && ex.ClientBetID == in.ClientBetID {
			return nil, false, fmt.Errorf("betstore: clientBetId %s déjà associé à la txn %s", in.ClientBetID, ex.DebitTransactionID)
		}
	}

	now := time.Now()
	in.State = StatePending
	in.CreatedAt, in.UpdatedAt = now, now
	in.Version = 1
	in.CreditTransactionID, in.RollbackTransactionID = "", ""
	in.CashoutMultiplier, in.PayoutMinor, in.CashoutAt, in.ReconcileReason = 0, 0, time.Time{}, ""
	cp := in
	m.bets[in.DebitTransactionID] = &cp
	out := in
	return &out, true, nil
}

func (m *MemoryStore) Get(_ context.Context, operatorID, debitTxn string) (*Bet, error) {
	if err := checkRef(operatorID, debitTxn); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	b, err := m.find(operatorID, debitTxn)
	if err != nil {
		return nil, err
	}
	cp := *b
	return &cp, nil
}

func (m *MemoryStore) MarkCashedOut(_ context.Context, operatorID, debitTxn string, multiplier float64, payoutMinor int64, at time.Time) (*Bet, error) {
	if err := checkRef(operatorID, debitTxn); err != nil {
		return nil, err
	}
	if err := checkCashout(multiplier, payoutMinor); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	b, err := m.find(operatorID, debitTxn)
	if err != nil {
		return nil, err
	}
	switch {
	case b.State == StatePending:
		b.State = StateCashedOut
		b.CashoutMultiplier = multiplier
		b.PayoutMinor = payoutMinor
		b.CashoutAt = at
		m.touch(b)
	case b.State == StateCashedOut && b.PayoutMinor == payoutMinor && b.CashoutMultiplier == multiplier:
	default:
		return nil, conflict(debitTxn, b.State, StateCashedOut)
	}
	cp := *b
	return &cp, nil
}

func (m *MemoryStore) MarkLost(_ context.Context, operatorID, debitTxn string, at time.Time) (*Bet, error) {
	if err := checkRef(operatorID, debitTxn); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	b, err := m.find(operatorID, debitTxn)
	if err != nil {
		return nil, err
	}
	switch b.State {
	case StatePending:
		b.State = StateLost
		b.CashoutAt = at
		m.touch(b)
	case StateLost:
	default:
		return nil, conflict(debitTxn, b.State, StateLost)
	}
	cp := *b
	return &cp, nil
}

func (m *MemoryStore) MarkReconciling(_ context.Context, operatorID, debitTxn, reason string) (*Bet, error) {
	if err := checkRef(operatorID, debitTxn); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	b, err := m.find(operatorID, debitTxn)
	if err != nil {
		return nil, err
	}
	switch b.State {
	case StatePending:
		b.State = StateReconciling
		b.ReconcileReason = reason
		m.touch(b)
	case StateReconciling:
	default:
		return nil, conflict(debitTxn, b.State, StateReconciling)
	}
	cp := *b
	return &cp, nil
}

func (m *MemoryStore) SetCreditRef(_ context.Context, operatorID, debitTxn, creditTxn string) (*Bet, error) {
	if err := checkRef(operatorID, debitTxn); err != nil {
		return nil, err
	}
	if creditTxn == "" {
		return nil, fmt.Errorf("%w: creditTransactionId requis", ErrInvalid)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	b, err := m.find(operatorID, debitTxn)
	if err != nil {
		return nil, err
	}
	switch {
	case b.State == StateCashedOut && b.CreditTransactionID == "":
		b.CreditTransactionID = creditTxn
		m.touch(b)
	case b.State == StateCashedOut && b.CreditTransactionID == creditTxn:
	default:
		return nil, conflict(debitTxn, b.State, StateCashedOut)
	}
	cp := *b
	return &cp, nil
}

func (m *MemoryStore) ReconcileOrphans(_ context.Context, instanceID string, olderThan time.Time, reason string) (int64, error) {
	if instanceID == "" {
		return 0, fmt.Errorf("%w: instanceId requis", ErrInvalid)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for _, b := range m.bets {
		if b.State == StatePending && b.InstanceID != instanceID && b.UpdatedAt.Before(olderThan) {
			b.State = StateReconciling
			b.ReconcileReason = reason
			m.touch(b)
			n++
		}
	}
	return n, nil
}

func (m *MemoryStore) ListByState(_ context.Context, state State, limit int) ([]Bet, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Bet, 0)
	for _, b := range m.bets {
		if b.State == state {
			out = append(out, *b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
