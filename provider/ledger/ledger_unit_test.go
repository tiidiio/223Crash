package ledger

// Tests unitaires du Ledger — AUCUNE dépendance à MongoDB. Ils valident la logique métier
// (validation, idempotency, machine d'états, traduction d'erreurs) au travers d'un faux store.
//
// Ils ne prouvent PAS : l'unicité réelle de l'index composé idempotence, l'atomicité de
// FindOneAndUpdate, ni le comportement des curseurs. Ces propriétés restent couvertes par les
// tests d'intégration (MONGO_TEST_URI) de ledger_test.go.

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"
)

var utErrInfra = errors.New("infra: boom")

// utStore : chaque méthode délègue à son champ fonction ; une méthode non configurée
// appelée par le code testé fait échouer le test (aucun appel store inattendu).
type utStore struct {
	t *testing.T

	insertFn   func(context.Context, Entry) error
	byKeyFn    func(context.Context, OperatorID, IdempotencyKey) (*Entry, error)
	byTxnFn    func(context.Context, TransactionID) (*Entry, error)
	resolveFn  func(context.Context, TransactionID, transitionSpec) (*Entry, error)
	pendingFn  func(context.Context, time.Time) ([]Entry, error)
	ensureFn   func(context.Context, indexDef) error
	insertSeen []Entry
}

func (f *utStore) insert(ctx context.Context, e Entry) error {
	f.t.Helper()
	f.insertSeen = append(f.insertSeen, e)
	if f.insertFn == nil {
		f.t.Fatal("insert inattendu")
	}
	return f.insertFn(ctx, e)
}

func (f *utStore) byIdempotencyKey(ctx context.Context, operatorID OperatorID, k IdempotencyKey) (*Entry, error) {
	f.t.Helper()
	if f.byKeyFn == nil {
		f.t.Fatal("byIdempotencyKey inattendu")
	}
	return f.byKeyFn(ctx, operatorID, k)
}

func (f *utStore) byTransactionID(ctx context.Context, id TransactionID) (*Entry, error) {
	f.t.Helper()
	if f.byTxnFn == nil {
		f.t.Fatal("byTransactionID inattendu")
	}
	return f.byTxnFn(ctx, id)
}

func (f *utStore) resolvePending(ctx context.Context, id TransactionID, t transitionSpec) (*Entry, error) {
	f.t.Helper()
	if f.resolveFn == nil {
		f.t.Fatal("resolvePending inattendu")
	}
	return f.resolveFn(ctx, id, t)
}

func (f *utStore) pendingBefore(ctx context.Context, before time.Time) ([]Entry, error) {
	f.t.Helper()
	if f.pendingFn == nil {
		f.t.Fatal("pendingBefore inattendu")
	}
	return f.pendingFn(ctx, before)
}

func (f *utStore) ensureIndex(ctx context.Context, d indexDef) error {
	f.t.Helper()
	if f.ensureFn == nil {
		f.t.Fatal("ensureIndex inattendu")
	}
	return f.ensureFn(ctx, d)
}

func utLedger(t *testing.T) (*Ledger, *utStore) {
	t.Helper()
	fs := &utStore{t: t}
	return &Ledger{st: fs}, fs
}

func utValidNew() NewEntry {
	return NewEntry{
		GameID:         "crash",
		OperatorID:     "op1",
		PlayerID:       "pl1",
		ExternalRef:    "round=r1|bet=b1",
		IdempotencyKey: "debit:op1:pl1:b1",
		Type:           EntryDebit,
		AmountMinor:    500,
	}
}

func TestUnitNewEntryValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*NewEntry)
		want   error
	}{
		{"valide DEBIT", func(*NewEntry) {}, nil},
		{"valide CREDIT", func(e *NewEntry) { e.Type = EntryCredit }, nil},
		{"valide ROLLBACK avec parent", func(e *NewEntry) { e.Type = EntryRollback; e.ParentTransactionID = "txn_parent" }, nil},
		{"game id manquant", func(e *NewEntry) { e.GameID = "" }, ErrGameIDRequired},
		{"operator id manquant", func(e *NewEntry) { e.OperatorID = "" }, ErrOperatorIDRequired},
		{"player id manquant", func(e *NewEntry) { e.PlayerID = "" }, ErrPlayerIDRequired},
		{"idempotency key manquante", func(e *NewEntry) { e.IdempotencyKey = "" }, ErrIdempotencyKeyRequired},
		{"montant nul", func(e *NewEntry) { e.AmountMinor = 0 }, ErrInvalidAmount},
		{"montant négatif", func(e *NewEntry) { e.AmountMinor = -1 }, ErrInvalidAmount},
		{"type vide", func(e *NewEntry) { e.Type = "" }, ErrInvalidEntryType},
		{"type inconnu", func(e *NewEntry) { e.Type = "REFUND" }, ErrInvalidEntryType},
		{"rollback sans parent", func(e *NewEntry) { e.Type = EntryRollback }, ErrParentTransactionIDRequired},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := utValidNew()
			tc.mutate(&e)
			if got := e.validate(); !errors.Is(got, tc.want) {
				t.Fatalf("validate() = %v, attendu %v", got, tc.want)
			}
		})
	}
}

func TestUnitNewTransactionID(t *testing.T) {
	re := regexp.MustCompile(`^txn_[0-9a-f]{24}$`)
	a, err := newTransactionID()
	if err != nil {
		t.Fatal(err)
	}
	b, err := newTransactionID()
	if err != nil {
		t.Fatal(err)
	}
	if !re.MatchString(string(a)) || !re.MatchString(string(b)) {
		t.Fatalf("format inattendu: %q %q", a, b)
	}
	if a == b {
		t.Fatalf("deux ids identiques: %q", a)
	}
}

func TestUnitNewTransactionIDRandomFailure(t *testing.T) {
	orig := randRead
	t.Cleanup(func() { randRead = orig })
	randRead = func([]byte) (int, error) { return 0, utErrInfra }

	id, err := newTransactionID()
	if id != "" || !errors.Is(err, utErrInfra) {
		t.Fatalf("id=%q err=%v", id, err)
	}
}

func TestUnitBeginValidationErrorSkipsStore(t *testing.T) {
	l, _ := utLedger(t) // toute interaction store ferait échouer le test
	e := utValidNew()
	e.AmountMinor = 0
	entry, created, err := l.Begin(context.Background(), e)
	if entry != nil || created || !errors.Is(err, ErrInvalidAmount) {
		t.Fatalf("entry=%v created=%v err=%v", entry, created, err)
	}
}

func TestUnitBeginRandomFailure(t *testing.T) {
	orig := randRead
	t.Cleanup(func() { randRead = orig })
	randRead = func([]byte) (int, error) { return 0, utErrInfra }

	l, _ := utLedger(t)
	entry, created, err := l.Begin(context.Background(), utValidNew())
	if entry != nil || created || !errors.Is(err, utErrInfra) {
		t.Fatalf("entry=%v created=%v err=%v", entry, created, err)
	}
}

func TestUnitBeginInsertSuccess(t *testing.T) {
	l, fs := utLedger(t)
	fs.insertFn = func(context.Context, Entry) error { return nil }

	in := utValidNew()
	before := time.Now()
	entry, created, err := l.Begin(context.Background(), in)
	if err != nil || !created || entry == nil {
		t.Fatalf("entry=%v created=%v err=%v", entry, created, err)
	}
	if len(fs.insertSeen) != 1 || fs.insertSeen[0].TransactionID != entry.TransactionID {
		t.Fatalf("l'entrée insérée doit être celle retournée: %+v", fs.insertSeen)
	}
	if entry.Status != StatusPending || entry.Type != EntryDebit || entry.AmountMinor != 500 ||
		entry.IdempotencyKey != in.IdempotencyKey || entry.GameID != in.GameID ||
		entry.OperatorID != in.OperatorID || entry.PlayerID != in.PlayerID ||
		entry.ExternalRef != in.ExternalRef {
		t.Fatalf("entrée inattendue: %+v", entry)
	}
	if !strings.HasPrefix(string(entry.TransactionID), "txn_") {
		t.Fatalf("transaction id: %q", entry.TransactionID)
	}
	if entry.CreatedAt.Before(before) || !entry.CreatedAt.Equal(entry.UpdatedAt) {
		t.Fatalf("horodatages incohérents: %v %v", entry.CreatedAt, entry.UpdatedAt)
	}
	if entry.OperatorBalanceAfterMinor != nil || !entry.ConfirmedAt.IsZero() || !entry.FailedAt.IsZero() {
		t.Fatalf("champs de résolution renseignés à la création: %+v", entry)
	}
}

