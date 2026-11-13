package recovery

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/tiidiio/223Crash/provider/ledger"
	"github.com/tiidiio/223Crash/provider/wallet"
)

var base = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

type idempotencyScope struct {
	operatorID ledger.OperatorID
	key        ledger.IdempotencyKey
}

type fakeStore struct {
	byTxn map[ledger.TransactionID]*ledger.Entry
	byKey map[idempotencyScope]*ledger.Entry
	seq   int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		byTxn: make(map[ledger.TransactionID]*ledger.Entry),
		byKey: make(map[idempotencyScope]*ledger.Entry),
	}
}

func scopeOf(operatorID ledger.OperatorID, key ledger.IdempotencyKey) idempotencyScope {
	return idempotencyScope{operatorID: operatorID, key: key}
}

func (f *fakeStore) add(e ledger.Entry) {
	c := e
	f.byTxn[c.TransactionID] = &c
	f.byKey[scopeOf(c.OperatorID, c.IdempotencyKey)] = &c
}

func (f *fakeStore) ListPendingOlderThan(_ context.Context, before time.Time) ([]ledger.Entry, error) {
	var out []ledger.Entry
	for _, e := range f.byTxn {
		if e.Status == ledger.StatusPending && e.CreatedAt.Before(before) {
			out = append(out, *e)
		}
	}
	return out, nil
}

func (f *fakeStore) GetByIdempotencyKey(
	_ context.Context,
	operatorID ledger.OperatorID,
	k ledger.IdempotencyKey,
) (*ledger.Entry, error) {
	if e, ok := f.byKey[scopeOf(operatorID, k)]; ok {
		c := *e
		return &c, nil
	}
	return nil, ledger.ErrEntryNotFound
}

func (f *fakeStore) GetByTransactionID(_ context.Context, id ledger.TransactionID) (*ledger.Entry, error) {
	if e, ok := f.byTxn[id]; ok {
		c := *e
		return &c, nil
	}
	return nil, ledger.ErrEntryNotFound
}

func (f *fakeStore) Begin(_ context.Context, n ledger.NewEntry) (*ledger.Entry, bool, error) {
	scope := scopeOf(n.OperatorID, n.IdempotencyKey)
	if e, ok := f.byKey[scope]; ok {
		c := *e
		return &c, false, nil
	}

	f.seq++
	e := ledger.Entry{
		TransactionID:       ledger.TransactionID(fmt.Sprintf("txn_auto%d", f.seq)),
		IdempotencyKey:      n.IdempotencyKey,
		GameID:              n.GameID,
		OperatorID:          n.OperatorID,
		PlayerID:            n.PlayerID,
		ExternalRef:         n.ExternalRef,
		ParentTransactionID: n.ParentTransactionID,
		Type:                n.Type,
		AmountMinor:         n.AmountMinor,
		Status:              ledger.StatusPending,
		CreatedAt:           base,
	}
	f.add(e)
	c := e
	return &c, true, nil
}

func (f *fakeStore) transition(id ledger.TransactionID, to ledger.EntryStatus) (*ledger.Entry, error) {
	e, ok := f.byTxn[id]
	if !ok {
		return nil, ledger.ErrEntryNotFound
	}
	if e.Status == ledger.StatusPending {
		e.Status = to
	} else if e.Status != to {
		return nil, ledger.ErrStateConflict
	}
	c := *e
	return &c, nil
}

func (f *fakeStore) Confirm(_ context.Context, id ledger.TransactionID, _ *int64) (*ledger.Entry, error) {
	return f.transition(id, ledger.StatusConfirmed)
}

func (f *fakeStore) Fail(_ context.Context, id ledger.TransactionID, _ string) (*ledger.Entry, error) {
	return f.transition(id, ledger.StatusFailed)
}

type fakeSender struct {
	calls []wallet.Callback
	fn    func(cb wallet.Callback) (int64, wallet.Outcome, error)
}

