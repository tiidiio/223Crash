package wallet

// Tests d'intégration de Queue.process / drainDue contre un vrai Mongo (Atlas jetable) et un
// faux opérateur httptest. Skippés si MONGO_TEST_URI est absent : `go test ./...` reste vert.
// Chaque test utilise une base aléatoire t223_wallet_<hex>, supprimée en t.Cleanup.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/tiidiio/223Gaming/223Crash/internal/provider"
	"github.com/tiidiio/223Gaming/223Crash/internal/provider/ledger"
)

type testEnv struct {
	q   *Queue
	led *ledger.Ledger
	cli *Client
	srv *httptest.Server

	mu    sync.Mutex
	calls int
	keys  []string
}

func (e *testEnv) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

func (e *testEnv) idemKeys() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.keys...)
}

func newTestEnv(t *testing.T, h http.HandlerFunc) *testEnv {
	t.Helper()
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI absent : test d'intégration Mongo ignoré")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	mc, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatalf("mongo connect: %v", err)
	}
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	db := mc.Database("t223_wallet_" + hex.EncodeToString(b))
	t.Cleanup(func() {
		c, cc := context.WithTimeout(context.Background(), 30*time.Second)
		defer cc()
		_ = db.Drop(c)
		_ = mc.Disconnect(c)
	})

	led, err := ledger.New(ctx, db)
	if err != nil {
		t.Fatalf("ledger.New: %v", err)
	}

	e := &testEnv{led: led}
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		e.calls++
		e.keys = append(e.keys, r.Header.Get("Idempotency-Key"))
		e.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(e.srv.Close)

	e.cli = NewClient(e.srv.URL, "test-secret")
	e.q, err = NewQueue(ctx, db, e.cli, led)
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	return e
}

func respond(code int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}
}

// seed ouvre (optionnellement) une entrée Ledger, enqueue le callback et le relit depuis Mongo.
func (e *testEnv) seed(t *testing.T, id string, withTxn bool) Callback {
	t.Helper()
	ctx := context.Background()
	cb := Callback{
		ID:             "cb-" + id,
		Type:           CallbackCredit,
		GameID:         "223crash",
		OperatorID:     "op1",
		PlayerID:       "p1",
		RoundID:        "r-" + id,
		BetID:          "b-" + id,
		Amount:         1000,
		IdempotencyKey: "credit:" + id,
	}
	if withTxn {
		entry, created, err := e.led.Begin(ctx, ledger.NewEntry{
			GameID:         "223crash",
			OperatorID:     "op1",
			PlayerID:       "p1",
			IdempotencyKey: provider.IdempotencyKey(cb.IdempotencyKey),
			Type:           ledger.EntryCredit,
			AmountMinor:    1000,
		})
		if err != nil || !created {
			t.Fatalf("ledger.Begin: created=%v err=%v", created, err)
		}
		cb.TransactionID = entry.TransactionID
	}
	if err := e.q.Enqueue(ctx, cb); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	return e.load(t, cb.ID)
}

func (e *testEnv) load(t *testing.T, id string) Callback {
	t.Helper()
	var cb Callback
	if err := e.q.col.FindOne(context.Background(), bson.M{"_id": id}).Decode(&cb); err != nil {
		t.Fatalf("load %s: %v", id, err)
	}
	return cb
}

func (e *testEnv) queueCount(t *testing.T) int64 {
	t.Helper()
	n, err := e.q.col.CountDocuments(context.Background(), bson.M{})
	if err != nil {
		t.Fatalf("count queue: %v", err)
	}
	return n
}

func (e *testEnv) deadCount(t *testing.T) int64 {
	t.Helper()
	n, err := e.q.deadCol.CountDocuments(context.Background(), bson.M{})
	if err != nil {
		t.Fatalf("count dead: %v", err)
	}
	return n
}

func (e *testEnv) entry(t *testing.T, txn provider.TransactionID) *ledger.Entry {
	t.Helper()
	en, err := e.led.GetByTransactionID(context.Background(), txn)
	if err != nil {
		t.Fatalf("ledger get %s: %v", txn, err)
	}
	return en
}

