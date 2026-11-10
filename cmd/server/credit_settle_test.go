package main

// Tests unitaires du règlement des gains — AUCUNE dépendance à MongoDB ni à l'engine : faux
// ledger et fausse queue. Ils valident l'invariant « aucun callback CREDIT sans entrée Ledger »,
// l'absence de double envoi et la reprise après échec de Begin / Enqueue. Ils ne prouvent PAS
// l'unicité réelle de l'index idempotencyKey (couverte par les tests d'intégration du ledger).

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/tiidiio/223Gaming/internal/product"
	"github.com/tiidiio/223Gaming/provider/wallet"
	"github.com/tiidiio/223Gaming/provider/ledger"
)

var csErrInfra = errors.New("infra: boom")

type csLedger struct {
	mu      sync.Mutex
	fn      func(call int, e ledger.NewEntry) (*ledger.Entry, bool, error)
	calls   int
	entries []ledger.NewEntry
}

func (l *csLedger) Begin(_ context.Context, e ledger.NewEntry) (*ledger.Entry, bool, error) {
	l.mu.Lock()
	l.calls++
	n := l.calls
	l.entries = append(l.entries, e)
	fn := l.fn
	l.mu.Unlock()
	return fn(n, e)
}

func (l *csLedger) callCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls
}

type csQueue struct {
	mu  sync.Mutex
	err error
	cbs []wallet.Callback
}

func (q *csQueue) Enqueue(_ context.Context, cb wallet.Callback) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.cbs = append(q.cbs, cb)
	return q.err
}

func (q *csQueue) count() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.cbs)
}

type csLogs struct {
	mu    sync.Mutex
	lines []string
}

func (l *csLogs) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *csLogs) len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.lines)
}

func csReq() creditRequest {
	return creditRequest{OperatorID: "op1", PlayerID: "pl1", RoundID: "r1", BetID: "bet-0001", AmountMinor: 2500}
}

func csEntryFrom(e ledger.NewEntry, status ledger.EntryStatus) *ledger.Entry {
	return &ledger.Entry{
		TransactionID:  "txn_test1",
		IdempotencyKey: e.IdempotencyKey,
		GameID:         e.GameID,
		OperatorID:     e.OperatorID,
		PlayerID:       e.PlayerID,
		ExternalRef:    e.ExternalRef,
		Type:           e.Type,
		AmountMinor:    e.AmountMinor,
		Status:         status,
	}
}

func csCreated(_ int, e ledger.NewEntry) (*ledger.Entry, bool, error) {
	return csEntryFrom(e, ledger.StatusPending), true, nil
}

// csSettler : spawn synchrone, sleep sans attente — déterministe et instantané.
func csSettler(t *testing.T, led *csLedger, q *csQueue) (*creditSettler, *csLogs) {
	t.Helper()
	logs := &csLogs{}
	s := newCreditSettler(led, q, product.GameID)
	s.logf = logs.logf
	s.sleep = func(time.Duration) {}
	s.spawn = func(f func()) { f() }
	return s, logs
}

func TestCreditSettle_Nominal_BeginThenEnqueueWithTransactionID(t *testing.T) {
	led := &csLedger{fn: csCreated}
	q := &csQueue{}
	s, _ := csSettler(t, led, q)

	if got := s.Settle(csReq()); got != creditQueued {
		t.Fatalf("outcome = %v, attendu creditQueued", got)
	}
	if led.callCount() != 1 || q.count() != 1 {
		t.Fatalf("Begin=%d Enqueue=%d, attendu 1/1", led.callCount(), q.count())
	}

	ne := led.entries[0]
	if ne.Type != ledger.EntryCredit || ne.AmountMinor != 2500 || ne.GameID != product.GameID {
		t.Errorf("NewEntry = %+v", ne)
	}
	if ne.IdempotencyKey != "credit:op1:pl1:bet-0001" {
		t.Errorf("clé d'idempotence = %q", ne.IdempotencyKey)
	}
	if ne.ExternalRef != "round=r1|bet=bet-0001" {
		t.Errorf("ExternalRef = %q", ne.ExternalRef)
	}

	cb := q.cbs[0]
	if cb.TransactionID != "txn_test1" {
		t.Errorf("TransactionID callback = %q, attendu txn_test1", cb.TransactionID)
	}
	if cb.ID != "credit-op1-pl1-bet-0001" || cb.IdempotencyKey != "credit:op1:pl1:bet-0001" {
		t.Errorf("ID/clé callback = %q / %q", cb.ID, cb.IdempotencyKey)
	}
	if cb.Type != wallet.CallbackCredit || cb.Amount != 2500 || cb.RoundID != "r1" || cb.BetID != "bet-0001" || cb.GameID != product.GameID {
		t.Errorf("callback = %+v", cb)
	}
}