func (s *fakeSender) Send(_ context.Context, cb wallet.Callback) (int64, wallet.Outcome, error) {
	s.calls = append(s.calls, cb)
	return s.fn(cb)
}

func always(o wallet.Outcome) func(wallet.Callback) (int64, wallet.Outcome, error) {
	return func(wallet.Callback) (int64, wallet.Outcome, error) {
		if o == wallet.OutcomeAccepted {
			return 5000, o, nil
		}
		return 0, o, fmt.Errorf("scripté")
	}
}

func debitE(txn string, created time.Time) ledger.Entry {
	return ledger.Entry{
		TransactionID:  ledger.TransactionID(txn),
		IdempotencyKey: "debit:opA:p1:b1",
		GameID:         "crash",
		OperatorID:     "opA",
		PlayerID:       "p1",
		ExternalRef:    "round=r1|panel=A|amount=1000|autoCashout=0.00",
		Type:           ledger.EntryDebit,
		AmountMinor:    1000,
		Status:         ledger.StatusPending,
		CreatedAt:      created,
	}
}

func markerE(created time.Time) ledger.Entry {
	return ledger.Entry{
		TransactionID:       "txn_r1",
		IdempotencyKey:      "rollback:opA:p1:txn_d1",
		GameID:              "crash",
		OperatorID:          "opA",
		PlayerID:            "p1",
		ExternalRef:         "round=r1|panel=A|amount=1000|autoCashout=0.00",
		ParentTransactionID: "txn_d1",
		Type:                ledger.EntryRollback,
		AmountMinor:         1000,
		Status:              ledger.StatusPending,
		CreatedAt:           created,
	}
}

func newSweeper(st *fakeStore, sd *fakeSender, startedAt time.Time) *Sweeper {
	s := New(st, sd)
	s.now = func() time.Time { return base }
	s.startedAt = startedAt
	return s
}

func status(t *testing.T, st *fakeStore, id string) ledger.EntryStatus {
	t.Helper()
	e, ok := st.byTxn[ledger.TransactionID(id)]
	if !ok {
		t.Fatalf("entrée %s absente", id)
	}
	return e.Status
}

func TestFakeStoreIdempotencyIsScopedByOperator(t *testing.T) {
	st := newFakeStore()

	first := ledger.NewEntry{
		GameID:         "crash",
		OperatorID:     "opA",
		PlayerID:       "p1",
		IdempotencyKey: "same-key",
		Type:           ledger.EntryDebit,
		AmountMinor:    100,
	}
	second := first
	second.OperatorID = "opB"
	second.PlayerID = "p2"

	a, createdA, err := st.Begin(context.Background(), first)
	if err != nil || !createdA {
		t.Fatalf("première entrée: entry=%+v created=%v err=%v", a, createdA, err)
	}
	b, createdB, err := st.Begin(context.Background(), second)
	if err != nil || !createdB {
		t.Fatalf("seconde entrée: entry=%+v created=%v err=%v", b, createdB, err)
	}
	if a.TransactionID == b.TransactionID {
		t.Fatal("deux opérateurs distincts ne doivent pas partager le même transactionId")
	}

	retryA, createdRetryA, err := st.Begin(context.Background(), first)
	if err != nil || createdRetryA || retryA.TransactionID != a.TransactionID {
		t.Fatalf("retry opA incorrect: entry=%+v created=%v err=%v", retryA, createdRetryA, err)
	}
	retryB, createdRetryB, err := st.Begin(context.Background(), second)
	if err != nil || createdRetryB || retryB.TransactionID != b.TransactionID {
		t.Fatalf("retry opB incorrect: entry=%+v created=%v err=%v", retryB, createdRetryB, err)
	}
}

