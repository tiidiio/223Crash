package main

// Tests du chemin BET/rejeu SANS MongoDB : moteur de test construit sur les vrais RoundManager et
// BetManager (phases pilotées à la main, donc déterministes), faux Ledger en mémoire avec la
// sémantique d'idempotence (operatorId, idempotencyKey), faux wallet et fausse queue qui comptent
// chaque DEBIT / CREDIT / ROLLBACK. Ils ne prouvent PAS l'unicité réelle de l'index Mongo (couverte
// par les tests d'intégration du ledger).

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/tiidiio/223Crash/internal/engine"
	"github.com/tiidiio/223Crash/internal/game"
	"github.com/tiidiio/223Gaming/provider"
	"github.com/tiidiio/223Gaming/provider/ledger"
	"github.com/tiidiio/223Gaming/provider/wallet"
)

// ---------- faux Ledger ----------

type rpLedger struct {
	mu    sync.Mutex
	seq   int
	byKey map[string]*ledger.Entry
	byTxn map[ledger.TransactionID]*ledger.Entry
}

func newRPLedger() *rpLedger {
	return &rpLedger{
		byKey: map[string]*ledger.Entry{},
		byTxn: map[ledger.TransactionID]*ledger.Entry{},
	}
}

func (l *rpLedger) Begin(_ context.Context, e ledger.NewEntry) (*ledger.Entry, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	k := string(e.OperatorID) + "|" + string(e.IdempotencyKey)
	if ex, ok := l.byKey[k]; ok {
		cp := *ex
		return &cp, false, nil
	}
	l.seq++
	en := &ledger.Entry{
		TransactionID:       ledger.TransactionID(fmt.Sprintf("txn_%d", l.seq)),
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
	l.byKey[k] = en
	l.byTxn[en.TransactionID] = en
	cp := *en
	return &cp, true, nil
}

func (l *rpLedger) resolve(id ledger.TransactionID, to ledger.EntryStatus, bal *int64) (*ledger.Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	en, ok := l.byTxn[id]
	if !ok {
		return nil, ledger.ErrEntryNotFound
	}
	switch en.Status {
	case ledger.StatusPending:
		en.Status = to
		en.OperatorBalanceAfterMinor = bal
	case to:
	default:
		return nil, ledger.ErrStateConflict
	}
	cp := *en
	return &cp, nil
}

func (l *rpLedger) Confirm(_ context.Context, id ledger.TransactionID, bal *int64) (*ledger.Entry, error) {
	return l.resolve(id, ledger.StatusConfirmed, bal)
}

func (l *rpLedger) Fail(_ context.Context, id ledger.TransactionID, _ string) (*ledger.Entry, error) {
	return l.resolve(id, ledger.StatusFailed, nil)
}

func (l *rpLedger) GetByTransactionID(_ context.Context, id ledger.TransactionID) (*ledger.Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	en, ok := l.byTxn[id]
	if !ok {
		return nil, ledger.ErrEntryNotFound
	}
	cp := *en
	return &cp, nil
}

func (l *rpLedger) GetByIdempotencyKey(_ context.Context, op ledger.OperatorID, key ledger.IdempotencyKey) (*ledger.Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	en, ok := l.byKey[string(op)+"|"+string(key)]
	if !ok {
		return nil, ledger.ErrEntryNotFound
	}
	cp := *en
	return &cp, nil
}

func (l *rpLedger) ListPendingOlderThan(context.Context, time.Time) ([]ledger.Entry, error) {
	return nil, nil
}

func (l *rpLedger) count(t ledger.EntryType) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, e := range l.byTxn {
		if e.Type == t {
			n++
		}
	}
	return n
}

// ---------- faux wallet et fausse queue ----------

type rpWallet struct {
	mu      sync.Mutex
	debits  int
	delay   time.Duration
	onDebit func()
}

func (w *rpWallet) Debit(_ context.Context, _ provider.GameID, _ provider.TransactionID, _, _, _, _ string, amount int64) (int64, wallet.Outcome, error) {
	w.mu.Lock()
	w.debits++
	d, hook := w.delay, w.onDebit
	w.mu.Unlock()
	if d > 0 {
		time.Sleep(d)
	}
	if hook != nil {
		hook()
	}
	return 1_000_000 - amount, wallet.OutcomeAccepted, nil
}

func (w *rpWallet) Send(context.Context, wallet.Callback) (int64, wallet.Outcome, error) {
	return 0, wallet.OutcomeAccepted, nil
}

func (w *rpWallet) GetBalance(context.Context, string, string) (int64, error) {
	return 1_000_000, nil
}

func (w *rpWallet) debitCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.debits
}

type rpQueue struct {
	mu  sync.Mutex
	cbs []wallet.Callback
}