func TestCreditSettle_BeginFails_NeverEnqueuesWithoutLedgerEntry_ThenRecovers(t *testing.T) {
	led := &csLedger{fn: func(n int, e ledger.NewEntry) (*ledger.Entry, bool, error) {
		if n <= 3 { // appel synchrone + 2 reprises en échec
			return nil, false, csErrInfra
		}
		return csEntryFrom(e, ledger.StatusPending), true, nil
	}}
	q := &csQueue{}
	s, logs := csSettler(t, led, q)

	// Le spawn est synchrone : Settle exécute aussi toute la reprise.
	if got := s.Settle(csReq()); got != creditDeferred {
		t.Fatalf("outcome = %v, attendu creditDeferred", got)
	}
	if led.callCount() != 4 {
		t.Fatalf("Begin appelé %d fois, attendu 4 (1 synchrone + 3 reprises)", led.callCount())
	}
	if q.count() != 1 {
		t.Fatalf("Enqueue = %d, attendu exactement 1 (après Begin réussi uniquement)", q.count())
	}
	if q.cbs[0].TransactionID != "txn_test1" {
		t.Errorf("le callback doit porter le TransactionID Ledger, obtenu %q", q.cbs[0].TransactionID)
	}
	if logs.len() == 0 {
		t.Error("l'échec de Begin doit être journalisé")
	}
	if len(s.inflight) != 0 {
		t.Errorf("inflight non nettoyé: %d", len(s.inflight))
	}
}

