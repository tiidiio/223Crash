package ledger

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// newTestLedger connecte un Ledger à une base MongoDB réelle désignée par MONGO_TEST_URI.
// Si la variable n'est pas définie, le test est explicitement SKIPPED — jamais simulé, jamais
// marqué PASS sans connexion réelle : l'atomicité testée ici dépend de l'index unique MongoDB,
// qu'aucun mock in-memory ne peut reproduire fidèlement.
func newTestLedger(t *testing.T) (*Ledger, func()) {
	t.Helper()

	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI non défini — test Ledger nécessitant MongoDB SKIPPED")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatalf("connexion mongo: %v", err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		t.Fatalf("ping mongo: %v", err)
	}

	db := client.Database("223crash_ledger_test")
	col := db.Collection(collectionName)
	if err := col.Drop(ctx); err != nil {
		t.Fatalf("drop collection avant test: %v", err)
	}

	l, err := New(ctx, db)
	if err != nil {
		t.Fatalf("New(ledger): %v", err)
	}

	cleanup := func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = col.Drop(cleanupCtx)
		_ = client.Disconnect(cleanupCtx)
	}

	return l, cleanup
}

func validEntry(idemKey IdempotencyKey) NewEntry {
	return NewEntry{
		GameID:         GameID("223crash"),
		OperatorID:     OperatorID("op-1"),
		PlayerID:       PlayerID("player-1"),
		ExternalRef:    ExternalRef("round=round-1|panel=A|amount=10000"),
		IdempotencyKey: idemKey,
		Type:           EntryDebit,
		AmountMinor:    10000,
	}
}

func TestBegin_CreatesNewEntry(t *testing.T) {
	l, cleanup := newTestLedger(t)
	defer cleanup()

	ctx := context.Background()
	entry, created, err := l.Begin(ctx, validEntry("idem-1"))
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if !created {
		t.Fatalf("attendu created=true sur une première insertion")
	}
	if entry.Status != StatusPending {
		t.Fatalf("attendu status=PENDING, obtenu %s", entry.Status)
	}
	if entry.TransactionID == "" {
		t.Fatalf("TransactionID vide")
	}
}

func TestBegin_IdempotentRetrySameKey(t *testing.T) {
	l, cleanup := newTestLedger(t)
	defer cleanup()

	ctx := context.Background()
	first, created1, err := l.Begin(ctx, validEntry("idem-2"))
	if err != nil {
		t.Fatalf("Begin premier appel: %v", err)
	}
	if !created1 {
		t.Fatalf("attendu created=true au premier appel")
	}

	second, created2, err := l.Begin(ctx, validEntry("idem-2"))
	if err != nil {
		t.Fatalf("Begin second appel: %v", err)
	}
	if created2 {
		t.Fatalf("attendu created=false au second appel avec la même idempotencyKey")
	}
	if second.TransactionID != first.TransactionID {
		t.Fatalf("attendu le même TransactionID, obtenu %s vs %s", second.TransactionID, first.TransactionID)
	}
}

func TestBegin_ConcurrentSameKey_ExactlyOneCreated(t *testing.T) {
	l, cleanup := newTestLedger(t)
	defer cleanup()

	const attempts = 20
	var createdCount int64
	var wg sync.WaitGroup
	results := make([]*Entry, attempts)
	errs := make([]error, attempts)

	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			entry, created, err := l.Begin(context.Background(), validEntry("idem-concurrent"))
			results[idx] = entry
			errs[idx] = err
			if created {
				atomic.AddInt64(&createdCount, 1)
			}
		}(i)
	}
	wg.Wait()

	if createdCount != 1 {
		t.Fatalf("attendu exactement 1 insertion créée sur %d tentatives concurrentes, obtenu %d", attempts, createdCount)
	}

	var txnID TransactionID
	for i, err := range errs {
		if err != nil {
			t.Fatalf("Begin goroutine %d: %v", i, err)
		}
		if txnID == "" {
			txnID = results[i].TransactionID
		}
		if results[i].TransactionID != txnID {
			t.Fatalf("TransactionID divergent entre goroutines concurrentes: %s vs %s", results[i].TransactionID, txnID)
		}
	}
}

func TestConfirm_TransitionsFromPending(t *testing.T) {
	l, cleanup := newTestLedger(t)
	defer cleanup()

	ctx := context.Background()
	entry, _, err := l.Begin(ctx, validEntry("idem-confirm"))
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	balance := int64(50000)
	confirmed, err := l.Confirm(ctx, entry.TransactionID, &balance)
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if confirmed.Status != StatusConfirmed {
		t.Fatalf("attendu status=CONFIRMED, obtenu %s", confirmed.Status)
	}
	if confirmed.OperatorBalanceAfterMinor == nil || *confirmed.OperatorBalanceAfterMinor != balance {
		t.Fatalf("solde opérateur non persisté correctement")
	}
}

func TestConfirm_IdempotentDoubleConfirm(t *testing.T) {
	l, cleanup := newTestLedger(t)
	defer cleanup()

	ctx := context.Background()
	entry, _, err := l.Begin(ctx, validEntry("idem-double-confirm"))
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	if _, err := l.Confirm(ctx, entry.TransactionID, nil); err != nil {
		t.Fatalf("premier Confirm: %v", err)
	}
	second, err := l.Confirm(ctx, entry.TransactionID, nil)
	if err != nil {
		t.Fatalf("second Confirm (idempotent) ne doit pas échouer: %v", err)
	}
	if second.Status != StatusConfirmed {
		t.Fatalf("attendu status=CONFIRMED après double confirm, obtenu %s", second.Status)
	}
}

func TestFail_ThenConfirmConflicts(t *testing.T) {
	l, cleanup := newTestLedger(t)
	defer cleanup()

	ctx := context.Background()
	entry, _, err := l.Begin(ctx, validEntry("idem-fail-conflict"))
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	if _, err := l.Fail(ctx, entry.TransactionID, "rejet test"); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	if _, err := l.Confirm(ctx, entry.TransactionID, nil); err == nil {
		t.Fatalf("attendu ErrStateConflict en confirmant une entrée déjà FAILED")
	}
}

func TestBegin_RejectsInvalidEntry(t *testing.T) {
	l, cleanup := newTestLedger(t)
	defer cleanup()

	ctx := context.Background()
	invalid := validEntry("idem-invalid")
	invalid.AmountMinor = 0

	if _, _, err := l.Begin(ctx, invalid); err == nil {
		t.Fatalf("attendu une erreur de validation pour AmountMinor=0")
	}
}

func TestListPendingOlderThan(t *testing.T) {
	l, cleanup := newTestLedger(t)
	defer cleanup()

	ctx := context.Background()
	if _, _, err := l.Begin(ctx, validEntry("idem-pending-1")); err != nil {
		t.Fatalf("Begin: %v", err)
	}

	entries, err := l.ListPendingOlderThan(ctx, time.Now().Add(1*time.Minute))
	if err != nil {
		t.Fatalf("ListPendingOlderThan: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.IdempotencyKey == "idem-pending-1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("entrée PENDING attendue dans ListPendingOlderThan")
	}
}
