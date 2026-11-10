package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"

	v1 "github.com/tiidiio/223Gaming/contracts/product/v1"
	"github.com/tiidiio/223Gaming/internal/product"
	"github.com/tiidiio/223Gaming/provider/ledger"
	"github.com/tiidiio/223Gaming/provider/wallet"
)

const noAutoCashoutSentinel = 50000.00

var betIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)

func validBetID(id string) bool { return betIDPattern.MatchString(id) }

var errBetAlreadyExists = errors.New("bet already exists")

type wsClaims struct {
	OperatorID string `json:"operatorId"`
	jwt.RegisteredClaims
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     wsCheckOrigin,
}

type client struct {
	playerID   string
	operatorID string
	conn       *websocket.Conn
	send       chan []byte
	done       chan struct{}
	closeOnce  sync.Once
	lim        *wsRateLimiter
	mu         sync.Mutex
	activeBets map[v1.PanelID]*v1.BetView
}

func (c *client) sendJSON(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	c.trySend(b)
}
func (c *client) trySend(b []byte) {
	select {
	case <-c.done:
		return
	default:
	}
	select {
	case c.send <- b:
	case <-c.done:
	default:
	}
}
func (c *client) writePump() {
	ping := time.NewTicker(wsPingInterval)
	defer ping.Stop()
	for {
		select {
		case b := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if err := c.conn.WriteMessage(websocket.TextMessage, b); err != nil {
				c.conn.Close()
				return
			}
		case <-ping.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				c.conn.Close()
				return
			}
		case <-c.done:
			return
		}
	}
}

type gateway struct {
	panelGuard   *panelGuard
	eng          v1.GameEngine
	jwtSecret    []byte
	walletClient *wallet.Client
	walletQueue  *wallet.Queue
	ledger       *ledger.Ledger
	credits      *creditSettler
	betStore     BetStore
	mu           sync.RWMutex
	clients      map[string]*client
	shutting     atomic.Bool
	shutdownOnce sync.Once
}

func newGateway(eng v1.GameEngine, jwtSecret string, walletClient *wallet.Client, walletQueue *wallet.Queue, ldg *ledger.Ledger, betStore BetStore) *gateway {
	if betStore == nil {
		betStore = NewMemBetStore()
	}
	return &gateway{
		panelGuard:   newPanelGuard(),
		eng:          eng,
		jwtSecret:    []byte(jwtSecret),
		walletClient: walletClient,
		walletQueue:  walletQueue,
		ledger:       ldg,
		credits:      newCreditSettler(ldg, walletQueue, string(product.GameID)),
		betStore:     betStore,
		clients:      make(map[string]*client),
	}
}

func (gw *gateway) register(c *client) bool {
	gw.mu.Lock()
	if gw.shutting.Load() {
		gw.mu.Unlock()
		return false
	}
	old, hadOld := gw.clients[c.engineID()]
	gw.clients[c.engineID()] = c
	gw.mu.Unlock()
	if hadOld && old != nil && old.conn != nil {
		_ = old.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(4001, "session_replaced"), time.Now().Add(time.Second))
		old.conn.Close()
	}
	return true
}
func (gw *gateway) unregister(c *client) {
	gw.mu.Lock()
	if cur, ok := gw.clients[c.engineID()]; ok && cur == c {
		delete(gw.clients, c.engineID())
	}
	gw.mu.Unlock()
	c.closeOnce.Do(func() { close(c.done) })
	// libère tous les locks panels de ce user
	if gw.panelGuard != nil {
		gw.panelGuard.UnlockAll(c.engineID())
	}
}
func (gw *gateway) Shutdown() {
	gw.shutdownOnce.Do(func() {
		gw.mu.Lock()
		gw.shutting.Store(true)
		snapshot := make([]*client, 0, len(gw.clients))
		for _, c := range gw.clients {
			snapshot = append(snapshot, c)
		}
		for k := range gw.clients {
			delete(gw.clients, k)
		}
		gw.mu.Unlock()
		for _, c := range snapshot {
			if c.conn != nil {
				_ = c.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseGoingAway, "server_shutdown"), time.Now().Add(time.Second))
				_ = c.conn.Close()
			}
			c.closeOnce.Do(func() { close(c.done) })
		}
		log.Printf("gateway: arrêt gracieux, %d client(s) fermé(s)", len(snapshot))
	})
}
func (gw *gateway) Broadcast(msgType string, data map[string]any) {
	payload := map[string]any{"type": msgType, "data": data}
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	gw.mu.RLock()
	defer gw.mu.RUnlock()
	for _, c := range gw.clients {
		c.trySend(b)
	}
}
func (gw *gateway) broadcast(msgType string, data map[string]any) { gw.Broadcast(msgType, data) }