func TestMarkerDebitAcceptedTriggersRollback(t *testing.T) {
	st, sd := newFakeStore(), &fakeSender{fn: always(wallet.OutcomeAccepted)}
	st.add(debitE("txn_d1", base.Add(-2*time.Minute)))
	st.add(markerE(base.Add(-2 * time.Minute)))
	s := newSweeper(st, sd, base.Add(-time.Hour))

	s.RunOnce(context.Background())

	if status(t, st, "txn_d1") != ledger.StatusConfirmed || status(t, st, "txn_r1") != ledger.StatusConfirmed {
		t.Fatal("debit et rollback doivent être CONFIRMED")
	}
	if len(sd.calls) != 2 {
		t.Fatalf("attendu 2 appels wallet, obtenu %d", len(sd.calls))
	}

	d, r := sd.calls[0], sd.calls[1]
	if d.Type != wallet.CallbackDebit || d.IdempotencyKey != "debit:opA:p1:b1" || d.BetID != "b1" || d.RoundID != "r1" {
		t.Fatalf("callback debit inattendu: %+v", d)
	}
	if r.Type != wallet.CallbackRollback || r.IdempotencyKey != "rollback:opA:p1:txn_d1" ||
		r.ID != "rollback-txn_d1" || r.BetID != "b1" || r.Amount != 1000 {
		t.Fatalf("callback rollback inattendu: %+v", r)
	}

	s.RunOnce(context.Background()) // idempotence : aucun nouvel appel
	if len(sd.calls) != 2 {
		t.Fatalf("second passage ne doit rien renvoyer, appels=%d", len(sd.calls))
	}
}

func TestDebitRejectedFailsDebitAndMarker(t *testing.T) {
	st, sd := newFakeStore(), &fakeSender{fn: always(wallet.OutcomeRejected)}
	st.add(debitE("txn_d1", base.Add(-2*time.Minute)))
	st.add(markerE(base.Add(-2 * time.Minute)))
	newSweeper(st, sd, base.Add(-time.Hour)).RunOnce(context.Background())

	if status(t, st, "txn_d1") != ledger.StatusFailed || status(t, st, "txn_r1") != ledger.StatusFailed {
		t.Fatal("debit et marqueur doivent être FAILED")
	}
	if len(sd.calls) != 1 {
		t.Fatalf("un seul appel attendu, obtenu %d", len(sd.calls))
	}
}

func TestNoMarkerSameProcessNoRefund(t *testing.T) {
	st, sd := newFakeStore(), &fakeSender{fn: always(wallet.OutcomeAccepted)}
	st.add(debitE("txn_d1", base.Add(-2*time.Minute)))
	newSweeper(st, sd, base.Add(-time.Hour)).RunOnce(context.Background())

	if status(t, st, "txn_d1") != ledger.StatusConfirmed {
		t.Fatal("debit doit être CONFIRMED")
	}
	if len(sd.calls) != 1 || len(st.byKey) != 1 {
		t.Fatalf("aucun rollback attendu: appels=%d entrées=%d", len(sd.calls), len(st.byKey))
	}
}

func TestNoMarkerCrashRecoveryRefunds(t *testing.T) {
	st, sd := newFakeStore(), &fakeSender{fn: always(wallet.OutcomeAccepted)}
	st.add(debitE("txn_d1", base.Add(-2*time.Minute)))
	newSweeper(st, sd, base.Add(-time.Minute)).RunOnce(context.Background()) // entrée antérieure au démarrage

	rb, ok := st.byKey[scopeOf("opA", "rollback:opA:p1:txn_d1")]
	if !ok || rb.Status != ledger.StatusConfirmed || rb.ParentTransactionID != "txn_d1" {
		t.Fatalf("rollback attendu CONFIRMED lié au débit: %+v", rb)
	}
	if len(sd.calls) != 2 {
		t.Fatalf("attendu 2 appels, obtenu %d", len(sd.calls))
	}
}

