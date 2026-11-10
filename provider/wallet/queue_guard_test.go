package wallet

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tiidiio/223Gaming/provider/ledger"
)

type qgStore struct {
	dead    []Callback
	removed []string
	retries []Callback
}

func (s *qgStore) insert(context.Context, Callback) error { return nil }
func (s *qgStore) lease(context.Context, time.Time, time.Duration) (Callback, error) {
	return Callback{}, errNoneDue
}
func (s *qgStore) remove(_ context.Context, cb Callback) error {
	s.removed = append(s.removed, cb.ID)
	return nil
}
func (s *qgStore) scheduleRetry(_ context.Context, cb Callback, _ time.Time) error {
	s.retries = append(s.retries, cb)
	return nil
}
func (s *qgStore) deadLetter(_ context.Context, cb Callback) error {
	s.dead = append(s.dead, cb)
	return nil
}

type qgSender struct{ calls int }

func (s *qgSender) Send(context.Context, Callback) (int64, Outcome, error) {
	s.calls++
	return 100, OutcomeAccepted, nil
}

// qgNoReader n'implémente PAS GetByTransactionID : il simule un resolver minimal.
type qgNoReader struct{ confirms int }

func (r *qgNoReader) Confirm(context.Context, ledger.TransactionID, *int64) (*ledger.Entry, error) {
	r.confirms++
	return &ledger.Entry{}, nil
}
func (r *qgNoReader) Fail(context.Context, ledger.TransactionID, string) (*ledger.Entry, error) {
	return &ledger.Entry{}, nil
}

type qgLedger struct {
	entries  map[ledger.TransactionID]*ledger.Entry
	getErr   error
	confirms int
	fails    int
}

func (l *qgLedger) Confirm(_ context.Context, id ledger.TransactionID, _ *int64) (*ledger.Entry, error) {
	l.confirms++
	return &ledger.Entry{TransactionID: id, Status: ledger.StatusConfirmed}, nil
}
func (l *qgLedger) Fail(_ context.Context, id ledger.TransactionID, _ string) (*ledger.Entry, error) {
	l.fails++
	return &ledger.Entry{TransactionID: id, Status: ledger.StatusFailed}, nil
}
func (l *qgLedger) GetByTransactionID(_ context.Context, id ledger.TransactionID) (*ledger.Entry, error) {
	if l.getErr != nil {
		return nil, l.getErr
	}
	e, ok := l.entries[id]
	if !ok {
		return nil, ledger.ErrEntryNotFound
	}
	c := *e
	return &c, nil
}

func qgCreditCB() Callback {
	return Callback{
		ID: "credit-op1-p1-b1", Type: CallbackCredit, TransactionID: "txn_c1",
		OperatorID: "op1", PlayerID: "p1", Amount: 500, IdempotencyKey: "credit:op1:p1:b1",
	}
}

func qgCreditEntry(st ledger.EntryStatus) *ledger.Entry {
	return &ledger.Entry{
		TransactionID: "txn_c1", Type: ledger.EntryCredit, OperatorID: "op1", PlayerID: "p1",
		AmountMinor: 500, IdempotencyKey: "credit:op1:p1:b1", Status: st,
	}
}

func qgRollbackCB() Callback {
	return Callback{
		ID: "rollback-txn_d1", Type: CallbackRollback, TransactionID: "txn_r1",
		OperatorID: "op1", PlayerID: "p1", Amount: 1000, IdempotencyKey: "rollback:op1:p1:txn_d1",
	}
}

func qgRollbackEntry(st ledger.EntryStatus) *ledger.Entry {
	return &ledger.Entry{
		TransactionID: "txn_r1", Type: ledger.EntryRollback, ParentTransactionID: "txn_d1",
		OperatorID: "op1", PlayerID: "p1", AmountMinor: 1000,
		IdempotencyKey: "rollback:op1:p1:txn_d1", Status: st,
	}
}

func qgParent(st ledger.EntryStatus) *ledger.Entry {
	return &ledger.Entry{
		TransactionID: "txn_d1", Type: ledger.EntryDebit, OperatorID: "op1", PlayerID: "p1",
		AmountMinor: 1000, Status: st,
	}
}

func qgRun(cb Callback, entries ...*ledger.Entry) (*qgSender, *qgLedger, *qgStore) {
	led := &qgLedger{entries: map[ledger.TransactionID]*ledger.Entry{}}
	for _, e := range entries {
		led.entries[e.TransactionID] = e
	}
	snd, st := &qgSender{}, &qgStore{}
	q := &Queue{client: snd, ledger: led, st: st}
	q.process(context.Background(), cb)
	return snd, led, st
}

func TestGuard_MissingTransactionIDDeadLettersWithoutSend(t *testing.T) {
	cb := qgCreditCB()
	cb.TransactionID = ""
	snd, _, st := qgRun(cb)
	if snd.calls != 0 || len(st.dead) != 1 || len(st.removed) != 1 {
		t.Fatalf("send=%d dead=%d removed=%d, veut 0/1/1", snd.calls, len(st.dead), len(st.removed))
	}
}

func TestGuard_MissingTransactionIDBlockedEvenWithoutReader(t *testing.T) {
	cb := qgCreditCB()
	cb.TransactionID = ""
	snd, st := &qgSender{}, &qgStore{}
	q := &Queue{client: snd, ledger: &qgNoReader{}, st: st}
	q.process(context.Background(), cb)
	if snd.calls != 0 || len(st.dead) != 1 {
		t.Fatalf("send=%d dead=%d, veut 0/1", snd.calls, len(st.dead))
	}
}

