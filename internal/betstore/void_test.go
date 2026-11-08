package betstore

import (
	"context"
	"errors"
	"testing"
	"time"
)

func runVoidContract(t *testing.T, mk func(t *testing.T) VoidStore) {
	ctx := context.Background()

	t.Run("VoidFromPendingIdempotent", func(t *testing.T) {
		s := mk(t)
		s.Insert(ctx, sampleBet("txn_v1", "cbetv101"))
		b, err := s.MarkVoided(ctx, "op1", "txn_v1", "panel occupé")
		if err != nil || b.State != StateVoided || b.ReconcileReason == "" {
			t.Fatalf("void: b=%+v err=%v", b, err)
		}
		if _, err := s.MarkVoided(ctx, "op1", "txn_v1", "autre raison"); err != nil {
			t.Fatalf("rejeu: %v", err)
		}
	})

	t.Run("VoidedIsTerminal", func(t *testing.T) {
		s := mk(t)
		s.Insert(ctx, sampleBet("txn_v2", "cbetv201"))
		s.MarkVoided(ctx, "op1", "txn_v2", "x")
		if _, err := s.MarkCashedOut(ctx, "op1", "txn_v2", 2, 2000, time.Now()); !errors.Is(err, ErrStateConflict) {
			t.Fatalf("cashout après void: %v", err)
		}
		if _, err := s.MarkLost(ctx, "op1", "txn_v2", time.Now()); !errors.Is(err, ErrStateConflict) {
			t.Fatalf("lost après void: %v", err)
		}
		if _, err := s.MarkReconciling(ctx, "op1", "txn_v2", "x"); !errors.Is(err, ErrStateConflict) {
			t.Fatalf("reconciling après void: %v", err)
		}
	})

	t.Run("VoidFromTerminalConflicts", func(t *testing.T) {
		s := mk(t)
		s.Insert(ctx, sampleBet("txn_v3", "cbetv301"))
		s.MarkCashedOut(ctx, "op1", "txn_v3", 2, 2000, time.Now())
		if _, err := s.MarkVoided(ctx, "op1", "txn_v3", "x"); !errors.Is(err, ErrStateConflict) {
			t.Fatalf("void après cashout: %v", err)
		}
	})

	t.Run("VoidOperatorIsolationAndOrphans", func(t *testing.T) {
		s := mk(t)
		s.Insert(ctx, sampleBet("txn_v4", "cbetv401"))
		if _, err := s.MarkVoided(ctx, "op2", "txn_v4", "x"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("autre opérateur: %v", err)
		}
		s.MarkVoided(ctx, "op1", "txn_v4", "x")
		if n, err := s.ReconcileOrphans(ctx, "inst-B", time.Now().Add(time.Hour), "r"); err != nil || n != 0 {
			t.Fatalf("un VOIDED ne doit jamais être réconcilié: n=%d err=%v", n, err)
		}
	})
}

func TestMemoryStoreVoidContract(t *testing.T) {
	runVoidContract(t, func(t *testing.T) VoidStore { return NewMemory() })
}
