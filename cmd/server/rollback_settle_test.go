package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/tiidiio/223Gaming/provider/ledger"
	"github.com/tiidiio/223Gaming/provider/wallet"
)

type rbFakeLedger struct {
	mu           sync.Mutex
	entries      map[string]*ledger.Entry
	beginCalls   int
	beginErrs    []error
	confirmCalls int
	confirmErr   error
	lastNew      ledger.NewEntry
}

func newRBFakeLedger() *rbFakeLedger {
	return &rbFakeLedger{entries: make(map[string]*ledger.Entry)}
}

func rbKey(op ledger.OperatorID, k ledger.IdempotencyKey) string { return string(op) + "|" + string(k) }

func (f *rbFakeLedger) Begin(_ context.Context, e ledger.NewEntry) (*ledger.Entry, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.beginCalls++
	f.lastNew = e
	if len(f.beginErrs) > 0 {
		err := f.beginErrs[0]
		f.beginErrs = f.beginErrs[1:]
		if err != nil {
			return nil, false, err
		}
	}
	k := rbKey(e.OperatorID, e.IdempotencyKey)
	if ex, ok := f.entries[k]; ok {
		c := *ex
		return &c, false, nil
	}
	ne := &ledger.Entry{
		TransactionID:       ledger.TransactionID(fmt.Sprintf("txn_rb%d", len(f.entries)+1)),
		IdempotencyKey:      e.IdempotencyKey,
		GameID:              e.GameID,
		OperatorID:          e.OperatorID,
		PlayerID:            e.PlayerID,
		ExternalRef:         e.ExternalRef,
		ParentTransactionID: e.ParentTransactionID,
		Type:                e.Type,
		AmountMinor:         e.AmountMinor,
		Status:              ledger.StatusPending,
	}
	f.entries[k] = ne
	c := *ne
	return &c, true, nil
}

func (f *rbFakeLedger) Confirm(_ context.Context, txn ledger.TransactionID, _ *int64) (*ledger.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.confirmCalls++
	if f.confirmErr != nil {
		return nil, f.confirmErr
	}
	return &ledger.Entry{TransactionID: txn, Status: ledger.StatusConfirmed}, nil
}

type rbFakeQueue struct {
	mu  sync.Mutex
	cbs []wallet.Callback
	err error
}

func (q *rbFakeQueue) Enqueue(_ context.Context, cb wallet.Callback) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.err != nil {
		return q.err
	}
	q.cbs = append(q.cbs, cb)
	return nil
}

func rbDebit(status ledger.EntryStatus) *ledger.Entry {
	return &ledger.Entry{
		TransactionID:  "txn_debit1",
		IdempotencyKey: "debit:op1:p1:bet12345",
		GameID:         "223crash",
		OperatorID:     "op1",
		PlayerID:       "p1",
		ExternalRef:    "round=r1|panel=A|amount=1000|autoCashout=2.00",
		Type:           ledger.EntryDebit,
		AmountMinor:    1000,
		Status:         status,
	}
}

func rbSettler(l *rbFakeLedger, q *rbFakeQueue) *rollbackSettler {
	s := newRollbackSettler(l, q, "223crash")
	s.logf = func(string, ...any) {}
	s.sleep = func(time.Duration) {}
	s.spawn = func(f func()) { f() }
	return s
}

func rbReq(d *ledger.Entry, accepted bool) rollbackRequest {
	return rollbackRequest{Debit: d, RoundID: "r1", BetID: "bet12345", WalletAccepted: accepted}
}

func TestRollbackSettle_CreatedEnqueuesOnceWithTraceableEntry(t *testing.T) {
	l, q := newRBFakeLedger(), &rbFakeQueue{}
	d := rbDebit(ledger.StatusConfirmed)

	out := rbSettler(l, q).Settle(rbReq(d, true))

	if out != rollbackQueued {
		t.Fatalf("outcome = %d, veut rollbackQueued", out)
	}
	if len(q.cbs) != 1 {
		t.Fatalf("callbacks enfilés = %d, veut 1", len(q.cbs))
	}
	cb := q.cbs[0]
	if cb.TransactionID == "" || cb.TransactionID == d.TransactionID {
		t.Fatalf("TransactionID callback = %q, doit être celui de l'entrée ROLLBACK", cb.TransactionID)
	}
	if cb.Type != wallet.CallbackRollback || cb.Amount != 1000 {
		t.Fatalf("callback inattendu: %+v", cb)
	}
	if cb.IdempotencyKey != string(refundKey(d)) || cb.ID != "rollback-txn_debit1" {
		t.Fatalf("clé/ID instables: key=%q id=%q", cb.IdempotencyKey, cb.ID)
	}
	if l.lastNew.ParentTransactionID != d.TransactionID || l.lastNew.Type != ledger.EntryRollback {
		t.Fatalf("Begin mal paramétré: %+v", l.lastNew)
	}
}