// FIX BUG 1 : crédit basé sur debitTxnID, pas betID
func (gw *gateway) settleCashout(c *client, bet v1.BetView) {
	if bet.Payout <= 0 {
		return
	}
	// Récupère debitTxnID persistant
	debitTxnID, ok := gw.betStore.GetDebitTxnID(bet.ID)
	if !ok {
		// fallback si redémarrage sans persistance : utilise bet.ID mais log critique
		log.Printf("settleCashout: CRITIQUE betStore miss betID=%s, fallback betID (risque collision après restart)", bet.ID)
	}
	// Crédit idempotent par debitTxnID
	if ok {
		gw.credits.SettleWithDebitTxn(creditRequest{
			OperatorID: c.operatorID, PlayerID: c.playerID,
			RoundID: bet.RoundID, BetID: bet.ID, AmountMinor: bet.Payout,
			DebitTxnID: debitTxnID,
		})
	} else {
		gw.credits.Settle(creditRequest{
			OperatorID: c.operatorID, PlayerID: c.playerID,
			RoundID: bet.RoundID, BetID: bet.ID, AmountMinor: bet.Payout,
		})
	}
	c.mu.Lock()
	delete(c.activeBets, bet.Panel)
	if gw.panelGuard != nil {
		gw.panelGuard.Unlock(bet.UserID, bet.Panel)
		gw.panelGuard.Unlock(c.engineID(), bet.Panel)
	}
	c.mu.Unlock()
	gw.refreshBalance(c, 500*time.Millisecond, 2*time.Second, 5*time.Second)
	c.sendJSON(map[string]any{"type": "CASHOUT_ACCEPTED", "data": map[string]any{"slot": product.PanelToSlot(bet.Panel), "payout": product.ToUnits(bet.Payout)}})
}

func (gw *gateway) CheckAutoCashout(multiplier float64) { gw.checkAutoCashout(multiplier) }

func (gw *gateway) checkAutoCashout(currentMultiplier float64) {
	// L'auto-cashout est désormais géré par le moteur (EventAutoCashout), plus ici
	// Gardé pour compat si besoin
	gw.mu.RLock()
	snapshot := make([]*client, 0, len(gw.clients))
	for _, c := range gw.clients {
		snapshot = append(snapshot, c)
	}
	gw.mu.RUnlock()
	for _, c := range snapshot {
		c.mu.Lock()
		var toCashout []v1.BetView
		for _, bet := range c.activeBets {
			if bet != nil && bet.CashoutTarget <= currentMultiplier && bet.CashoutTarget > 0 && bet.CashoutTarget < noAutoCashoutSentinel {
				toCashout = append(toCashout, *bet)
			}
		}
		c.mu.Unlock()
		for _, bet := range toCashout {
			settled, err := gw.eng.Cashout(bet.ID)
			if err != nil {
				continue
			}
			gw.settleCashout(c, settled)
		}
	}
}

func (gw *gateway) NotifyLostBets(lost []v1.BetView) { gw.notifyLostBets(lost) }

func (gw *gateway) notifyLostBets(lost []v1.BetView) {
	gw.mu.RLock()
	defer gw.mu.RUnlock()
	for _, bet := range lost {
		c, ok := gw.clients[bet.UserID]
		if !ok {
			// cherche aussi par engineID
			for _, cc := range gw.clients {
				if cc.engineID() == bet.UserID {
					c = cc
					ok = true
					break
				}
			}
		}
		if !ok {
			continue
		}
		c.mu.Lock()
		delete(c.activeBets, bet.Panel)
		if gw.panelGuard != nil {
			gw.panelGuard.Unlock(bet.UserID, bet.Panel)
			gw.panelGuard.Unlock(c.engineID(), bet.Panel)
		}
		c.mu.Unlock()
		c.sendJSON(map[string]any{"type": "CASHOUT_REJECTED", "data": map[string]any{"slot": product.PanelToSlot(bet.Panel), "reason": "round_crashed"}})
	}
}