func TestUnknownLeavesEverythingPending(t *testing.T) {
	st, sd := newFakeStore(), &fakeSender{fn: always(wallet.OutcomeUnknown)}
	st.add(debitE("txn_d1", base.Add(-2*time.Minute)))
	newSweeper(st, sd, base.Add(-time.Minute)).RunOnce(context.Background())

	if status(t, st, "txn_d1") != ledger.StatusPending || len(st.byKey) != 1 {
		t.Fatal("un résultat indéterminé ne doit rien résoudre ni créer")
	}
}

func TestYoungEntriesIgnored(t *testing.T) {
	st, sd := newFakeStore(), &fakeSender{fn: always(wallet.OutcomeAccepted)}
	st.add(debitE("txn_d1", base.Add(-10*time.Second)))
	newSweeper(st, sd, base.Add(-time.Hour)).RunOnce(context.Background())
	if len(sd.calls) != 0 {
		t.Fatal("une entrée récente ne doit pas être touchée")
	}
}

func TestOrphanCreditResent(t *testing.T) {
	st, sd := newFakeStore(), &fakeSender{fn: always(wallet.OutcomeAccepted)}
	st.add(ledger.Entry{
		TransactionID: "txn_c1", IdempotencyKey: "credit:opA:p1:b9", GameID: "crash", OperatorID: "opA", PlayerID: "p1",
		ExternalRef: "round=r1|bet=b9", Type: ledger.EntryCredit, AmountMinor: 2500,
		Status: ledger.StatusPending, CreatedAt: base.Add(-10 * time.Minute),
	})
	st.add(ledger.Entry{
		TransactionID: "txn_c2", IdempotencyKey: "credit:opA:p1:b10", GameID: "crash", OperatorID: "opA", PlayerID: "p1",
		ExternalRef: "round=r1|bet=b10", Type: ledger.EntryCredit, AmountMinor: 100,
		Status: ledger.StatusPending, CreatedAt: base.Add(-time.Minute),
	})
	newSweeper(st, sd, base.Add(-time.Hour)).RunOnce(context.Background())

	if status(t, st, "txn_c1") != ledger.StatusConfirmed || status(t, st, "txn_c2") != ledger.StatusPending {
		t.Fatal("seul le crédit ancien doit être repris")
	}
	c := sd.calls[0]
	if len(sd.calls) != 1 || c.Type != wallet.CallbackCredit || c.ID != "credit-opA-p1-b9" ||
		c.RoundID != "r1" || c.BetID != "b9" || c.Amount != 2500 {
		t.Fatalf("callback crédit inattendu: %+v", c)
	}
}

func TestRollbackWithFailedParentIsClosedWithoutSend(t *testing.T) {
	st, sd := newFakeStore(), &fakeSender{fn: always(wallet.OutcomeAccepted)}
	d := debitE("txn_d1", base.Add(-10*time.Minute))
	d.Status = ledger.StatusFailed
	st.add(d)
	st.add(markerE(base.Add(-10 * time.Minute)))
	newSweeper(st, sd, base.Add(-time.Hour)).RunOnce(context.Background())

	if status(t, st, "txn_r1") != ledger.StatusFailed || len(sd.calls) != 0 {
		t.Fatal("marqueur orphelin : doit passer FAILED sans appel wallet")
	}
}

func TestRollbackCallbackRejectsDifferentOperatorOrPlayer(t *testing.T) {
	parent := debitE("txn_d1", base.Add(-2*time.Minute))
	rollback := markerE(base.Add(-2 * time.Minute))

	rollback.OperatorID = "opB"
	if _, ok := rollbackCallback(rollback, &parent); ok {
		t.Fatal("un rollback d'un autre opérateur ne doit pas produire de callback")
	}

	rollback = markerE(base.Add(-2 * time.Minute))
	rollback.PlayerID = "p2"
	if _, ok := rollbackCallback(rollback, &parent); ok {
		t.Fatal("un rollback d'un autre joueur ne doit pas produire de callback")
	}
}
