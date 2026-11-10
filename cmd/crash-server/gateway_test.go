package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/tiidiio/223Gaming/internal/engine"
	"github.com/tiidiio/223Gaming/internal/game"
	"github.com/tiidiio/223Gaming/provider/ledger"
	"github.com/tiidiio/223Gaming/provider/wallet"
)

// Tests d'intégration de handleBet : engine réel + Ledger sur MongoDB (MONGO_TEST_URI, base jetable
// t223_gw_*) + faux opérateur HTTP qui enregistre chaque callback reçu. Skippés sans MONGO_TEST_URI.

// ---------- faux opérateur ----------

type gwtOperator struct {
	mu          sync.Mutex
	calls       []wallet.Callback
	debitStatus int           // 0 => 200
	debitDelay  time.Duration // appliqué aux DEBIT uniquement
}

func (o *gwtOperator) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/wallet/callback" {
		http.NotFound(w, r)
		return
	}
	var cb wallet.Callback
	if err := json.NewDecoder(r.Body).Decode(&cb); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	o.mu.Lock()
	o.calls = append(o.calls, cb)
	status := http.StatusOK
	var delay time.Duration
	if cb.Type == wallet.CallbackDebit {
		if o.debitStatus != 0 {
			status = o.debitStatus
		}
		delay = o.debitDelay
	}
	o.mu.Unlock()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(wallet.CallbackResponse{Balance: 5_000_000})
}

func (o *gwtOperator) setDebit(status int, delay time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.debitStatus, o.debitDelay = status, delay
}

func (o *gwtOperator) filter(keep func(wallet.Callback) bool) []wallet.Callback {
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []wallet.Callback
	for _, cb := range o.calls {
		if keep(cb) {
			out = append(out, cb)
		}
	}
	return out
}

func gwtIsDebit(cb wallet.Callback) bool    { return cb.Type == wallet.CallbackDebit }
func gwtIsRollback(cb wallet.Callback) bool { return cb.Type == wallet.CallbackRollback }

// ---------- environnement ----------

type gwtEnv struct {
	gw    *gateway
	eng   *engine.CrashEngine
	led   *ledger.Ledger
	op    *gwtOperator
	slot  string
	panel game.BetPanel
}

func gwtNewEnv(t *testing.T, op *gwtOperator, bettingWindow time.Duration) *gwtEnv {
	t.Helper()
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI absent : test d'intégration gateway ignoré")
	}
	// La config de sécurité WS est chargée paresseusement (sync.Once) à la première utilisation,
	// pendant handleBet, et elle est fail-closed (log.Fatal) sans opérateur autorisé. Les tests
	// utilisent l'opérateur "op1" ; la première valeur chargée vaut pour tout le process de test.
	t.Setenv("ALLOWED_OPERATORS", "op1")
	slot, panel := gwtPickSlot(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	mc, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatalf("mongo connect: %v", err)
	}
	rb := make([]byte, 6)
	if _, err := rand.Read(rb); err != nil {
		t.Fatalf("rand: %v", err)
	}
	db := mc.Database("t223_gw_" + hex.EncodeToString(rb))
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

	srv := httptest.NewServer(op)
	t.Cleanup(srv.Close)
	cli := wallet.NewClient(srv.URL, "test-secret")

	q, err := wallet.NewQueue(ctx, db, cli, led)
	if err != nil {
		t.Fatalf("wallet.NewQueue: %v", err)
	}

	cfg := engine.DefaultConfig()
	cfg.BettingDuration = bettingWindow
	cfg.CrashedDuration = 5 * time.Second // aucun nouveau round pendant la durée d'un test
	cfg.TickInterval = 20 * time.Millisecond
	cfg.EventBuffer = 256
	cfg.InitialClientSeed = "gw-test-seed"
	eng, err := engine.NewCrashEngine(cfg)
	if err != nil {
		t.Fatalf("NewCrashEngine: %v", err)
	}

	runCtx, stop := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		q.Run(runCtx)
	}()
	go func() {
		defer wg.Done()
		_ = eng.Run(runCtx)
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-runCtx.Done():
				return
			case _, ok := <-eng.Events():
				if !ok {
					return
				}
			}
		}
	}()
	t.Cleanup(func() {
		stop()
		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Log("arrêt engine/queue : délai dépassé")
		}
	})

	gw := newGateway(eng, "gw-test-jwt-secret-gw-test-jwt-secret", cli, q, led)
	return &gwtEnv{gw: gw, eng: eng, led: led, op: op, slot: slot, panel: panel}
}