func TestProcess_Accepted_ConfirmsLedgerAndRemovesFromQueue(t *testing.T) {
	e := newTestEnv(t, respond(200, `{"balance":4200}`))
	cb := e.seed(t, "ok", true)

	e.q.process(context.Background(), cb)

	en := e.entry(t, cb.TransactionID)
	if en.Status != ledger.StatusConfirmed {
		t.Fatalf("status = %s, attendu CONFIRMED", en.Status)
	}
	if en.OperatorBalanceAfterMinor == nil || *en.OperatorBalanceAfterMinor != 4200 {
		t.Fatalf("balance = %v, attendu 4200", en.OperatorBalanceAfterMinor)
	}
	if n := e.queueCount(t); n != 0 {
		t.Fatalf("queue = %d, attendu 0", n)
	}
	if n := e.deadCount(t); n != 0 {
		t.Fatalf("dead = %d, attendu 0", n)
	}
}

func TestProcess_AcceptedUnreadableBody_StillConfirmsNoRetry(t *testing.T) {
	e := newTestEnv(t, respond(200, `<<pas du json>>`))
	cb := e.seed(t, "badbody", true)

	e.q.process(context.Background(), cb)

	en := e.entry(t, cb.TransactionID)
	if en.Status != ledger.StatusConfirmed {
		t.Fatalf("status = %s, attendu CONFIRMED (HTTP 200 fait foi)", en.Status)
	}
	// Point ouvert : resolveLedger reçoit &balance == &0 même quand le corps n'a pas pu être
	// parsé, donc un faux solde 0 est écrit dans le Ledger. Voir compte rendu.
	if en.OperatorBalanceAfterMinor != nil {
		t.Logf("ATTENTION: operatorBalanceAfterMinor = %d écrit alors que le corps était illisible", *en.OperatorBalanceAfterMinor)
	}
	if n := e.queueCount(t); n != 0 {
		t.Fatalf("queue = %d, attendu 0 (pas de retry sur un 200)", n)
	}
	if c := e.callCount(); c != 1 {
		t.Fatalf("appels opérateur = %d, attendu 1", c)
	}
}

func TestProcess_Rejected_FailsLedgerAndDeadLettersImmediately(t *testing.T) {
	e := newTestEnv(t, respond(422, `insufficient funds`))
	cb := e.seed(t, "rej", true)

	e.q.process(context.Background(), cb)

	en := e.entry(t, cb.TransactionID)
	if en.Status != ledger.StatusFailed {
		t.Fatalf("status = %s, attendu FAILED", en.Status)
	}
	if !strings.Contains(en.FailureReason, "422") {
		t.Fatalf("failureReason = %q, attendu mention de 422", en.FailureReason)
	}
	if n := e.queueCount(t); n != 0 {
		t.Fatalf("queue = %d, attendu 0", n)
	}
	if n := e.deadCount(t); n != 1 {
		t.Fatalf("dead = %d, attendu 1", n)
	}
	if c := e.callCount(); c != 1 {
		t.Fatalf("appels opérateur = %d, attendu 1 (pas de retry sur rejet)", c)
	}
}

func TestProcess_Unknown5xx_KeepsLedgerPendingAndSchedulesRetry(t *testing.T) {
	e := newTestEnv(t, respond(500, `boom`))
	cb := e.seed(t, "5xx", true)
	before := time.Now()

	e.q.process(context.Background(), cb)

	if en := e.entry(t, cb.TransactionID); en.Status != ledger.StatusPending {
		t.Fatalf("status = %s, attendu PENDING (résultat indéterminé)", en.Status)
	}
	got := e.load(t, cb.ID)
	if got.Attempts != 1 {
		t.Fatalf("attempts = %d, attendu 1", got.Attempts)
	}
	if got.LastError == "" {
		t.Fatalf("lastError vide")
	}
	if !got.DueAt.After(before) {
		t.Fatalf("dueAt = %v, attendu > %v (backoff)", got.DueAt, before)
	}
	if n := e.deadCount(t); n != 0 {
		t.Fatalf("dead = %d, attendu 0", n)
	}
}