func TestCreditSettle_BeginFailsAndNoRecovery_NeverEnqueues(t *testing.T) {
	led := &csLedger{fn: func(int, ledger.NewEntry) (*ledger.Entry, bool, error) { return nil, false, csErrInfra }}
	q := &csQueue{}
	s, logs := csSettler(t, led, q)
	s.bgMaxAttempts = 5

	if got := s.Settle(csReq()); got != creditDeferred {
		t.Fatalf("outcome = %v", got)
	}
	if q.count() != 0 {
		t.Fatalf("Enqueue = %d : un callback ne doit JAMAIS partir sans entrée Ledger", q.count())
	}
	if led.callCount() != 6 { // 1 synchrone + 5 reprises
		t.Errorf("Begin = %d, attendu 6", led.callCount())
	}
	last := logs.lines[len(logs.lines)-1]
	if want := "op=op1 player=pl1 round=r1 bet=bet-0001 amount=2500"; !contains(last, "CRITIQUE") || !contains(last, want) {
		t.Errorf("dernier log = %q : doit être CRITIQUE et contenir %q pour rejeu manuel", last, want)
	}
	if len(s.inflight) != 0 {
		t.Errorf("inflight non nettoyé: %d", len(s.inflight))
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestCreditSettle_BeginPermanentError_NoRetryNoEnqueue(t *testing.T) {
	led := &csLedger{fn: func(int, ledger.NewEntry) (*ledger.Entry, bool, error) {
		return nil, false, fmt.Errorf("wrap: %w", ledger.ErrOperatorIDRequired)
	}}
	q := &csQueue{}
	s, _ := csSettler(t, led, q)

	if got := s.Settle(csReq()); got != creditNeedsReview {
		t.Fatalf("outcome = %v, attendu creditNeedsReview", got)
	}
	if led.callCount() != 1 || q.count() != 0 {
		t.Errorf("Begin=%d Enqueue=%d, attendu 1/0", led.callCount(), q.count())
	}
}

func TestCreditSettle_EnqueueFails_EntryStaysForRecovery_NoSecondBegin(t *testing.T) {
	led := &csLedger{fn: csCreated}
	q := &csQueue{err: csErrInfra}
	s, logs := csSettler(t, led, q)

	if got := s.Settle(csReq()); got != creditRecoverable {
		t.Fatalf("outcome = %v, attendu creditRecoverable", got)
	}
	if led.callCount() != 1 || q.count() != 1 {
		t.Errorf("Begin=%d Enqueue=%d, attendu 1/1 (pas de boucle de renvoi)", led.callCount(), q.count())
	}
	if logs.len() == 0 {
		t.Error("l'échec d'Enqueue doit être journalisé")
	}
}

func TestCreditSettle_ExistingPending_NoSecondSend(t *testing.T) {
	led := &csLedger{fn: func(_ int, e ledger.NewEntry) (*ledger.Entry, bool, error) {
		return csEntryFrom(e, ledger.StatusPending), false, nil
	}}
	q := &csQueue{}
	s, _ := csSettler(t, led, q)

	if got := s.Settle(csReq()); got != creditAlreadyKnown {
		t.Fatalf("outcome = %v, attendu creditAlreadyKnown", got)
	}
	if q.count() != 0 {
		t.Errorf("Enqueue = %d : une entrée préexistante ne doit pas être renvoyée d'ici", q.count())
	}
}

func TestCreditSettle_ExistingConfirmed_NoSend(t *testing.T) {
	led := &csLedger{fn: func(_ int, e ledger.NewEntry) (*ledger.Entry, bool, error) {
		return csEntryFrom(e, ledger.StatusConfirmed), false, nil
	}}
	q := &csQueue{}
	s, _ := csSettler(t, led, q)

	if got := s.Settle(csReq()); got != creditAlreadyKnown {
		t.Fatalf("outcome = %v", got)
	}
	if q.count() != 0 {
		t.Errorf("Enqueue = %d, attendu 0", q.count())
	}
}

func TestCreditSettle_ExistingFailed_NeedsReviewNoSend(t *testing.T) {
	led := &csLedger{fn: func(_ int, e ledger.NewEntry) (*ledger.Entry, bool, error) {
		return csEntryFrom(e, ledger.StatusFailed), false, nil
	}}
	q := &csQueue{}
	s, logs := csSettler(t, led, q)

	if got := s.Settle(csReq()); got != creditNeedsReview {
		t.Fatalf("outcome = %v, attendu creditNeedsReview", got)
	}
	if q.count() != 0 {
		t.Errorf("Enqueue = %d, attendu 0", q.count())
	}
	if logs.len() == 0 {
		t.Error("un gain FAILED doit être journalisé")
	}
}

func TestCreditSettle_ExistingMismatch_NeedsReviewNoSend(t *testing.T) {
	cases := map[string]func(e *ledger.Entry){
		"montant":   func(e *ledger.Entry) { e.AmountMinor = 9999 },
		"type":      func(e *ledger.Entry) { e.Type = ledger.EntryDebit },
		"opérateur": func(e *ledger.Entry) { e.OperatorID = "autre" },
		"joueur":    func(e *ledger.Entry) { e.PlayerID = "autre" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			led := &csLedger{fn: func(_ int, e ledger.NewEntry) (*ledger.Entry, bool, error) {
				ex := csEntryFrom(e, ledger.StatusPending)
				mutate(ex)
				return ex, false, nil
			}}
			q := &csQueue{}
			s, _ := csSettler(t, led, q)
			if got := s.Settle(csReq()); got != creditNeedsReview {
				t.Fatalf("outcome = %v, attendu creditNeedsReview", got)
			}
			if q.count() != 0 {
				t.Errorf("Enqueue = %d, attendu 0", q.count())
			}
		})
	}
}

func TestCreditSettle_NonPositiveAmount_NoBegin(t *testing.T) {
	for _, amt := range []int64{0, -1} {
		led := &csLedger{fn: csCreated}
		q := &csQueue{}
		s, _ := csSettler(t, led, q)
		r := csReq()
		r.AmountMinor = amt
		if got := s.Settle(r); got != creditNeedsReview {
			t.Errorf("amount=%d outcome = %v", amt, got)
		}
		if led.callCount() != 0 || q.count() != 0 {
			t.Errorf("amount=%d Begin=%d Enqueue=%d, attendu 0/0", amt, led.callCount(), q.count())
		}
	}
}

func TestCreditSettle_BackgroundRetryDeduplicatedPerKey(t *testing.T) {
	led := &csLedger{fn: func(int, ledger.NewEntry) (*ledger.Entry, bool, error) { return nil, false, csErrInfra }}
	q := &csQueue{}
	s, _ := csSettler(t, led, q)

	var spawned []func()
	s.spawn = func(f func()) { spawned = append(spawned, f) } // capturé, non exécuté

	s.Settle(csReq())
	s.Settle(csReq())
	if len(spawned) != 1 {
		t.Fatalf("reprises lancées = %d, attendu 1 pour une même clé d'idempotence", len(spawned))
	}

	other := csReq()
	other.BetID = "bet-0002"
	s.Settle(other)
	if len(spawned) != 2 {
		t.Fatalf("reprises lancées = %d, attendu 2 (clé différente)", len(spawned))
	}
}

func TestCreditSettle_ConcurrentSameKey_ExactlyOneEnqueue(t *testing.T) {
	// Faux ledger imitant l'index unique : le premier Begin crée, les suivants relisent l'entrée.
	var mu sync.Mutex
	seen := false
	led := &csLedger{fn: func(_ int, e ledger.NewEntry) (*ledger.Entry, bool, error) {
		mu.Lock()
		defer mu.Unlock()
		if !seen {
			seen = true
			return csEntryFrom(e, ledger.StatusPending), true, nil
		}
		return csEntryFrom(e, ledger.StatusPending), false, nil
	}}
	q := &csQueue{}
	s, _ := csSettler(t, led, q)

	const n = 50
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			s.Settle(csReq())
		}()
	}
	close(start)
	wg.Wait()

	if q.count() != 1 {
		t.Fatalf("Enqueue = %d, attendu exactement 1 pour %d règlements concurrents de la même clé", q.count(), n)
	}
}