// ---------- helpers ----------

func gwtPickSlot(t *testing.T) (string, game.BetPanel) {
	t.Helper()
	for _, s := range []string{"A", "B", "1", "2", "a", "b"} {
		if p, ok := slotToPanel(s); ok {
			return s, p
		}
	}
	t.Fatalf("aucun slot connu parmi A/B/1/2 — adapter gwtPickSlot à slotToPanel")
	var zero game.BetPanel
	return "", zero
}

func gwtBetID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	id := fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
	if !validBetID(id) {
		t.Fatalf("validBetID refuse un UUID v4 (%s) — adapter gwtBetID", id)
	}
	return id
}

func gwtClient(operatorID, playerID string) *client {
	return &client{
		playerID:   playerID,
		operatorID: operatorID,
		send:       make(chan []byte, 256),
		done:       make(chan struct{}),
		activeBets: make(map[game.BetPanel]*game.Bet),
	}
}

// gwtRecv renvoie le prochain message de réponse du client. Les messages BALANCE sont des
// notifications de solde asynchrones (rafraîchissement après mouvement wallet) : elles peuvent
// précéder ou suivre BET_ACCEPTED / BET_REJECTED et sont ignorées ici.
func gwtRecv(t *testing.T, c *client, d time.Duration) map[string]any {
	t.Helper()
	timeout := time.After(d)
	for {
		select {
		case b := <-c.send:
			var m map[string]any
			if err := json.Unmarshal(b, &m); err != nil {
				t.Fatalf("réponse non JSON: %v (%s)", err, b)
			}
			if gwtType(m) == "BALANCE" {
				continue
			}
			return m
		case <-timeout:
			t.Fatalf("aucune réponse client sous %s", d)
			return nil
		}
	}
}

func gwtType(m map[string]any) string {
	s, _ := m["type"].(string)
	return s
}

func gwtReason(m map[string]any) string {
	d, _ := m["data"].(map[string]any)
	s, _ := d["reason"].(string)
	return s
}

func gwtEventually(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("délai dépassé (%s) : %s", d, what)
}

// gwtWaitOpen attend la phase de mises sans dépendre des champs internes de Round : un pari
// « sonde » d'un joueur dédié n'est accepté que lorsque les mises sont ouvertes.
func gwtWaitOpen(t *testing.T, env *gwtEnv) {
	t.Helper()
	gwtEventually(t, 5*time.Second, "round en phase de mises", func() bool {
		_, err := env.eng.PlaceBet("probe-player", env.panel, 1000, 2.0)
		return err == nil
	})
}

// ---------- tests ----------

func TestGatewayHandleBet_ConcurrentSameBetID(t *testing.T) {
	op := &gwtOperator{}
	env := gwtNewEnv(t, op, 60*time.Second)
	gwtWaitOpen(t, env)

	const n = 20
	c := gwtClient("op1", "player-conc")
	betID := gwtBetID(t)
	msg := clientMessage{Action: "bet", BetID: betID, Slot: env.slot, Amount: 10}

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			env.gw.handleBet(c, msg)
		}()
	}
	close(start)
	wg.Wait()

	for i := 0; i < n; i++ {
		m := gwtRecv(t, c, 2*time.Second)
		if gwtType(m) != "BET_ACCEPTED" {
			t.Errorf("réponse %d inattendue : type=%s reason=%q", i, gwtType(m), gwtReason(m))
		}
	}

	debits := op.filter(gwtIsDebit)
	if len(debits) != 1 {
		t.Fatalf("attendu exactement 1 appel wallet DEBIT, obtenu %d", len(debits))
	}
	d := debits[0]
	if want := fmt.Sprintf("debit:%s:%s:%s", "op1", "player-conc", betID); d.IdempotencyKey != want {
		t.Errorf("idempotency key = %q, attendu %q", d.IdempotencyKey, want)
	}
	if d.Amount != 1000 {
		t.Errorf("montant débité = %d centimes, attendu 1000", d.Amount)
	}
	if got := len(env.eng.UserRoundBets("player-conc")); got != 1 {
		t.Errorf("paris engine = %d, attendu 1", got)
	}
	entry, err := env.led.GetByTransactionID(context.Background(), d.TransactionID)
	if err != nil {
		t.Fatalf("ledger GetByTransactionID: %v", err)
	}
	if entry.Status != ledger.StatusConfirmed {
		t.Errorf("statut ledger = %v, attendu CONFIRMED", entry.Status)
	}
}