func (gw *gateway) handleUpgrade(w http.ResponseWriter, r *http.Request) {
	if gw.shutting.Load() {
		http.Error(w, "service en arrêt", http.StatusServiceUnavailable)
		return
	}
	claims, err := wsSecurity().validateToken(r.URL.Query().Get("token"), gw.jwtSecret)
	if err != nil {
		http.Error(w, "token invalide", http.StatusUnauthorized)
		return
	}
	if !wsSecurity().acquire() {
		http.Error(w, "service saturé", http.StatusServiceUnavailable)
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws upgrade: %v", err)
		wsSecurity().release()
		return
	}
	c := &client{
		playerID:   claims.Subject,
		operatorID: claims.OperatorID,
		conn:       conn,
		send:       make(chan []byte, 32),
		done:       make(chan struct{}),
		lim:        newWSRateLimiter(wsMsgRate, wsMsgBurst),
		activeBets: make(map[v1.PanelID]*v1.BetView),
	}
	if !gw.register(c) {
		_ = conn.Close()
		wsSecurity().release()
		return
	}
	gw.rehydrateBets(c)
	go c.writePump()
	go gw.readPump(c)
	c.sendJSON(map[string]any{"type": "CONNECTED", "playerId": c.playerID})
	gw.syncRoundState(c)
	balCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	balance, err := gw.walletClient.GetBalance(balCtx, c.operatorID, c.playerID)
	cancel()
	if err != nil {
		log.Printf("wallet: GetBalance player=%s: %v", c.playerID, err)
		c.sendJSON(map[string]any{"type": "ERROR", "reason": "solde indisponible"})
		balance = 0
	}
	c.sendJSON(map[string]any{"type": "BALANCE", "balance": product.ToUnits(balance)})
}

type clientMessage struct {
	Action      string   `json:"action"`
	BetID       string   `json:"betId"`
	Slot        string   `json:"slot"`
	Amount      float64  `json:"amount"`
	AutoCashout *float64 `json:"autoCashout"`
}

func (gw *gateway) readPump(c *client) {
	defer func() {
		gw.unregister(c)
		c.conn.Close()
		wsSecurity().release()
	}()
	wsConfigureConn(c.conn)
	log.Printf("ws: connecté player=%s op=%s", c.playerID, c.operatorID)
	for {
		_, raw, err := c.conn.ReadMessage()
		if err != nil {
			log.Printf("ws: fin player=%s: %v", c.playerID, err)
			return
		}
		if !c.lim.allow() {
			if c.lim.viol > wsMaxViolations {
				log.Printf("ws: fin player=%s: trop de messages (rate limit)", c.playerID)
				return
			}
			continue
		}
		var msg clientMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		switch msg.Action {
		case "join":
			balCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			balance, err := gw.walletClient.GetBalance(balCtx, c.operatorID, c.playerID)
			cancel()
			if err != nil {
				log.Printf("wallet: GetBalance (join) player=%s: %v", c.playerID, err)
				continue
			}
			c.sendJSON(map[string]any{"type": "BALANCE", "balance": product.ToUnits(balance)})
		case "ping":
			c.sendJSON(map[string]any{"type": "PONG"})
		case "bet":
			gw.handleBet(c, msg)
		case "cashout":
			gw.handleCashout(c, msg)
		}
	}
}