func (q *rpQueue) Enqueue(_ context.Context, cb wallet.Callback) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.cbs = append(q.cbs, cb)
	return nil
}

func (q *rpQueue) Run(ctx context.Context) { <-ctx.Done() }

func (q *rpQueue) of(t wallet.CallbackType) []wallet.Callback {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []wallet.Callback
	for _, cb := range q.cbs {
		if cb.Type == t {
			out = append(out, cb)
		}
	}
	return out
}

// ---------- faux moteur (vrais RoundManager / BetManager) ----------

type rpEngine struct {
	rm *game.RoundManager
	bm *game.BetManager

	mu        sync.Mutex
	mult      float64
	forceOpen bool // CurrentRound annonce BETTING_OPEN même si les mises sont closes (simule la course)
}

func newRPEngine() *rpEngine {
	return &rpEngine{rm: game.NewRoundManager(64), bm: game.NewBetManager(), mult: 1.0}
}

func rpMust(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("transition moteur de test: %v", err)
	}
}

func (e *rpEngine) start(t *testing.T) {
	t.Helper()
	now := time.Now()
	r, err := e.rm.CreateRound(now)
	rpMust(t, err)
	rpMust(t, e.bm.OpenRound(r.ID))
	rpMust(t, e.rm.OpenBetting(now))
}

func (e *rpEngine) closeBetting(t *testing.T) {
	t.Helper()
	rpMust(t, e.bm.CloseBetting())
	rpMust(t, e.rm.CloseBetting(time.Now()))
}

func (e *rpEngine) run(t *testing.T) {
	t.Helper()
	rpMust(t, e.bm.StartRound())
	rpMust(t, e.rm.Start(time.Now()))
}

func (e *rpEngine) crash(t *testing.T) {
	t.Helper()
	_, err := e.bm.Crash(time.Now())
	rpMust(t, err)
	rpMust(t, e.rm.Crash(time.Now()))
}

func (e *rpEngine) settle(t *testing.T) {
	t.Helper()
	rpMust(t, e.rm.Settle(time.Now()))
}

func (e *rpEngine) reset(t *testing.T) {
	t.Helper()
	rpMust(t, e.bm.ResetRound())
}

func (e *rpEngine) setMult(m float64) {
	e.mu.Lock()
	e.mult = m
	e.mu.Unlock()
}

func (e *rpEngine) setForceOpen(v bool) {
	e.mu.Lock()
	e.forceOpen = v
	e.mu.Unlock()
}

func (e *rpEngine) Events() <-chan engine.Event { return nil }

func (e *rpEngine) CurrentRound() (*game.Round, error) {
	r, err := e.rm.CurrentRound()
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	if e.forceOpen {
		r.State = game.StateBettingOpen
	}
	e.mu.Unlock()
	return r, nil
}

func (e *rpEngine) CurrentFairness() (string, string, int64) { return "hash", "client", 1 }

func (e *rpEngine) CurrentMultiplier() float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.mult
}

func (e *rpEngine) PlaceBet(userID string, panel game.BetPanel, amount int64, target float64) (*game.Bet, error) {
	return e.bm.PlaceBet(userID, panel, amount, target)
}

func (e *rpEngine) GetPanelBet(userID string, panel game.BetPanel) (game.Bet, error) {
	return e.bm.GetPanelBet(userID, panel)
}

func (e *rpEngine) Cashout(betID string) (*game.Bet, error) {
	return e.bm.Cashout(betID, e.CurrentMultiplier(), time.Now())
}

func (e *rpEngine) UserRoundBets(userID string) []game.Bet { return e.bm.UserRoundBets(userID) }

var (
	_ gameEngine = (*rpEngine)(nil)
	_ ledgerAPI  = (*rpLedger)(nil)
	_ walletAPI  = (*rpWallet)(nil)
	_ queueAPI   = (*rpQueue)(nil)
)

// ---------- environnement ----------

type rpEnv struct {
	gw  *gateway
	fe  *rpEngine
	led *rpLedger
	w   *rpWallet
	q   *rpQueue
	c   *client
}

func newRPEnv(t *testing.T) *rpEnv {
	t.Helper()
	// La config WS est chargée paresseusement et fail-closed : voir gwtNewEnv.
	t.Setenv("ALLOWED_OPERATORS", "op1")
	fe := newRPEngine()
	led := newRPLedger()
	w := &rpWallet{}
	q := &rpQueue{}
	gw := &gateway{
		eng:          fe,
		walletClient: w,
		walletQueue:  q,
		ledger:       led,
		credits:      newCreditSettler(led, q, productGameID),
		clients:      make(map[string]*client),
	}
	fe.start(t)
	return &rpEnv{gw: gw, fe: fe, led: led, w: w, q: q, c: gwtClient("op1", "player")}
}