func TestGatewayHandleBet_PlaceBetFailsAfterDebit_RollsBack(t *testing.T) {
	// Fenêtre de mises 300 ms, débit retardé de 900 ms : quand le DEBIT revient, les mises sont
	// closes et PlaceBet échoue (erreur autre que ErrBetAlreadyExists) => ROLLBACK attendu.
	op := &gwtOperator{debitDelay: 900 * time.Millisecond}
	env := gwtNewEnv(t, op, 300*time.Millisecond)
	gwtWaitOpen(t, env)

	c := gwtClient("op1", "player-rb")
	betID := gwtBetID(t)
	env.gw.handleBet(c, clientMessage{Action: "bet", BetID: betID, Slot: env.slot, Amount: 10})

	m := gwtRecv(t, c, 5*time.Second)
	if gwtType(m) != "BET_REJECTED" {
		t.Fatalf("attendu BET_REJECTED, obtenu %s (reason=%q)", gwtType(m), gwtReason(m))
	}

	debits := op.filter(gwtIsDebit)
	if len(debits) != 1 {
		t.Fatalf("attendu 1 DEBIT, obtenu %d", len(debits))
	}
	d := debits[0]

	gwtEventually(t, 20*time.Second, "ROLLBACK reçu par l'opérateur", func() bool {
		return len(op.filter(gwtIsRollback)) >= 1
	})
	rbs := op.filter(gwtIsRollback)
	if len(rbs) != 1 {
		t.Fatalf("attendu 1 ROLLBACK, obtenu %d", len(rbs))
	}
	rb := rbs[0]
	if want := fmt.Sprintf("rollback-%s", d.TransactionID); rb.ID != want {
		t.Errorf("rollback.ID = %q, attendu %q", rb.ID, want)
	}
	if want := fmt.Sprintf("rollback:%s:%s:%s", "op1", "player-rb", d.TransactionID); rb.IdempotencyKey != want {
		t.Errorf("rollback idempotency key = %q, attendu %q", rb.IdempotencyKey, want)
	}
	if rb.Amount != d.Amount || rb.BetID != betID {
		t.Errorf("rollback amount/betId = %d/%s, attendu %d/%s", rb.Amount, rb.BetID, d.Amount, betID)
	}

	entry, err := env.led.GetByTransactionID(context.Background(), d.TransactionID)
	if err != nil {
		t.Fatalf("ledger GetByTransactionID: %v", err)
	}
	if entry.Status != ledger.StatusConfirmed {
		t.Errorf("DEBIT ledger = %v, attendu CONFIRMED (le rollback est une entrée distincte)", entry.Status)
	}
	if got := len(env.eng.UserRoundBets("player-rb")); got != 0 {
		t.Errorf("paris engine = %d, attendu 0", got)
	}
}

func TestGatewayHandleBet_UnknownOutcome_StaysPendingAndNeverReplays(t *testing.T) {
	op := &gwtOperator{debitStatus: http.StatusInternalServerError}
	env := gwtNewEnv(t, op, 60*time.Second)
	gwtWaitOpen(t, env)

	c := gwtClient("op1", "player-unk")
	betID := gwtBetID(t)
	msg := clientMessage{Action: "bet", BetID: betID, Slot: env.slot, Amount: 10}

	env.gw.handleBet(c, msg)
	if m := gwtRecv(t, c, 2*time.Second); gwtType(m) != "BET_REJECTED" {
		t.Fatalf("attendu BET_REJECTED, obtenu %s", gwtType(m))
	}
	debits := op.filter(gwtIsDebit)
	if len(debits) != 1 {
		t.Fatalf("attendu 1 DEBIT, obtenu %d", len(debits))
	}
	entry, err := env.led.GetByTransactionID(context.Background(), debits[0].TransactionID)
	if err != nil {
		t.Fatalf("ledger GetByTransactionID: %v", err)
	}
	if entry.Status != ledger.StatusPending {
		t.Errorf("statut ledger = %v, attendu PENDING (issue indéterminée)", entry.Status)
	}
	if got := len(env.eng.UserRoundBets("player-unk")); got != 0 {
		t.Errorf("paris engine = %d, attendu 0", got)
	}

	// L'opérateur redevient sain : un retry avec le même betId ne doit JAMAIS rappeler le wallet
	// tant que l'entrée est PENDING (attente bornée puis rejet temporaire).
	op.setDebit(http.StatusOK, 0)
	env.gw.handleBet(c, msg)
	if m := gwtRecv(t, c, 2*time.Second); gwtType(m) != "BET_REJECTED" {
		t.Fatalf("retry : attendu BET_REJECTED, obtenu %s (reason=%q)", gwtType(m), gwtReason(m))
	}
	if got := len(op.filter(gwtIsDebit)); got != 1 {
		t.Errorf("retry : DEBIT rejoué (%d appels wallet au total, attendu 1)", got)
	}
	if got := len(env.eng.UserRoundBets("player-unk")); got != 0 {
		t.Errorf("retry : paris engine = %d, attendu 0", got)
	}
}