func TestUnitBeginRollbackKeepsParent(t *testing.T) {
	l, fs := utLedger(t)
	fs.insertFn = func(context.Context, Entry) error { return nil }

	in := utValidNew()
	in.Type = EntryRollback
	in.ParentTransactionID = "txn_parent"
	entry, _, err := l.Begin(context.Background(), in)
	if err != nil || entry.ParentTransactionID != "txn_parent" {
		t.Fatalf("entry=%+v err=%v", entry, err)
	}
}

func TestUnitBeginInsertError(t *testing.T) {
	l, fs := utLedger(t)
	fs.insertFn = func(context.Context, Entry) error { return utErrInfra }

	entry, created, err := l.Begin(context.Background(), utValidNew())
	if entry != nil || created || !errors.Is(err, utErrInfra) || !strings.Contains(err.Error(), "insertion") {
		t.Fatalf("entry=%v created=%v err=%v", entry, created, err)
	}
	if errors.Is(err, errDuplicate) {
		t.Fatal("une erreur d'infra ne doit pas être prise pour un doublon")
	}
}

func TestUnitBeginDuplicateReturnsExistingInSameOperatorScope(t *testing.T) {
	l, fs := utLedger(t)
	existing := &Entry{
		TransactionID:  "txn_old",
		OperatorID:     "op1",
		IdempotencyKey: utValidNew().IdempotencyKey,
		Status:         StatusConfirmed,
	}
	fs.insertFn = func(context.Context, Entry) error { return errDuplicate }

	var gotOperator OperatorID
	var gotKey IdempotencyKey
	fs.byKeyFn = func(_ context.Context, operatorID OperatorID, key IdempotencyKey) (*Entry, error) {
		gotOperator = operatorID
		gotKey = key
		return existing, nil
	}

	entry, created, err := l.Begin(context.Background(), utValidNew())
	if err != nil || created || entry != existing {
		t.Fatalf("entry=%v created=%v err=%v", entry, created, err)
	}
	if gotOperator != utValidNew().OperatorID || gotKey != utValidNew().IdempotencyKey {
		t.Fatalf("relecture hors périmètre: operator=%q key=%q", gotOperator, gotKey)
	}
}

func TestUnitBeginDuplicateRereadFails(t *testing.T) {
	l, fs := utLedger(t)
	fs.insertFn = func(context.Context, Entry) error { return errDuplicate }
	fs.byKeyFn = func(context.Context, OperatorID, IdempotencyKey) (*Entry, error) {
		return nil, utErrInfra
	}

	entry, created, err := l.Begin(context.Background(), utValidNew())
	if entry != nil || created || !errors.Is(err, utErrInfra) || !strings.Contains(err.Error(), "relecture") {
		t.Fatalf("entry=%v created=%v err=%v", entry, created, err)
	}
}

func TestUnitBeginDuplicateButEntryVanished(t *testing.T) {
	l, fs := utLedger(t)
	fs.insertFn = func(context.Context, Entry) error { return errDuplicate }
	fs.byKeyFn = func(context.Context, OperatorID, IdempotencyKey) (*Entry, error) {
		return nil, errNotFound
	}

	_, created, err := l.Begin(context.Background(), utValidNew())
	if created || !errors.Is(err, ErrEntryNotFound) {
		t.Fatalf("created=%v err=%v", created, err)
	}
}