func TestRollbackSettle_SecondCallNeverEnqueuesAgain(t *testing.T) {
	l, q := newRBFakeLedger(), &rbFakeQueue{}
	s := rbSettler(l, q)
	d := rbDebit(ledger.StatusConfirmed)

	s.Settle(rbReq(d, true))
	out := s.Settle(rbReq(d, true))

	if out != rollbackAlreadyKnown {
		t.Fatalf("outcome = %d, veut rollbackAlreadyKnown", out)
	}
	if len(q.cbs) != 1 {
		t.Fatalf("callbacks = %d, veut 1 (aucun second ROLLBACK)", len(q.cbs))
	}
}

func TestRollbackSettle_ExistingConfirmedNeverEnqueued(t *testing.T) {
	l, q := newRBFakeLedger(), &rbFakeQueue{}
	d := rbDebit(ledger.StatusConfirmed)
	l.entries[rbKey(d.OperatorID, refundKey(d))] = &ledger.Entry{
		TransactionID: "txn_rbold", Type: ledger.EntryRollback, ParentTransactionID: d.TransactionID,
		AmountMinor: 1000, OperatorID: "op1", PlayerID: "p1", Status: ledger.StatusConfirmed,
	}

	out := rbSettler(l, q).Settle(rbReq(d, true))

	if out != rollbackAlreadyKnown || len(q.cbs) != 0 {
		t.Fatalf("outcome=%d callbacks=%d, veut AlreadyKnown et 0", out, len(q.cbs))
	}
}

func TestRollbackSettle_ExistingWrongParentNeedsReview(t *testing.T) {
	l, q := newRBFakeLedger(), &rbFakeQueue{}
	d := rbDebit(ledger.StatusConfirmed)
	l.entries[rbKey(d.OperatorID, refundKey(d))] = &ledger.Entry{
		TransactionID: "txn_rbold", Type: ledger.EntryRollback, ParentTransactionID: "txn_autre",
		AmountMinor: 1000, OperatorID: "op1", PlayerID: "p1", Status: ledger.StatusPending,
	}

	out := rbSettler(l, q).Settle(rbReq(d, true))

	if out != rollbackNeedsReview || len(q.cbs) != 0 {
		t.Fatalf("outcome=%d callbacks=%d, veut NeedsReview et 0", out, len(q.cbs))
	}
}

func TestRollbackSettle_BeginTransientFailureSendsNothingThenRetriesOnce(t *testing.T) {
	l, q := newRBFakeLedger(), &rbFakeQueue{}
	l.beginErrs = []error{errors.New("mongo indisponible"), nil}

	out := rbSettler(l, q).Settle(rbReq(rbDebit(ledger.StatusConfirmed), true))

	if out != rollbackDeferred {
		t.Fatalf("outcome = %d, veut rollbackDeferred", out)
	}
	if l.beginCalls != 2 {
		t.Fatalf("Begin appelé %d fois, veut 2 (échec puis reprise)", l.beginCalls)
	}
	if len(q.cbs) != 1 || q.cbs[0].TransactionID == "" {
		t.Fatalf("après reprise: callbacks=%+v, veut 1 callback avec TransactionID", q.cbs)
	}
}

func TestRollbackSettle_BeginAlwaysFailingNeverEnqueues(t *testing.T) {
	l, q := newRBFakeLedger(), &rbFakeQueue{}
	l.beginErrs = []error{errors.New("down"), errors.New("down"), errors.New("down")}
	s := rbSettler(l, q)
	s.bgMaxAttempts = 2

	out := s.Settle(rbReq(rbDebit(ledger.StatusConfirmed), true))

	if out != rollbackDeferred || len(q.cbs) != 0 {
		t.Fatalf("outcome=%d callbacks=%d, veut Deferred et 0 (aucun enqueue non traçable)", out, len(q.cbs))
	}
}