func TestCreditSettle_BeginSucceededButResponseLost_RetryFindsPendingEntryNoSend(t *testing.T) {
	// 1er Begin : l'insertion a eu lieu mais la réponse est perdue (erreur) ; la reprise relit
	// l'entrée PENDING (created=false). Rien n'est enfilé d'ici : le Recovery reprend l'entrée.
	led := &csLedger{fn: func(n int, e ledger.NewEntry) (*ledger.Entry, bool, error) {
		if n == 1 {
			return nil, false, csErrInfra
		}
		return csEntryFrom(e, ledger.StatusPending), false, nil
	}}
	q := &csQueue{}
	s, _ := csSettler(t, led, q)

	if got := s.Settle(csReq()); got != creditDeferred {
		t.Fatalf("outcome = %v", got)
	}
	if q.count() != 0 {
		t.Errorf("Enqueue = %d, attendu 0 (entrée préexistante, reprise par le Recovery)", q.count())
	}
	if led.callCount() != 2 {
		t.Errorf("Begin = %d, attendu 2", led.callCount())
	}
}

func TestPermanentBeginError(t *testing.T) {
	for _, e := range []error{
		ledger.ErrIdempotencyKeyRequired, ledger.ErrGameIDRequired, ledger.ErrOperatorIDRequired,
		ledger.ErrPlayerIDRequired, ledger.ErrInvalidAmount, ledger.ErrInvalidEntryType,
		fmt.Errorf("x: %w", ledger.ErrInvalidAmount),
	} {
		if !permanentBeginError(e) {
			t.Errorf("%v devrait être permanente", e)
		}
	}
	for _, e := range []error{csErrInfra, context.DeadlineExceeded, ledger.ErrStateConflict} {
		if permanentBeginError(e) {
			t.Errorf("%v ne devrait pas être permanente", e)
		}
	}
}