func TestUnitConfirmPassesSpec(t *testing.T) {
	l, fs := utLedger(t)
	want := &Entry{TransactionID: "txn_1", Status: StatusConfirmed}
	bal := int64(12345)
	fs.resolveFn = func(_ context.Context, id TransactionID, ts transitionSpec) (*Entry, error) {
		if id != "txn_1" || ts.to != StatusConfirmed || ts.balance == nil || *ts.balance != 12345 ||
			ts.reason != "" || ts.at.IsZero() {
			t.Fatalf("spec inattendue: id=%s %+v", id, ts)
		}
		return want, nil
	}
	got, err := l.Confirm(context.Background(), "txn_1", &bal)
	if err != nil || got != want {
		t.Fatalf("got=%v err=%v", got, err)
	}
}

func TestUnitConfirmWithoutBalance(t *testing.T) {
	l, fs := utLedger(t)
	fs.resolveFn = func(_ context.Context, _ TransactionID, ts transitionSpec) (*Entry, error) {
		if ts.balance != nil {
			t.Fatalf("aucun solde ne doit être transmis: %v", *ts.balance)
		}
		return &Entry{Status: StatusConfirmed}, nil
	}
	if _, err := l.Confirm(context.Background(), "txn_1", nil); err != nil {
		t.Fatal(err)
	}
}

func TestUnitFailPassesSpec(t *testing.T) {
	l, fs := utLedger(t)
	want := &Entry{TransactionID: "txn_2", Status: StatusFailed}
	fs.resolveFn = func(_ context.Context, id TransactionID, ts transitionSpec) (*Entry, error) {
		if id != "txn_2" || ts.to != StatusFailed || ts.reason != "fonds insuffisants" ||
			ts.balance != nil || ts.at.IsZero() {
			t.Fatalf("spec inattendue: id=%s %+v", id, ts)
		}
		return want, nil
	}
	got, err := l.Fail(context.Background(), "txn_2", "fonds insuffisants")
	if err != nil || got != want {
		t.Fatalf("got=%v err=%v", got, err)
	}
}

func TestUnitTransitionInfraError(t *testing.T) {
	l, fs := utLedger(t)
	fs.resolveFn = func(context.Context, TransactionID, transitionSpec) (*Entry, error) {
		return nil, utErrInfra
	}
	// byTxnFn non configuré : aucune relecture ne doit avoir lieu sur une erreur d'infra.

	got, err := l.Confirm(context.Background(), "txn_1", nil)
	if got != nil || !errors.Is(err, utErrInfra) {
		t.Fatalf("got=%v err=%v", got, err)
	}
	if errors.Is(err, ErrEntryNotFound) || errors.Is(err, ErrStateConflict) {
		t.Fatal("une erreur d'infra ne doit pas être traduite en erreur métier")
	}
}

func TestUnitTransitionIdempotent(t *testing.T) {
	for _, to := range []EntryStatus{StatusConfirmed, StatusFailed} {
		t.Run(string(to), func(t *testing.T) {
			l, fs := utLedger(t)
			existing := &Entry{TransactionID: "txn_1", Status: to}
			fs.resolveFn = func(context.Context, TransactionID, transitionSpec) (*Entry, error) {
				return nil, errNotFound
			}
			fs.byTxnFn = func(context.Context, TransactionID) (*Entry, error) {
				return existing, nil
			}

			var got *Entry
			var err error
			if to == StatusConfirmed {
				got, err = l.Confirm(context.Background(), "txn_1", nil)
			} else {
				got, err = l.Fail(context.Background(), "txn_1", "x")
			}
			if err != nil || got != existing {
				t.Fatalf("got=%v err=%v", got, err)
			}
		})
	}
}