func TestGuard_ResolverWithoutReaderStillSends(t *testing.T) {
	snd, st, r := &qgSender{}, &qgStore{}, &qgNoReader{}
	q := &Queue{client: snd, ledger: r, st: st}
	q.process(context.Background(), qgCreditCB())
	if snd.calls != 1 || r.confirms != 1 || len(st.removed) != 1 {
		t.Fatalf("send=%d confirms=%d removed=%d, veut 1/1/1", snd.calls, r.confirms, len(st.removed))
	}
}

func TestGuard_AlreadyConfirmedIsRemovedNeverResent(t *testing.T) {
	snd, led, st := qgRun(qgCreditCB(), qgCreditEntry(ledger.StatusConfirmed))
	if snd.calls != 0 || led.confirms != 0 || len(st.removed) != 1 || len(st.dead) != 0 {
		t.Fatalf("send=%d confirms=%d removed=%d dead=%d", snd.calls, led.confirms, len(st.removed), len(st.dead))
	}
}

func TestGuard_FailedEntryDeadLettersWithoutSend(t *testing.T) {
	snd, _, st := qgRun(qgCreditCB(), qgCreditEntry(ledger.StatusFailed))
	if snd.calls != 0 || len(st.dead) != 1 {
		t.Fatalf("send=%d dead=%d", snd.calls, len(st.dead))
	}
}

func TestGuard_MissingEntryDeadLettersWithoutSend(t *testing.T) {
	snd, _, st := qgRun(qgCreditCB())
	if snd.calls != 0 || len(st.dead) != 1 {
		t.Fatalf("send=%d dead=%d", snd.calls, len(st.dead))
	}
}

func TestGuard_AmountMismatchDeadLettersWithoutSend(t *testing.T) {
	e := qgCreditEntry(ledger.StatusPending)
	e.AmountMinor = 999
	snd, _, st := qgRun(qgCreditCB(), e)
	if snd.calls != 0 || len(st.dead) != 1 {
		t.Fatalf("send=%d dead=%d", snd.calls, len(st.dead))
	}
}

func TestGuard_TransientLedgerReadRetriesWithoutSend(t *testing.T) {
	led := &qgLedger{getErr: errors.New("mongo down")}
	snd, st := &qgSender{}, &qgStore{}
	q := &Queue{client: snd, ledger: led, st: st}
	q.process(context.Background(), qgCreditCB())
	if snd.calls != 0 || len(st.retries) != 1 || len(st.dead) != 0 {
		t.Fatalf("send=%d retries=%d dead=%d, veut 0/1/0", snd.calls, len(st.retries), len(st.dead))
	}
}

func TestGuard_RollbackWithPendingParentRetriesNeverSendsNorFails(t *testing.T) {
	snd, led, st := qgRun(qgRollbackCB(), qgRollbackEntry(ledger.StatusPending), qgParent(ledger.StatusPending))
	if snd.calls != 0 || led.fails != 0 || led.confirms != 0 || len(st.retries) != 1 || len(st.dead) != 0 {
		t.Fatalf("send=%d fails=%d confirms=%d retries=%d dead=%d", snd.calls, led.fails, led.confirms, len(st.retries), len(st.dead))
	}
}

func TestGuard_RollbackWithFailedParentDoesNotSend(t *testing.T) {
	snd, _, _ := qgRun(qgRollbackCB(), qgRollbackEntry(ledger.StatusPending), qgParent(ledger.StatusFailed))
	if snd.calls != 0 {
		t.Fatalf("send=%d, un parent FAILED ne doit jamais déclencher un remboursement", snd.calls)
	}
}

func TestGuard_RollbackMissingParentDeadLetters(t *testing.T) {
	snd, _, st := qgRun(qgRollbackCB(), qgRollbackEntry(ledger.StatusPending))
	if snd.calls != 0 || len(st.dead) != 1 {
		t.Fatalf("send=%d dead=%d", snd.calls, len(st.dead))
	}
}

func TestGuard_RollbackWithoutParentReferenceDeadLetters(t *testing.T) {
	e := qgRollbackEntry(ledger.StatusPending)
	e.ParentTransactionID = ""
	snd, _, st := qgRun(qgRollbackCB(), e)
	if snd.calls != 0 || len(st.dead) != 1 {
		t.Fatalf("send=%d dead=%d", snd.calls, len(st.dead))
	}
}

func TestGuard_RollbackParentAmountMismatchDeadLetters(t *testing.T) {
	p := qgParent(ledger.StatusConfirmed)
	p.AmountMinor = 2000
	snd, _, st := qgRun(qgRollbackCB(), qgRollbackEntry(ledger.StatusPending), p)
	if snd.calls != 0 || len(st.dead) != 1 {
		t.Fatalf("send=%d dead=%d", snd.calls, len(st.dead))
	}
}

func TestGuard_RollbackWithConfirmedParentIsSentAndConfirmed(t *testing.T) {
	snd, led, st := qgRun(qgRollbackCB(), qgRollbackEntry(ledger.StatusPending), qgParent(ledger.StatusConfirmed))
	if snd.calls != 1 || led.confirms != 1 || len(st.removed) != 1 || len(st.dead) != 0 {
		t.Fatalf("send=%d confirms=%d removed=%d dead=%d, veut 1/1/1/0", snd.calls, led.confirms, len(st.removed), len(st.dead))
	}
}

func TestGuard_DebitTypeNotAllowedInQueue(t *testing.T) {
	cb := qgCreditCB()
	cb.Type = CallbackDebit
	snd, _, st := qgRun(cb, qgCreditEntry(ledger.StatusPending))
	if snd.calls != 0 || len(st.dead) != 1 {
		t.Fatalf("send=%d dead=%d", snd.calls, len(st.dead))
	}
}