func (e *rpEnv) bet(id string) {
	e.gw.handleBet(e.c, clientMessage{Action: "bet", BetID: id, Slot: "A", Amount: 10})
}

func (e *rpEnv) expect(t *testing.T, want string) map[string]any {
	t.Helper()
	m := gwtRecv(t, e.c, 2*time.Second)
	if gwtType(m) != want {
		t.Fatalf("attendu %s, obtenu %s (reason=%q)", want, gwtType(m), gwtReason(m))
	}
	return m
}

func (e *rpEnv) expectUntil(t *testing.T, want string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		m := gwtRecv(t, e.c, 2*time.Second)
		if gwtType(m) == want {
			return m
		}
	}
	t.Fatalf("message %s jamais reçu", want)
	return nil
}

func (e *rpEnv) myBets() []game.Bet { return e.fe.bm.UserRoundBets("op1:player") }

func (e *rpEnv) money(t *testing.T, debits, credits, rollbacks int) {
	t.Helper()
	if got := e.w.debitCount(); got != debits {
		t.Errorf("DEBIT wallet = %d, attendu %d", got, debits)
	}
	if got := len(e.q.of(wallet.CallbackCredit)); got != credits {
		t.Errorf("CREDIT enfilés = %d, attendu %d", got, credits)
	}
	if got := len(e.q.of(wallet.CallbackRollback)); got != rollbacks {
		t.Errorf("ROLLBACK enfilés = %d, attendu %d", got, rollbacks)
	}
	if got := e.led.count(ledger.EntryRollback); got != rollbacks {
		t.Errorf("entrées Ledger ROLLBACK = %d, attendu %d", got, rollbacks)
	}
}

// ---------- tests ----------

func TestReplay_FirstBetAccepted(t *testing.T) {
	env := newRPEnv(t)
	env.bet(gwtBetID(t))
	env.expect(t, "BET_ACCEPTED")
	env.money(t, 1, 0, 0)
	if n := len(env.myBets()); n != 1 {
		t.Fatalf("paris moteur = %d, attendu 1", n)
	}
}

func TestReplay_DuringBettingOpen_ReusesSameBet(t *testing.T) {
	env := newRPEnv(t)
	id := gwtBetID(t)
	env.bet(id)
	env.expect(t, "BET_ACCEPTED")
	env.bet(id)
	env.expect(t, "BET_ACCEPTED")
	env.money(t, 1, 0, 0)
	if n := len(env.myBets()); n != 1 {
		t.Fatalf("paris moteur = %d, attendu 1 (aucun second pari)", n)
	}
}

// Rejeu après chaque changement de phase : jamais de second débit, jamais de remboursement, et le
// pari d'origine est intact (vivant tant que le round n'est pas crashé, perdu ensuite).
func TestReplay_AfterPhaseChange_NoRefundNoRedebit(t *testing.T) {
	cases := []struct {
		name      string
		advance   func(*testing.T, *rpEngine)
		wantBets  int
		wantState game.BetState
	}{
		{"BETTING_CLOSED", func(t *testing.T, e *rpEngine) { e.closeBetting(t) }, 1, game.BetPending},
		{"RUNNING", func(t *testing.T, e *rpEngine) { e.closeBetting(t); e.run(t) }, 1, game.BetPending},
		{"CRASHED", func(t *testing.T, e *rpEngine) { e.closeBetting(t); e.run(t); e.crash(t) }, 1, game.BetLost},
		{"SETTLED", func(t *testing.T, e *rpEngine) { e.closeBetting(t); e.run(t); e.crash(t); e.settle(t) }, 1, game.BetLost},
		{"SETTLED_APRES_RESET", func(t *testing.T, e *rpEngine) {
			e.closeBetting(t)
			e.run(t)
			e.crash(t)
			e.settle(t)
			e.reset(t)
		}, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newRPEnv(t)
			id := gwtBetID(t)
			env.bet(id)
			env.expect(t, "BET_ACCEPTED")

			tc.advance(t, env.fe)

			env.bet(id) // rejeu du même BET
			m := env.expect(t, "BET_REJECTED")
			if r := gwtReason(m); r != "paris fermés" {
				t.Errorf("raison = %q, attendu %q", r, "paris fermés")
			}
			env.money(t, 1, 0, 0)

			bets := env.myBets()
			if len(bets) != tc.wantBets {
				t.Fatalf("paris moteur = %d, attendu %d", len(bets), tc.wantBets)
			}
			if tc.wantBets == 1 && bets[0].State != tc.wantState {
				t.Errorf("état du pari = %s, attendu %s", bets[0].State, tc.wantState)
			}
		})
	}
}

