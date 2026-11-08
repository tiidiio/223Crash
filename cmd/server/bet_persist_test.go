package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tiidiio/223Crash/internal/betstore"
	"github.com/tiidiio/223Gaming/provider/wallet"
)

func bpEnv(t *testing.T) (*rpEnv, *betstore.MemoryStore) {
	t.Helper()
	env := newRPEnv(t)
	st := betstore.NewMemory()
	env.gw.bets = st
	env.gw.instanceID = "inst-A"
	return env, st
}

func bpOnly(t *testing.T, st betstore.Store, state betstore.State) betstore.Bet {
	t.Helper()
	list, err := st.ListByState(context.Background(), state, 10)
	if err != nil || len(list) != 1 {
		t.Fatalf("paris %s: len=%d err=%v, attendu 1", state, len(list), err)
	}
	return list[0]
}

func bpRunning(t *testing.T, env *rpEnv, mult float64) {
	t.Helper()
	env.fe.closeBetting(t)
	env.fe.run(t)
	env.fe.setMult(mult)
}

func TestBetPersist_BetIsPersistedPending(t *testing.T) {
	env, st := bpEnv(t)
	id := gwtBetID(t)
	env.bet(id)
	env.expectUntil(t, "BET_ACCEPTED")

	b := bpOnly(t, st, betstore.StatePending)
	if b.OperatorID != "op1" || b.PlayerID != "player" || b.ClientBetID != id ||
		b.AmountMinor != 1000 || b.Panel != 1 || b.InstanceID != "inst-A" || b.DebitTransactionID == "" {
		t.Fatalf("document inattendu: %+v", b)
	}
}

func TestBetPersist_ReplayDoesNotDuplicate(t *testing.T) {
	env, st := bpEnv(t)
	id := gwtBetID(t)
	env.bet(id)
	env.expectUntil(t, "BET_ACCEPTED")
	env.bet(id)
	env.expectUntil(t, "BET_ACCEPTED")

	env.money(t, 1, 0, 0)
	bpOnly(t, st, betstore.StatePending)
	if n := len(env.myBets()); n != 1 {
		t.Fatalf("paris moteur = %d, attendu 1", n)
	}
}

func TestBetPersist_CashoutPersistsAndCreditsWithDebitTxnKey(t *testing.T) {
	env, st := bpEnv(t)
	env.bet(gwtBetID(t))
	env.expectUntil(t, "BET_ACCEPTED")
	bpRunning(t, env, 2.0)

	env.gw.handleCashout(env.c, clientMessage{Action: "cashout", Slot: "A"})
	env.expectUntil(t, "CASHOUT_ACCEPTED")

	b := bpOnly(t, st, betstore.StateCashedOut)
	if b.PayoutMinor != 2000 || b.CashoutMultiplier != 2.0 {
		t.Fatalf("encaissement: %+v", b)
	}
	credits := env.q.of(wallet.CallbackCredit)
	if len(credits) != 1 {
		t.Fatalf("CREDIT enfilés = %d, attendu 1", len(credits))
	}
	wantKey := "credit:op1:player:" + b.DebitTransactionID
	if credits[0].IdempotencyKey != wantKey || credits[0].BetID != b.DebitTransactionID {
		t.Fatalf("clé=%q betId=%q, attendu clé=%q", credits[0].IdempotencyKey, credits[0].BetID, wantKey)
	}
}

func TestBetPersist_CashoutBlockedByDurableStateNeverCredits(t *testing.T) {
	env, st := bpEnv(t)
	env.bet(gwtBetID(t))
	env.expectUntil(t, "BET_ACCEPTED")
	b := bpOnly(t, st, betstore.StatePending)
	if _, err := st.MarkReconciling(context.Background(), "op1", b.DebitTransactionID, "test"); err != nil {
		t.Fatal(err)
	}
	bpRunning(t, env, 2.0)

	env.gw.handleCashout(env.c, clientMessage{Action: "cashout", Slot: "A"})
	env.expectUntil(t, "CASHOUT_REJECTED")

	if n := len(env.q.of(wallet.CallbackCredit)); n != 0 {
		t.Fatalf("CREDIT enfilés = %d, attendu 0 (pari RECONCILING)", n)
	}
}

func TestBetPersist_CrashMarksBetLost(t *testing.T) {
	env, st := bpEnv(t)
	env.bet(gwtBetID(t))
	env.expectUntil(t, "BET_ACCEPTED")
	env.fe.closeBetting(t)
	env.fe.run(t)
	env.fe.crash(t)

	env.gw.notifyLostBets(env.myBets())

	gwtEventually(t, 3*time.Second, "pari LOST", func() bool {
		l, _ := st.ListByState(context.Background(), betstore.StateLost, 10)
		return len(l) == 1
	})
	if n := len(env.q.of(wallet.CallbackCredit)); n != 0 {
		t.Fatalf("CREDIT enfilés = %d, attendu 0", n)
	}
}

func TestBetPersist_ReplayFromOtherInstanceNeverPlacesBet(t *testing.T) {
	env, st := bpEnv(t)
	id := gwtBetID(t)
	env.bet(id)
	env.expectUntil(t, "BET_ACCEPTED")

	env.gw.instanceID = "inst-B"
	env.bet(id)
	env.expectUntil(t, "BET_REJECTED")

	b := bpOnly(t, st, betstore.StateReconciling)
	if b.ReconcileReason == "" {
		t.Fatalf("raison de réconciliation absente: %+v", b)
	}
	env.money(t, 1, 0, 0) // aucun remboursement automatique, aucun second débit
}

type bpFailInsert struct{ betstore.VoidStore }

func (bpFailInsert) Insert(context.Context, betstore.Bet) (*betstore.Bet, bool, error) {
	return nil, false, errors.New("mongo indisponible")
}

func TestBetPersist_InsertFailureOnFreshDebitRollsBackAndPlacesNoBet(t *testing.T) {
	env, st := bpEnv(t)
	env.gw.bets = bpFailInsert{VoidStore: st}

	env.bet(gwtBetID(t))
	env.expectUntil(t, "BET_REJECTED")

	env.money(t, 1, 0, 1)
	if n := len(env.myBets()); n != 0 {
		t.Fatalf("paris moteur = %d, attendu 0", n)
	}
}
