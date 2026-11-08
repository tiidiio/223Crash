package betstore

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func sampleBet(txn, clientBet string) Bet {
	return Bet{
		DebitTransactionID: txn, ClientBetID: clientBet, EngineBetID: "bet-1",
		OperatorID: "op1", PlayerID: "p1", GameID: "223crash", RoundID: "r1",
		Panel: 1, AmountMinor: 1000, CashoutTarget: 2.0, InstanceID: "inst-A",
	}
}

// runContract valide le contrat de Store ; il est exécuté sur MemoryStore et, avec le tag
// integration, sur MongoStore.
func runContract(t *testing.T, mk func(t *testing.T) Store) {
	ctx := context.Background()

	t.Run("InsertIdempotent", func(t *testing.T) {
		s := mk(t)
		b, created, err := s.Insert(ctx, sampleBet("txn_1", "cbet0001"))
		if err != nil || !created || b.State != StatePending || b.Version != 1 {
			t.Fatalf("insert: b=%+v created=%v err=%v", b, created, err)
		}
		again, created, err := s.Insert(ctx, sampleBet("txn_1", "cbet0001"))
		if err != nil || created || again.DebitTransactionID != "txn_1" {
			t.Fatalf("rejeu: created=%v err=%v", created, err)
		}
	})

	t.Run("InsertValidation", func(t *testing.T) {
		s := mk(t)
		bad := sampleBet("txn_v", "cbetv001")
		bad.AmountMinor = 0
		if _, _, err := s.Insert(ctx, bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("montant nul: err=%v", err)
		}
		bad = sampleBet("txn_v", "cbetv001")
		bad.Panel = 9
		if _, _, err := s.Insert(ctx, bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("panel invalide: err=%v", err)
		}
	})

	t.Run("OperatorIsolation", func(t *testing.T) {
		s := mk(t)
		if _, _, err := s.Insert(ctx, sampleBet("txn_2", "cbet0002")); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Get(ctx, "op2", "txn_2"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("get autre opérateur: err=%v", err)
		}
		if _, err := s.MarkLost(ctx, "op2", "txn_2", time.Now()); !errors.Is(err, ErrNotFound) {
			t.Fatalf("lost autre opérateur: err=%v", err)
		}
		cur, _ := s.Get(ctx, "op1", "txn_2")
		if cur.State != StatePending {
			t.Fatalf("état modifié par un autre opérateur: %s", cur.State)
		}
	})

	t.Run("CashoutIdempotentThenConflict", func(t *testing.T) {
		s := mk(t)
		s.Insert(ctx, sampleBet("txn_3", "cbet0003"))
		at := time.Now()
		b, err := s.MarkCashedOut(ctx, "op1", "txn_3", 2.5, 2500, at)
		if err != nil || b.State != StateCashedOut || b.PayoutMinor != 2500 {
			t.Fatalf("cashout: b=%+v err=%v", b, err)
		}
		if _, err := s.MarkCashedOut(ctx, "op1", "txn_3", 2.5, 2500, at); err != nil {
			t.Fatalf("rejeu identique: %v", err)
		}
		if _, err := s.MarkCashedOut(ctx, "op1", "txn_3", 3.0, 3000, at); !errors.Is(err, ErrStateConflict) {
			t.Fatalf("rejeu différent: err=%v", err)
		}
		if _, err := s.MarkLost(ctx, "op1", "txn_3", at); !errors.Is(err, ErrStateConflict) {
			t.Fatalf("lost après cashout: err=%v", err)
		}
	})

	t.Run("LostNeverCashedOut", func(t *testing.T) {
		s := mk(t)
		s.Insert(ctx, sampleBet("txn_4", "cbet0004"))
		if _, err := s.MarkLost(ctx, "op1", "txn_4", time.Now()); err != nil {
			t.Fatal(err)
		}
		if _, err := s.MarkLost(ctx, "op1", "txn_4", time.Now()); err != nil {
			t.Fatalf("lost rejoué: %v", err)
		}
		if _, err := s.MarkCashedOut(ctx, "op1", "txn_4", 2, 2000, time.Now()); !errors.Is(err, ErrStateConflict) {
			t.Fatalf("cashout après lost: err=%v", err)
		}
	})

	t.Run("ReconcilingBlocksAutomaticSettlement", func(t *testing.T) {
		s := mk(t)
		s.Insert(ctx, sampleBet("txn_5", "cbet0005"))
		if _, err := s.MarkReconciling(ctx, "op1", "txn_5", "round perdu"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.MarkCashedOut(ctx, "op1", "txn_5", 2, 2000, time.Now()); !errors.Is(err, ErrStateConflict) {
			t.Fatalf("cashout depuis RECONCILING: err=%v", err)
		}
		if _, err := s.MarkLost(ctx, "op1", "txn_5", time.Now()); !errors.Is(err, ErrStateConflict) {
			t.Fatalf("lost depuis RECONCILING: err=%v", err)
		}
	})

	t.Run("CreditRefSetOnce", func(t *testing.T) {
		s := mk(t)
		s.Insert(ctx, sampleBet("txn_6", "cbet0006"))
		if _, err := s.SetCreditRef(ctx, "op1", "txn_6", "txn_c"); !errors.Is(err, ErrStateConflict) {
			t.Fatalf("ref avant cashout: err=%v", err)
		}
		s.MarkCashedOut(ctx, "op1", "txn_6", 2, 2000, time.Now())
		if _, err := s.SetCreditRef(ctx, "op1", "txn_6", "txn_c"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SetCreditRef(ctx, "op1", "txn_6", "txn_c"); err != nil {
			t.Fatalf("ref rejouée: %v", err)
		}
		if _, err := s.SetCreditRef(ctx, "op1", "txn_6", "txn_autre"); !errors.Is(err, ErrStateConflict) {
			t.Fatalf("ref différente: err=%v", err)
		}
	})

	t.Run("ReconcileOrphans", func(t *testing.T) {
		s := mk(t)
		own := sampleBet("txn_own", "cbetown1")
		own.InstanceID = "inst-B"
		old := sampleBet("txn_old", "cbetold1")
		done := sampleBet("txn_done", "cbetdone")
		s.Insert(ctx, own)
		s.Insert(ctx, old)
		s.Insert(ctx, done)
		s.MarkLost(ctx, "op1", "txn_done", time.Now())

		if n, err := s.ReconcileOrphans(ctx, "inst-B", time.Now().Add(-time.Hour), "redémarrage"); err != nil || n != 0 {
			t.Fatalf("trop récents: n=%d err=%v", n, err)
		}
		n, err := s.ReconcileOrphans(ctx, "inst-B", time.Now().Add(time.Hour), "redémarrage")
		if err != nil || n != 1 {
			t.Fatalf("orphelins: n=%d err=%v", n, err)
		}
		if b, _ := s.Get(ctx, "op1", "txn_old"); b.State != StateReconciling || b.ReconcileReason == "" {
			t.Fatalf("orphelin: %+v", b)
		}
		if b, _ := s.Get(ctx, "op1", "txn_own"); b.State != StatePending {
			t.Fatalf("pari de l'instance courante touché: %s", b.State)
		}
		if b, _ := s.Get(ctx, "op1", "txn_done"); b.State != StateLost {
			t.Fatalf("pari terminal touché: %s", b.State)
		}
		list, err := s.ListByState(ctx, StateReconciling, 10)
		if err != nil || len(list) != 1 {
			t.Fatalf("liste RECONCILING: len=%d err=%v", len(list), err)
		}
	})

	t.Run("ConcurrentCashoutVsLost", func(t *testing.T) {
		s := mk(t)
		s.Insert(ctx, sampleBet("txn_race", "cbetrace"))
		var wg sync.WaitGroup
		var cashOK, lostOK int32
		for i := 0; i < 20; i++ {
			wg.Add(2)
			go func() {
				defer wg.Done()
				if _, err := s.MarkCashedOut(ctx, "op1", "txn_race", 2.5, 2500, time.Now()); err == nil {
					atomic.AddInt32(&cashOK, 1)
				}
			}()
			go func() {
				defer wg.Done()
				if _, err := s.MarkLost(ctx, "op1", "txn_race", time.Now()); err == nil {
					atomic.AddInt32(&lostOK, 1)
				}
			}()
		}
		wg.Wait()
		final, err := s.Get(ctx, "op1", "txn_race")
		if err != nil {
			t.Fatal(err)
		}
		switch final.State {
		case StateCashedOut:
			if lostOK != 0 || cashOK == 0 {
				t.Fatalf("CASHED_OUT mais cashOK=%d lostOK=%d", cashOK, lostOK)
			}
		case StateLost:
			if cashOK != 0 || lostOK == 0 {
				t.Fatalf("LOST mais cashOK=%d lostOK=%d", cashOK, lostOK)
			}
		default:
			t.Fatalf("état final inattendu: %s", final.State)
		}
	})
}