func TestProcess_UnknownTimeout_KeepsLedgerPending(t *testing.T) {
	e := newTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(500 * time.Millisecond):
		case <-r.Context().Done():
		}
	})
	e.cli.http.Timeout = 50 * time.Millisecond
	cb := e.seed(t, "timeout", true)

	e.q.process(context.Background(), cb)

	if en := e.entry(t, cb.TransactionID); en.Status != ledger.StatusPending {
		t.Fatalf("status = %s, attendu PENDING sur timeout", en.Status)
	}
	if got := e.load(t, cb.ID); got.Attempts != 1 {
		t.Fatalf("attempts = %d, attendu 1", got.Attempts)
	}
	if n := e.deadCount(t); n != 0 {
		t.Fatalf("dead = %d, attendu 0", n)
	}
}

func TestProcess_Retries_ReuseSameIdempotencyKey(t *testing.T) {
	e := newTestEnv(t, respond(503, `unavailable`))
	cb := e.seed(t, "idem", true)

	for i := 0; i < 3; i++ {
		e.q.process(context.Background(), e.load(t, cb.ID))
	}

	keys := e.idemKeys()
	if len(keys) != 3 {
		t.Fatalf("appels = %d, attendu 3", len(keys))
	}
	for i, k := range keys {
		if k != cb.IdempotencyKey {
			t.Fatalf("appel %d: Idempotency-Key = %q, attendu %q", i, k, cb.IdempotencyKey)
		}
	}
	if got := e.load(t, cb.ID); got.Attempts != 3 {
		t.Fatalf("attempts = %d, attendu 3", got.Attempts)
	}
}

func TestProcess_LastAttemptUnknown_DeadLettersButLedgerStaysPending(t *testing.T) {
	e := newTestEnv(t, respond(500, `boom`))
	cb := e.seed(t, "dead", true)
	cb.Attempts = maxAttempts - 1

	e.q.process(context.Background(), cb)

	if n := e.deadCount(t); n != 1 {
		t.Fatalf("dead = %d, attendu 1", n)
	}
	if n := e.queueCount(t); n != 0 {
		t.Fatalf("queue = %d, attendu 0", n)
	}
	if en := e.entry(t, cb.TransactionID); en.Status != ledger.StatusPending {
		t.Fatalf("status = %s, attendu PENDING (jamais FAILED sur indéterminé)", en.Status)
	}
}

func TestProcess_NoTransactionID_DoesNotPanicAndProcesses(t *testing.T) {
	e := newTestEnv(t, respond(200, `{"balance":1}`))
	cb := e.seed(t, "notxn", false)

	e.q.process(context.Background(), cb)

	if n := e.queueCount(t); n != 0 {
		t.Fatalf("queue = %d, attendu 0", n)
	}
}

// Documente le comportement ACTUEL (trou de cohérence) : Confirm échoue (ici ErrEntryNotFound),
// mais le callback est quand même supprimé de la queue. Après correctif de queue.go
// (ne supprimer que si Confirm réussit, sinon dead-letter), inverser ces assertions.
func TestProcess_AcceptedButConfirmFails_CurrentBehaviorDropsCallback(t *testing.T) {
	e := newTestEnv(t, respond(200, `{"balance":10}`))
	cb := e.seed(t, "orphan", false)
	cb.TransactionID = "txn_inexistant"

	e.q.process(context.Background(), cb)

	if n := e.queueCount(t); n != 0 {
		t.Fatalf("queue = %d (comportement actuel attendu : 0)", n)
	}
	if n := e.deadCount(t); n != 0 {
		t.Fatalf("dead = %d (comportement actuel attendu : 0, trace perdue)", n)
	}
	t.Log("CONNU: argent déplacé côté opérateur, callback supprimé, aucune entrée Ledger résolue")
}

func TestDrainDue_ProcessesOnceThenBacksOff(t *testing.T) {
	e := newTestEnv(t, respond(500, `boom`))
	cb := e.seed(t, "drain", true)

	e.q.drainDue(context.Background())

	if c := e.callCount(); c != 1 {
		t.Fatalf("appels = %d, attendu 1 (backoff doit empêcher le re-lease immédiat)", c)
	}
	if got := e.load(t, cb.ID); got.Attempts != 1 {
		t.Fatalf("attempts = %d, attendu 1", got.Attempts)
	}
}