// Un pari vivant doit rester encaissable après une tentative de rejeu, et payé une seule fois.
func TestReplay_LiveBet_StillCashesOutOnce(t *testing.T) {
	env := newRPEnv(t)
	id := gwtBetID(t)
	env.bet(id)
	env.expect(t, "BET_ACCEPTED")
	env.fe.closeBetting(t)
	env.fe.run(t)
	env.fe.setMult(2.0)

	env.bet(id)
	env.expect(t, "BET_REJECTED")
	env.money(t, 1, 0, 0)

	env.gw.handleCashout(env.c, clientMessage{Action: "cashout", Slot: "A"})
	env.expectUntil(t, "CASHOUT_ACCEPTED")
	env.gw.handleCashout(env.c, clientMessage{Action: "cashout", Slot: "A"})
	env.expectUntil(t, "CASHOUT_REJECTED")

	env.money(t, 1, 1, 0)
	if cr := env.q.of(wallet.CallbackCredit); len(cr) == 1 && cr[0].Amount != 2000 {
		t.Errorf("gain = %d centimes, attendu 2000 (1000 x 2.00)", cr[0].Amount)
	}
}

// Débit réellement accepté, puis placement impossible (les mises ferment pendant le DEBIT) :
// c'est le seul cas où un ROLLBACK est légitime, avec un TransactionID valide.
func TestFreshDebit_PlaceFails_RollsBackOnce(t *testing.T) {
	env := newRPEnv(t)
	env.w.onDebit = func() { _ = env.fe.bm.CloseBetting() }

	env.bet(gwtBetID(t))
	m := env.expect(t, "BET_REJECTED")
	if r := gwtReason(m); r != "paris fermés" {
		t.Errorf("raison = %q, attendu %q", r, "paris fermés")
	}
	env.money(t, 1, 0, 1)

	rbs := env.q.of(wallet.CallbackRollback)
	if len(rbs) == 1 {
		if rbs[0].TransactionID == "" {
			t.Error("ROLLBACK enfilé sans TransactionID")
		}
		if rbs[0].Amount != 1000 {
			t.Errorf("montant ROLLBACK = %d, attendu 1000", rbs[0].Amount)
		}
	}
	if n := len(env.myBets()); n != 0 {
		t.Errorf("paris moteur = %d, attendu 0", n)
	}
}

// Régression C-1 : rejeu d'un DEBIT confirmé qui franchit le contrôle de phase (course de
// fermeture) puis échoue au placement. Aucun remboursement, aucun nouveau débit.
func TestReplay_ConfirmedDebit_PlaceFails_NeverRollsBack(t *testing.T) {
	env := newRPEnv(t)
	id := gwtBetID(t)

	round, err := env.fe.CurrentRound()
	rpMust(t, err)
	e, created, err := env.led.Begin(context.Background(), ledger.NewEntry{
		GameID:         productGameID,
		OperatorID:     "op1",
		PlayerID:       "player",
		ExternalRef:    ledger.ExternalRef(encodeDebitContext(round.ID, "A", 1000, noAutoCashoutSentinel)),
		IdempotencyKey: ledger.IdempotencyKey("debit:op1:player:" + id),
		Type:           ledger.EntryDebit,
		AmountMinor:    1000,
	})
	if err != nil || !created {
		t.Fatalf("pré-chargement du DEBIT: created=%v err=%v", created, err)
	}
	if _, err := env.led.Confirm(context.Background(), e.TransactionID, nil); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	env.fe.closeBetting(t)
	env.fe.setForceOpen(true) // le rejeu passe le contrôle de phase, PlaceBet voit des mises closes

	env.bet(id)
	env.expect(t, "BET_REJECTED")
	env.money(t, 0, 0, 0)
	if n := len(env.myBets()); n != 0 {
		t.Errorf("paris moteur = %d, attendu 0", n)
	}
}

// 20 requêtes concurrentes avec le même betId : un seul DEBIT, un seul pari, 20 réponses
// BET_ACCEPTED, aucun remboursement.
func TestReplay_ConcurrentSameBetID_SingleDebitSingleBet(t *testing.T) {
	env := newRPEnv(t)
	env.w.delay = 30 * time.Millisecond
	id := gwtBetID(t)

	const n = 20
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			env.bet(id)
		}()
	}
	close(start)
	wg.Wait()

	for i := 0; i < n; i++ {
		m := gwtRecv(t, env.c, 3*time.Second)
		if gwtType(m) != "BET_ACCEPTED" {
			t.Errorf("réponse %d : type=%s reason=%q", i, gwtType(m), gwtReason(m))
		}
	}
	env.money(t, 1, 0, 0)
	if got := len(env.myBets()); got != 1 {
		t.Errorf("paris moteur = %d, attendu 1", got)
	}
}