func TestRollbackSettle_PermanentBeginErrorNeedsReview(t *testing.T) {
	l, q := newRBFakeLedger(), &rbFakeQueue{}
	l.beginErrs = []error{fmt.Errorf("x: %w", ledger.ErrInvalidAmount)}

	out := rbSettler(l, q).Settle(rbReq(rbDebit(ledger.StatusConfirmed), true))

	if out != rollbackNeedsReview || len(q.cbs) != 0 || l.beginCalls != 1 {
		t.Fatalf("outcome=%d callbacks=%d begins=%d", out, len(q.cbs), l.beginCalls)
	}
}

func TestRollbackSettle_EnqueueFailureLeavesPendingEntry(t *testing.T) {
	l, q := newRBFakeLedger(), &rbFakeQueue{err: errors.New("queue down")}

	out := rbSettler(l, q).Settle(rbReq(rbDebit(ledger.StatusConfirmed), true))

	if out != rollbackRecoverable {
		t.Fatalf("outcome = %d, veut rollbackRecoverable", out)
	}
	if len(l.entries) != 1 {
		t.Fatalf("entrées ledger = %d, veut 1 (PENDING pour le Recovery)", len(l.entries))
	}
}

func TestRollbackSettle_UnconfirmedDebitWithoutWalletProofNeedsReview(t *testing.T) {
	l, q := newRBFakeLedger(), &rbFakeQueue{}

	out := rbSettler(l, q).Settle(rbReq(rbDebit(ledger.StatusPending), false))

	if out != rollbackNeedsReview || l.beginCalls != 0 || l.confirmCalls != 0 || len(q.cbs) != 0 {
		t.Fatalf("outcome=%d begins=%d confirms=%d callbacks=%d, veut aucune action", out, l.beginCalls, l.confirmCalls, len(q.cbs))
	}
}

func TestRollbackSettle_FailedDebitNeedsReview(t *testing.T) {
	l, q := newRBFakeLedger(), &rbFakeQueue{}

	out := rbSettler(l, q).Settle(rbReq(rbDebit(ledger.StatusFailed), true))

	if out != rollbackNeedsReview || l.beginCalls != 0 || len(q.cbs) != 0 {
		t.Fatalf("outcome=%d begins=%d callbacks=%d", out, l.beginCalls, len(q.cbs))
	}
}

func TestRollbackSettle_PendingDebitWithWalletProofIsConfirmedThenRolledBack(t *testing.T) {
	l, q := newRBFakeLedger(), &rbFakeQueue{}

	out := rbSettler(l, q).Settle(rbReq(rbDebit(ledger.StatusPending), true))

	if out != rollbackQueued || l.confirmCalls != 1 || len(q.cbs) != 1 {
		t.Fatalf("outcome=%d confirms=%d callbacks=%d", out, l.confirmCalls, len(q.cbs))
	}
}

func TestRollbackSettle_ConfirmRepairConflictNeverRollsBack(t *testing.T) {
	l, q := newRBFakeLedger(), &rbFakeQueue{}
	l.confirmErr = fmt.Errorf("x: %w", ledger.ErrStateConflict)

	out := rbSettler(l, q).Settle(rbReq(rbDebit(ledger.StatusPending), true))

	if out != rollbackNeedsReview || l.beginCalls != 0 || len(q.cbs) != 0 {
		t.Fatalf("outcome=%d begins=%d callbacks=%d", out, l.beginCalls, len(q.cbs))
	}
}

func TestRollbackSettle_NilAndInvalidDebitNeedsReview(t *testing.T) {
	l, q := newRBFakeLedger(), &rbFakeQueue{}
	s := rbSettler(l, q)

	if out := s.Settle(rbReq(nil, true)); out != rollbackNeedsReview {
		t.Fatalf("débit nil: outcome = %d", out)
	}
	credit := rbDebit(ledger.StatusConfirmed)
	credit.Type = ledger.EntryCredit
	if out := s.Settle(rbReq(credit, true)); out != rollbackNeedsReview {
		t.Fatalf("parent non DEBIT: outcome = %d", out)
	}
	if l.beginCalls != 0 || len(q.cbs) != 0 {
		t.Fatalf("begins=%d callbacks=%d, veut 0", l.beginCalls, len(q.cbs))
	}
}

func TestRollbackSettle_ConfirmInBackgroundRetriesUntilSuccess(t *testing.T) {
	l := newRBFakeLedger()
	l.confirmErr = errors.New("mongo down")
	s := rbSettler(l, &rbFakeQueue{})
	s.bgMaxAttempts = 3

	s.confirmInBackground("txn_debit1", nil)

	if l.confirmCalls != 3 {
		t.Fatalf("Confirm appelé %d fois, veut 3 (reprises bornées)", l.confirmCalls)
	}
}