func (gw *gateway) handleBet(c *client, msg clientMessage) {
	panel, ok := product.SlotToPanel(msg.Slot)
	if !ok {
		c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": "slot invalide"}})
		return
	}
	if !validBetID(msg.BetID) {
		c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": "betId invalide ou manquant"}})
		return
	}
	if msg.Amount != math.Trunc(msg.Amount) {
		c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": "montant invalide"}})
		return
	}
	if msg.Amount > wsSecurity().maxBetUnits {
		c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": "montant invalide"}})
		return
	}
	amount := int64(msg.Amount) * product.CentimesPerUnit
	if amount <= 0 {
		c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": "montant invalide"}})
		return
	}

	// FIX 2-panel V2 : TryLock AVANT tout DEBIT
	if gw.panelGuard != nil {
		if !gw.panelGuard.TryLock(c.engineID(), panel) {
			c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": errPanelOccupied.Error()}})
			gw.announceExistingBet(c, panel, msg.Slot)
			return
		}
	}

	round, err := gw.eng.CurrentRound()
	if err != nil {
		if gw.panelGuard != nil {
			gw.panelGuard.Unlock(c.engineID(), panel)
		}
		c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": "aucun round actif"}})
		return
	}
	cashoutTarget := noAutoCashoutSentinel
	if msg.AutoCashout != nil {
		t := *msg.AutoCashout
		if math.IsNaN(t) || math.IsInf(t, 0) || t < wsMinAutoCashout || t > wsMaxAutoCashout {
			if gw.panelGuard != nil {
				gw.panelGuard.Unlock(c.engineID(), panel)
			}
			c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": "autoCashout invalide"}})
			return
		}
		cashoutTarget = t
	}
	opCtx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	idemKey := ledger.IdempotencyKey(fmt.Sprintf("debit:%s:%s:%s", c.operatorID, c.playerID, msg.BetID))
	externalRef := ledger.ExternalRef(product.EncodeDebitContext(round.ID, panel, amount, cashoutTarget))
	debitEntry, created, err := gw.ledger.Begin(opCtx, ledger.NewEntry{
		GameID:         product.GameID,
		OperatorID:     ledger.OperatorID(c.operatorID),
		PlayerID:       ledger.PlayerID(c.playerID),
		ExternalRef:    externalRef,
		IdempotencyKey: idemKey,
		Type:           ledger.EntryDebit,
		AmountMinor:    amount,
	})
	if err != nil {
		log.Printf("ledger: begin debit betId=%s: %v", msg.BetID, err)
		if gw.panelGuard != nil {
			gw.panelGuard.Unlock(c.engineID(), panel)
		}
		c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": "service indisponible"}})
		return
	}
	if !created {
		if debitEntry.ExternalRef != externalRef {
			if gw.panelGuard != nil {
				gw.panelGuard.Unlock(c.engineID(), panel)
			}
			c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": "betId déjà utilisé avec des paramètres différents"}})
			return
		}
		resolved := debitEntry
		if resolved.Status == ledger.StatusPending {
			resolved, err = gw.waitForLedgerResolution(opCtx, debitEntry.TransactionID)
			if err != nil {
				log.Printf("ledger: attente résolution betId=%s txn=%s: %v", msg.BetID, debitEntry.TransactionID, err)
				if gw.panelGuard != nil {
					gw.panelGuard.Unlock(c.engineID(), panel)
				}
				c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": "débit en cours de traitement, réessayez"}})
				return
			}
		}
		switch resolved.Status {
		case ledger.StatusFailed:
			if gw.panelGuard != nil {
				gw.panelGuard.Unlock(c.engineID(), panel)
			}
			c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": "débit refusé"}})
			return
		case ledger.StatusConfirmed:
			if refunded, chkErr := gw.debitMarkedForRefund(c, resolved); chkErr != nil || refunded {
				reason := "débit annulé"
				if chkErr != nil {
					reason = "service indisponible, réessayez"
				}
				if gw.panelGuard != nil {
					gw.panelGuard.Unlock(c.engineID(), panel)
				}
				c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": reason}})
				return
			}
			gw.refreshBalance(c, 0)
			gw.placeBetOrReuse(c, msg, panel, amount, cashoutTarget, round, resolved)
			return
		default:
			log.Printf("ledger: statut inattendu après résolution betId=%s txn=%s status=%s", msg.BetID, resolved.TransactionID, resolved.Status)
			if gw.panelGuard != nil {
				gw.panelGuard.Unlock(c.engineID(), panel)
			}
			c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": "état de débit incohérent"}})
			return
		}
	}
	// double check panel après ledger (race)
	if _, ok, _ := gw.eng.GetPanelBet(c.engineID(), panel); ok {
		if _, fErr := gw.ledger.Fail(opCtx, debitEntry.TransactionID, "panel occupé"); fErr != nil {
			log.Printf("ledger: fail debit (panel occupé) txn=%s: %v", debitEntry.TransactionID, fErr)
		}
		if gw.panelGuard != nil {
			gw.panelGuard.Unlock(c.engineID(), panel)
		}
		c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": errPanelOccupied.Error()}})
		gw.announceExistingBet(c, panel, msg.Slot)
		return
	}
	debitCtx, cancelDebit := context.WithTimeout(opCtx, 3*time.Second)
	balance, outcome, err := gw.walletClient.Debit(debitCtx, product.GameID, debitEntry.TransactionID, c.operatorID, c.playerID, round.ID, msg.BetID, amount)
	cancelDebit()
	switch outcome {
	case wallet.OutcomeAccepted:
		confirmed, cErr := gw.ledger.Confirm(opCtx, debitEntry.TransactionID, &balance)
		if cErr != nil {
			log.Printf("ledger: confirm debit txn=%s: %v", debitEntry.TransactionID, cErr)
			confirmed = debitEntry
		}
		c.sendJSON(map[string]any{"type": "BALANCE", "balance": product.ToUnits(balance)})
		gw.placeBetOrReuseEx(c, msg, panel, amount, cashoutTarget, round, confirmed, true)
	case wallet.OutcomeRejected:
		reason := ""
		if err != nil {
			reason = err.Error()
		}
		if _, fErr := gw.ledger.Fail(opCtx, debitEntry.TransactionID, reason); fErr != nil {
			log.Printf("ledger: fail debit txn=%s: %v", debitEntry.TransactionID, fErr)
		}
		if gw.panelGuard != nil {
			gw.panelGuard.Unlock(c.engineID(), panel)
		}
		c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": "débit refusé"}})
	case wallet.OutcomeUnknown:
		log.Printf("wallet: debit outcome indéterminé txn=%s betId=%s: %v", debitEntry.TransactionID, msg.BetID, err)
		gw.markDebitForRefund(c, debitEntry, amount)
		if gw.panelGuard != nil {
			gw.panelGuard.Unlock(c.engineID(), panel)
		}
		c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": "débit indéterminé, réessayez"}})
	}
}

