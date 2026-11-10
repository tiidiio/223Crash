package ledger

import (
	"context"
	"testing"
	"time"
)

func TestMongoIdempotencyScopeAcrossOperators(t *testing.T) {
	l, cleanup := newTestLedger(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	const sharedKey IdempotencyKey = "scope-test-shared-key"

	opA := NewEntry{
		GameID:         "crash",
		OperatorID:     "scope-test-op-A",
		PlayerID:       "scope-test-player",
		IdempotencyKey: sharedKey,
		Type:           EntryDebit,
		AmountMinor:    100,
	}

	opB := NewEntry{
		GameID:         "crash",
		OperatorID:     "scope-test-op-B",
		PlayerID:       "scope-test-player",
		IdempotencyKey: sharedKey,
		Type:           EntryDebit,
		AmountMinor:    100,
	}

	firstA, createdA, err := l.Begin(ctx, opA)
	if err != nil {
		t.Fatalf("création opA: %v", err)
	}
	if !createdA || firstA == nil {
		t.Fatalf(
			"opA doit créer une entrée: entry=%+v created=%v",
			firstA,
			createdA,
		)
	}

	firstB, createdB, err := l.Begin(ctx, opB)
	if err != nil {
		t.Fatalf("création opB avec la même clé: %v", err)
	}
	if !createdB || firstB == nil {
		t.Fatalf(
			"opB doit créer sa propre entrée: entry=%+v created=%v",
			firstB,
			createdB,
		)
	}

	if firstA.TransactionID == firstB.TransactionID {
		t.Fatalf(
			"les deux opérateurs ont reçu le même transactionId: %s",
			firstA.TransactionID,
		)
	}

	if firstA.OperatorID != opA.OperatorID ||
		firstB.OperatorID != opB.OperatorID {
		t.Fatalf(
			"périmètres opérateurs incorrects: A=%s B=%s",
			firstA.OperatorID,
			firstB.OperatorID,
		)
	}

	retryA, retryCreatedA, err := l.Begin(ctx, opA)
	if err != nil {
		t.Fatalf("retry opA: %v", err)
	}
	if retryCreatedA || retryA == nil ||
		retryA.TransactionID != firstA.TransactionID {
		t.Fatalf(
			"retry opA doit retrouver son entrée: original=%+v retry=%+v created=%v",
			firstA,
			retryA,
			retryCreatedA,
		)
	}

	retryB, retryCreatedB, err := l.Begin(ctx, opB)
	if err != nil {
		t.Fatalf("retry opB: %v", err)
	}
	if retryCreatedB || retryB == nil ||
		retryB.TransactionID != firstB.TransactionID {
		t.Fatalf(
			"retry opB doit retrouver son entrée: original=%+v retry=%+v created=%v",
			firstB,
			retryB,
			retryCreatedB,
		)
	}

	gotA, err := l.GetByIdempotencyKey(ctx, opA.OperatorID, sharedKey)
	if err != nil {
		t.Fatalf("lecture scoped opA: %v", err)
	}

	gotB, err := l.GetByIdempotencyKey(ctx, opB.OperatorID, sharedKey)
	if err != nil {
		t.Fatalf("lecture scoped opB: %v", err)
	}

	if gotA.TransactionID != firstA.TransactionID ||
		gotB.TransactionID != firstB.TransactionID {
		t.Fatalf(
			"lecture scoped incorrecte: A=%+v B=%+v",
			gotA,
			gotB,
		)
	}
}
