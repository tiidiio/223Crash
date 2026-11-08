package betstore

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// StateVoided : le DEBIT a été remboursé (ou est en cours de remboursement) parce qu'aucun pari
// n'a pu être placé. État terminal, atteint uniquement depuis PENDING. La raison est conservée
// dans reconcileReason.
const StateVoided State = "VOIDED"

// VoidStore étend Store avec l'annulation d'un pari jamais placé.
type VoidStore interface {
	Store
	// MarkVoided : PENDING -> VOIDED. Rejouer est idempotent ; depuis tout autre état, ErrStateConflict.
	MarkVoided(ctx context.Context, operatorID, debitTxn, reason string) (*Bet, error)
}

var (
	_ VoidStore = (*MemoryStore)(nil)
	_ VoidStore = (*MongoStore)(nil)
)

func (m *MemoryStore) MarkVoided(_ context.Context, operatorID, debitTxn, reason string) (*Bet, error) {
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
		b.State = StateVoided
		b.ReconcileReason = reason
		m.touch(b)
	case StateVoided:
	default:
		return nil, conflict(debitTxn, b.State, StateVoided)
	}
	cp := *b
	return &cp, nil
}

func (s *MongoStore) MarkVoided(ctx context.Context, operatorID, debitTxn, reason string) (*Bet, error) {
	if err := checkRef(operatorID, debitTxn); err != nil {
		return nil, err
	}
	b, ok, err := s.transition(ctx, operatorID, debitTxn, StatePending, nil, bson.M{
		"state":           StateVoided,
		"reconcileReason": reason,
		"updatedAt":       time.Now(),
	})
	if err != nil || ok {
		return b, err
	}
	cur, err := s.find(ctx, operatorID, debitTxn)
	if err != nil {
		return nil, err
	}
	if cur.State == StateVoided {
		return cur, nil
	}
	return nil, conflict(debitTxn, cur.State, StateVoided)
}