func TestGatewayHandleBet_RejectedDebit_FailsAndNeverReplays(t *testing.T) {
	op := &gwtOperator{debitStatus: http.StatusPaymentRequired}
	env := gwtNewEnv(t, op, 60*time.Second)
	gwtWaitOpen(t, env)

	c := gwtClient("op1", "player-rej")
	betID := gwtBetID(t)
	msg := clientMessage{Action: "bet", BetID: betID, Slot: env.slot, Amount: 10}

	env.gw.handleBet(c, msg)
	if m := gwtRecv(t, c, 2*time.Second); gwtType(m) != "BET_REJECTED" {
		t.Fatalf("attendu BET_REJECTED, obtenu %s", gwtType(m))
	}
	debits := op.filter(gwtIsDebit)
	if len(debits) != 1 {
		t.Fatalf("attendu 1 DEBIT, obtenu %d", len(debits))
	}
	entry, err := env.led.GetByTransactionID(context.Background(), debits[0].TransactionID)
	if err != nil {
		t.Fatalf("ledger GetByTransactionID: %v", err)
	}
	if entry.Status != ledger.StatusFailed {
		t.Errorf("statut ledger = %v, attendu FAILED", entry.Status)
	}

	env.gw.handleBet(c, msg) // retry, même betId
	if m := gwtRecv(t, c, 2*time.Second); gwtType(m) != "BET_REJECTED" {
		t.Fatalf("retry : attendu BET_REJECTED, obtenu %s", gwtType(m))
	}
	if got := len(op.filter(gwtIsDebit)); got != 1 {
		t.Errorf("retry : DEBIT rejoué (%d appels wallet au total, attendu 1)", got)
	}
	if got := len(env.eng.UserRoundBets("player-rej")); got != 0 {
		t.Errorf("paris engine = %d, attendu 0", got)
	}
}

func TestGatewayHandleBet_ReusedBetIDDifferentPayload(t *testing.T) {
	op := &gwtOperator{}
	env := gwtNewEnv(t, op, 60*time.Second)
	gwtWaitOpen(t, env)

	c := gwtClient("op1", "player-reuse")
	betID := gwtBetID(t)

	env.gw.handleBet(c, clientMessage{Action: "bet", BetID: betID, Slot: env.slot, Amount: 10})
	if m := gwtRecv(t, c, 2*time.Second); gwtType(m) != "BET_ACCEPTED" {
		t.Fatalf("1er pari : attendu BET_ACCEPTED, obtenu %s (reason=%q)", gwtType(m), gwtReason(m))
	}

	env.gw.handleBet(c, clientMessage{Action: "bet", BetID: betID, Slot: env.slot, Amount: 20})
	m := gwtRecv(t, c, 2*time.Second)
	if gwtType(m) != "BET_REJECTED" {
		t.Fatalf("réutilisation du betId : attendu BET_REJECTED, obtenu %s", gwtType(m))
	}
	if got := len(op.filter(gwtIsDebit)); got != 1 {
		t.Errorf("DEBIT = %d appels, attendu 1 (aucun second appel wallet)", got)
	}
	if got := len(env.eng.UserRoundBets("player-reuse")); got != 1 {
		t.Errorf("paris engine = %d, attendu 1", got)
	}
}

// ---------- arrêt gracieux ----------