func (gw *gateway) waitForLedgerResolution(ctx context.Context, txnID ledger.TransactionID) (*ledger.Entry, error) {
	const pollInterval = 150 * time.Millisecond
	const maxWait = 4 * time.Second
	deadline := time.Now().Add(maxWait)
	for {
		entry, err := gw.ledger.GetByTransactionID(ctx, txnID)
		if err != nil {
			return nil, err
		}
		if entry.Status != ledger.StatusPending {
			return entry, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("ledger: résolution txn=%s: délai dépassé", txnID)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

func (gw *gateway) placeBetOrReuse(c *client, msg clientMessage, panel v1.PanelID, amount int64, cashoutTarget float64, round *v1.RoundView, debitEntry *ledger.Entry) {
	gw.placeBetOrReuseEx(c, msg, panel, amount, cashoutTarget, round, debitEntry, false)
}
func (gw *gateway) placeBetOrReuseEx(c *client, msg clientMessage, panel v1.PanelID, amount int64, cashoutTarget float64, round *v1.RoundView, debitEntry *ledger.Entry, fresh bool) {
	// FIX : persistance AVANT placement pour retrouver debitTxnID après restart
	_ = gw.betStore.Remember(context.Background(), msg.BetID, debitEntry.TransactionID, c.operatorID, c.playerID)

	bet, err := gw.eng.PlaceBet(c.engineID(), panel, amount, cashoutTarget)
	if err != nil {
		// FIX BUG 2 : errBetAlreadyExists est var sentinel, pas func
		if errors.Is(err, errBetAlreadyExists) {
			if fresh {
				gw.rollbackConfirmedDebit(c, msg, amount, round, debitEntry, errPanelOccupied)
				if gw.panelGuard != nil {
					gw.panelGuard.Unlock(c.engineID(), panel)
				}
				gw.announceExistingBet(c, panel, msg.Slot)
				return
			}
			if existing, ok, _ := gw.eng.GetPanelBet(c.engineID(), panel); ok {
				b := existing
				c.mu.Lock()
				c.activeBets[panel] = &b
				c.mu.Unlock()
				c.sendJSON(map[string]any{"type": "BET_ACCEPTED", "data": map[string]any{"slot": msg.Slot}})
				return
			}
			log.Printf("gateway: pari existant introuvable après ErrBetAlreadyExists player=%s panel=%d betId=%s: %v", c.playerID, panel, msg.BetID, err)
			if gw.panelGuard != nil {
				gw.panelGuard.Unlock(c.engineID(), panel)
			}
			c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": "pari déjà traité"}})
			return
		}
		gw.rollbackConfirmedDebit(c, msg, amount, round, debitEntry, err)
		if gw.panelGuard != nil {
			gw.panelGuard.Unlock(c.engineID(), panel)
		}
		return
	}
	c.mu.Lock()
	c.activeBets[panel] = bet
	c.mu.Unlock()
	// Garde le lock panel jusqu'au cashout/crash - ne pas unlock ici
	c.sendJSON(map[string]any{"type": "BET_ACCEPTED", "data": map[string]any{"slot": msg.Slot}})
}

func (gw *gateway) rollbackConfirmedDebit(c *client, msg clientMessage, amount int64, round *v1.RoundView, debitEntry *ledger.Entry, placeErr error) {
	idemKey := fmt.Sprintf("rollback:%s:%s:%s", c.operatorID, c.playerID, debitEntry.TransactionID)
	rollbackEntry, _, rbErr := gw.ledger.Begin(context.Background(), ledger.NewEntry{
		GameID:              product.GameID,
		OperatorID:          ledger.OperatorID(c.operatorID),
		PlayerID:            ledger.PlayerID(c.playerID),
		ExternalRef:         debitEntry.ExternalRef,
		IdempotencyKey:      ledger.IdempotencyKey(idemKey),
		Type:                ledger.EntryRollback,
		AmountMinor:         amount,
		ParentTransactionID: debitEntry.TransactionID,
	})
	if rbErr != nil {
		log.Printf("ledger: begin rollback txn=%s: %v", debitEntry.TransactionID, rbErr)
	}
	rb := wallet.Callback{
		ID:             fmt.Sprintf("rollback-%s", debitEntry.TransactionID),
		Type:           wallet.CallbackRollback,
		GameID:         product.GameID,
		OperatorID:     c.operatorID,
		PlayerID:       c.playerID,
		RoundID:        round.ID,
		BetID:          msg.BetID,
		Amount:         amount,
		IdempotencyKey: idemKey,
	}
	if rollbackEntry != nil {
		rb.TransactionID = rollbackEntry.TransactionID
	}
	if qErr := gw.walletQueue.Enqueue(context.Background(), rb); qErr != nil {
		log.Printf("wallet: enqueue ROLLBACK txn=%s: %v", debitEntry.TransactionID, qErr)
	}
	gw.refreshBalance(c, 800*time.Millisecond, 3*time.Second)
	c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": placeErr.Error()}})
}

func (gw *gateway) handleCashout(c *client, msg clientMessage) {
	panel, ok := product.SlotToPanel(msg.Slot)
	if !ok {
		return
	}
	c.mu.Lock()
	bet, exists := c.activeBets[panel]
	c.mu.Unlock()
	if !exists {
		c.sendJSON(map[string]any{"type": "CASHOUT_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": "aucun pari actif"}})
		return
	}
	settled, err := gw.eng.Cashout(bet.ID)
	if err != nil {
		c.sendJSON(map[string]any{"type": "CASHOUT_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": err.Error()}})
		return
	}
	gw.settleCashout(c, settled)
}

func centimesToUnits(c int64) float64 { return product.ToUnits(c) }