func TestUnitTransitionStateConflict(t *testing.T) {
	tests := []struct {
		name    string
		current EntryStatus
		call    func(*Ledger) error
	}{
		{"confirm sur FAILED", StatusFailed, func(l *Ledger) error {
			_, err := l.Confirm(context.Background(), "txn_1", nil)
			return err
		}},
		{"fail sur CONFIRMED", StatusConfirmed, func(l *Ledger) error {
			_, err := l.Fail(context.Background(), "txn_1", "x")
			return err
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l, fs := utLedger(t)
			fs.resolveFn = func(context.Context, TransactionID, transitionSpec) (*Entry, error) {
				return nil, errNotFound
			}
			fs.byTxnFn = func(context.Context, TransactionID) (*Entry, error) {
				return &Entry{TransactionID: "txn_1", Status: tc.current}, nil
			}
			err := tc.call(l)
			if !errors.Is(err, ErrStateConflict) || !strings.Contains(err.Error(), string(tc.current)) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestUnitTransitionEntryNotFound(t *testing.T) {
	l, fs := utLedger(t)
	fs.resolveFn = func(context.Context, TransactionID, transitionSpec) (*Entry, error) {
		return nil, errNotFound
	}
	fs.byTxnFn = func(context.Context, TransactionID) (*Entry, error) {
		return nil, errNotFound
	}

	_, err := l.Confirm(context.Background(), "txn_x", nil)
	if !errors.Is(err, ErrEntryNotFound) {
		t.Fatalf("err=%v", err)
	}
}

func TestUnitTransitionRereadInfraErrorIsNotNotFound(t *testing.T) {
	l, fs := utLedger(t)
	fs.resolveFn = func(context.Context, TransactionID, transitionSpec) (*Entry, error) {
		return nil, errNotFound
	}
	fs.byTxnFn = func(context.Context, TransactionID) (*Entry, error) {
		return nil, utErrInfra
	}

	_, err := l.Fail(context.Background(), "txn_x", "r")
	if !errors.Is(err, utErrInfra) {
		t.Fatalf("la cause d'infra doit rester visible: %v", err)
	}
	if errors.Is(err, ErrEntryNotFound) {
		t.Fatal("une panne de relecture ne doit pas être présentée comme 'entrée introuvable'")
	}
}

func TestUnitGetByIdempotencyKey(t *testing.T) {
	l, fs := utLedger(t)
	want := &Entry{TransactionID: "txn_1", OperatorID: "op1", IdempotencyKey: "k"}

	var gotOperator OperatorID
	var gotKey IdempotencyKey
	fs.byKeyFn = func(_ context.Context, operatorID OperatorID, key IdempotencyKey) (*Entry, error) {
		gotOperator, gotKey = operatorID, key
		return want, nil
	}

	if got, err := l.GetByIdempotencyKey(context.Background(), "op1", "k"); err != nil || got != want {
		t.Fatalf("got=%v err=%v", got, err)
	}
	if gotOperator != "op1" || gotKey != "k" {
		t.Fatalf("scope transmis incorrect: operator=%q key=%q", gotOperator, gotKey)
	}

	fs.byKeyFn = func(context.Context, OperatorID, IdempotencyKey) (*Entry, error) {
		return nil, errNotFound
	}
	if _, err := l.GetByIdempotencyKey(context.Background(), "op1", "k"); !errors.Is(err, ErrEntryNotFound) {
		t.Fatalf("err=%v", err)
	}

	fs.byKeyFn = func(context.Context, OperatorID, IdempotencyKey) (*Entry, error) {
		return nil, utErrInfra
	}
	if _, err := l.GetByIdempotencyKey(context.Background(), "op1", "k"); !errors.Is(err, utErrInfra) || errors.Is(err, ErrEntryNotFound) {
		t.Fatalf("err=%v", err)
	}
}

func TestUnitGetByIdempotencyKeyRejectsEmptyScope(t *testing.T) {
	l, _ := utLedger(t)

	if _, err := l.GetByIdempotencyKey(context.Background(), "", "k"); !errors.Is(err, ErrOperatorIDRequired) {
		t.Fatalf("operator vide: err=%v", err)
	}
	if _, err := l.GetByIdempotencyKey(context.Background(), "op1", ""); !errors.Is(err, ErrIdempotencyKeyRequired) {
		t.Fatalf("clé vide: err=%v", err)
	}
}

func TestUnitGetByTransactionID(t *testing.T) {
	l, fs := utLedger(t)
	want := &Entry{TransactionID: "txn_1"}
	fs.byTxnFn = func(context.Context, TransactionID) (*Entry, error) {
		return want, nil
	}
	if got, err := l.GetByTransactionID(context.Background(), "txn_1"); err != nil || got != want {
		t.Fatalf("got=%v err=%v", got, err)
	}

	fs.byTxnFn = func(context.Context, TransactionID) (*Entry, error) {
		return nil, errNotFound
	}
	if _, err := l.GetByTransactionID(context.Background(), "txn_1"); !errors.Is(err, ErrEntryNotFound) {
		t.Fatalf("err=%v", err)
	}

	fs.byTxnFn = func(context.Context, TransactionID) (*Entry, error) {
		return nil, utErrInfra
	}
	if _, err := l.GetByTransactionID(context.Background(), "txn_1"); !errors.Is(err, utErrInfra) || errors.Is(err, ErrEntryNotFound) {
		t.Fatalf("err=%v", err)
	}
}

func TestUnitListPendingOlderThan(t *testing.T) {
	l, fs := utLedger(t)
	cutoff := time.Now().Add(-time.Minute)
	want := []Entry{{TransactionID: "txn_1"}, {TransactionID: "txn_2"}}
	fs.pendingFn = func(_ context.Context, before time.Time) ([]Entry, error) {
		if !before.Equal(cutoff) {
			t.Fatalf("before=%v", before)
		}
		return want, nil
	}
	got, err := l.ListPendingOlderThan(context.Background(), cutoff)
	if err != nil || len(got) != 2 || got[0].TransactionID != "txn_1" {
		t.Fatalf("got=%v err=%v", got, err)
	}

	fs.pendingFn = func(context.Context, time.Time) ([]Entry, error) {
		return nil, utErrInfra
	}
	got, err = l.ListPendingOlderThan(context.Background(), cutoff)
	if got != nil || !errors.Is(err, utErrInfra) || !strings.Contains(err.Error(), "list pending") {
		t.Fatalf("got=%v err=%v", got, err)
	}
}

func TestUnitEnsureIndexesAll(t *testing.T) {
	_, fs := utLedger(t)
	var labels []string
	var uniques []bool
	var names []string
	var fields [][]string

	fs.ensureFn = func(_ context.Context, d indexDef) error {
		labels = append(labels, d.label)
		uniques = append(uniques, d.unique)
		names = append(names, d.name)
		fields = append(fields, append([]string(nil), d.fields...))
		return nil
	}

	if err := ensureIndexes(context.Background(), fs); err != nil {
		t.Fatal(err)
	}

	wantLabels := []string{
		"operatorId/idempotencyKey",
		"transactionId",
		"status/createdAt",
		"parentTransactionId",
	}
	wantUnique := []bool{true, true, false, false}
	wantNames := []string{
		idempotencyIndexName,
		"transactionId_1",
		"status_1_createdAt_1",
		"parentTransactionId_1",
	}

	if strings.Join(labels, ",") != strings.Join(wantLabels, ",") {
		t.Fatalf("labels=%v", labels)
	}
	if strings.Join(names, ",") != strings.Join(wantNames, ",") {
		t.Fatalf("names=%v", names)
	}
	for i := range wantUnique {
		if uniques[i] != wantUnique[i] {
			t.Fatalf("unique[%d]=%v", i, uniques[i])
		}
	}
	if len(fields) != 4 ||
		strings.Join(fields[0], ",") != "operatorId,idempotencyKey" ||
		strings.Join(fields[1], ",") != "transactionId" {
		t.Fatalf("définitions de champs d'index inattendues: %v", fields)
	}
}

func TestUnitEnsureIndexesStopsOnFirstError(t *testing.T) {
	for failAt := 0; failAt < len(indexDefs); failAt++ {
		_, fs := utLedger(t)
		calls := 0
		fs.ensureFn = func(_ context.Context, _ indexDef) error {
			calls++
			if calls == failAt+1 {
				return utErrInfra
			}
			return nil
		}

		err := ensureIndexes(context.Background(), fs)
		if !errors.Is(err, utErrInfra) || !strings.Contains(err.Error(), indexDefs[failAt].label) {
			t.Fatalf("failAt=%d err=%v", failAt, err)
		}
		if calls != failAt+1 {
			t.Fatalf("failAt=%d: %d appels (doit s'arrêter à la première erreur)", failAt, calls)
		}
	}
}