// TestGatewayShutdown : non-régression de l'arrêt gracieux de la gateway. Aucun MongoDB, wallet ni
// réseau : les clients de test ont conn == nil, et handleUpgrade doit répondre 503 AVANT toute
// validation JWT (donc sans jamais toucher wsSecurity(), fail-closed sans opérateur autorisé).
func TestGatewayShutdown(t *testing.T) {
	gw := &gateway{clients: make(map[string]*client)}

	mustNotPanic := func(what string, fn func()) {
		t.Helper()
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("%s a paniqué : %v", what, r)
			}
		}()
		fn()
	}
	clientCount := func() int {
		gw.mu.RLock()
		defer gw.mu.RUnlock()
		return len(gw.clients)
	}

	c1 := gwtClient("op1", "shutdown-p1")
	c2 := gwtClient("op1", "shutdown-p2")

	// Avant shutdown : les deux clients sont acceptés.
	if !gw.register(c1) {
		t.Fatal("register(c1) refusé avant shutdown")
	}
	if !gw.register(c2) {
		t.Fatal("register(c2) refusé avant shutdown")
	}
	if got := clientCount(); got != 2 {
		t.Fatalf("clients enregistrés = %d, attendu 2", got)
	}

	gw.Shutdown()

	// Les deux canaux done sont fermés (writePump signalé), la map est vide, le flag est levé.
	for name, c := range map[string]*client{"c1": c1, "c2": c2} {
		select {
		case <-c.done:
		default:
			t.Errorf("%s : canal done non fermé après Shutdown", name)
		}
	}
	if got := clientCount(); got != 0 {
		t.Errorf("clients après Shutdown = %d, attendu 0", got)
	}
	if !gw.shutting.Load() {
		t.Error("shutting devrait être vrai après Shutdown")
	}

	// c.send n'est jamais fermé : un envoi ne doit pas paniquer.
	mustNotPanic("envoi sur c1.send après Shutdown", func() {
		select {
		case c1.send <- []byte("x"):
		default:
		}
	})

	// Idempotence : second Shutdown, et unregister tardif (appelé par readPump), sans panic.
	mustNotPanic("second Shutdown", gw.Shutdown)
	mustNotPanic("unregister après Shutdown", func() { gw.unregister(c1) })

	// Après shutdown : register refuse et n'enregistre rien.
	c3 := gwtClient("op1", "shutdown-p3")
	if gw.register(c3) {
		t.Error("register(c3) accepté après Shutdown")
	}
	if got := clientCount(); got != 0 {
		t.Errorf("clients après register refusé = %d, attendu 0", got)
	}
	select {
	case <-c3.done:
		t.Error("c3.done fermé alors que c3 n'a jamais été enregistré")
	default:
	}

	// handleUpgrade après shutdown : 503 avant toute validation du token.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/?token=invalide", nil)
	gw.handleUpgrade(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("handleUpgrade après Shutdown : code = %d, attendu %d", rec.Code, http.StatusServiceUnavailable)
	}
	if got := rec.Body.String(); got != "service en arrêt\n" {
		t.Errorf("handleUpgrade après Shutdown : corps = %q", got)
	}
}

// TestGatewayShutdownRegisterRace : register et Shutdown concurrents. Invariant vérifié quel que
// soit l'entrelacement : un client accepté par register a son done fermé par Shutdown, un client
// refusé n'est jamais enregistré ni fermé, et la map finit vide. Déterministe, sans time.Sleep.
func TestGatewayShutdownRegisterRace(t *testing.T) {
	gw := &gateway{clients: make(map[string]*client)}

	const n = 64
	clients := make([]*client, n)
	accepted := make([]bool, n)
	for i := range clients {
		clients[i] = gwtClient("op1", fmt.Sprintf("race-%d", i))
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			accepted[i] = gw.register(clients[i])
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		gw.Shutdown()
	}()
	close(start)
	wg.Wait()

	for i, c := range clients {
		closed := false
		select {
		case <-c.done:
			closed = true
		default:
		}
		if accepted[i] && !closed {
			t.Errorf("client %d accepté mais done non fermé par Shutdown", i)
		}
		if !accepted[i] && closed {
			t.Errorf("client %d refusé mais done fermé", i)
		}
	}
	gw.mu.RLock()
	left := len(gw.clients)
	gw.mu.RUnlock()
	if left != 0 {
		t.Errorf("clients restants après Shutdown = %d, attendu 0", left)
	}
}
