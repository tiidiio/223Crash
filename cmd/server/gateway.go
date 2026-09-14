package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"

	"github.com/tiidiio/223Gaming/223Crash/internal/engine"
	"github.com/tiidiio/223Gaming/223Crash/internal/game"
	"github.com/tiidiio/223Gaming/223Crash/internal/wallet"
)

// centimesPerUnit convertit l'unité majeure affichée au joueur (frontend, step=1)
// vers l'unité mineure attendue par internal/wallet.Callback.Amount (centimes) —
// même unité que mockwallet.startingBalance (100000 = 1000.00). Toute la chaîne
// DEBIT/CREDIT/ROLLBACK doit rester en centimes ; ne JAMAIS repasser en float ici.
const centimesPerUnit = 100

type wsClaims struct {
	OperatorID string `json:"operatorId"`
	jwt.RegisteredClaims
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true }, // embed cross-origin (opérateurs tiers)
}

type client struct {
	playerID   string
	operatorID string
	conn       *websocket.Conn
	send       chan []byte

	mu         sync.Mutex
	activeBets map[game.BetPanel]*game.Bet
}

func (c *client) sendJSON(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	select {
	case c.send <- b:
	default:
		// buffer plein : client trop lent, on droppe plutôt que de bloquer le hub
	}
}

func (c *client) writePump() {
	for b := range c.send {
		if err := c.conn.WriteMessage(websocket.TextMessage, b); err != nil {
			return
		}
	}
}

type gateway struct {
	eng          *engine.CrashEngine
	jwtSecret    []byte
	walletClient *wallet.Client
	walletQueue  *wallet.Queue

	mu      sync.RWMutex
	clients map[string]*client // playerID -> client
}

func newGateway(eng *engine.CrashEngine, jwtSecret string, walletClient *wallet.Client, walletQueue *wallet.Queue) *gateway {
	return &gateway{
		eng:          eng,
		jwtSecret:    []byte(jwtSecret),
		walletClient: walletClient,
		walletQueue:  walletQueue,
		clients:      make(map[string]*client),
	}
}

func (gw *gateway) register(c *client) {
	gw.mu.Lock()
	if old, ok := gw.clients[c.playerID]; ok {
		old.conn.Close()
	}
	gw.clients[c.playerID] = c
	gw.mu.Unlock()
}

func (gw *gateway) unregister(c *client) {
	gw.mu.Lock()
	if cur, ok := gw.clients[c.playerID]; ok && cur == c {
		delete(gw.clients, c.playerID)
	}
	gw.mu.Unlock()
	close(c.send)
}

func (gw *gateway) broadcast(msgType string, data map[string]any) {
	payload := map[string]any{"type": msgType, "data": data}
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	gw.mu.RLock()
	defer gw.mu.RUnlock()
	for _, c := range gw.clients {
		select {
		case c.send <- b:
		default:
		}
	}
}

// settleCashout centralise le crédit wallet après un cashout réussi (manuel ou auto),
// pour ne pas dupliquer la logique CREDIT entre handleCashout et checkAutoCashout.
// bet.Payout est en centimes (même unité que le DEBIT initial) — reconverti en unité
// majeure uniquement pour l'affichage client (CASHOUT_ACCEPTED).
func (gw *gateway) settleCashout(c *client, bet *game.Bet) {
	if bet.Payout > 0 {
		cb := wallet.Callback{
			ID:             fmt.Sprintf("credit-%s", bet.ID),
			Type:           wallet.CallbackCredit,
			OperatorID:     c.operatorID,
			PlayerID:       c.playerID,
			RoundID:        bet.RoundID,
			BetID:          bet.ID,
			Amount:         bet.Payout,
			IdempotencyKey: fmt.Sprintf("credit:%s", bet.ID),
		}
		if err := gw.walletQueue.Enqueue(context.Background(), cb); err != nil {
			log.Printf("wallet: enqueue CREDIT bet=%s: %v", bet.ID, err)
		}
	}

	c.mu.Lock()
	delete(c.activeBets, bet.Panel)
	c.mu.Unlock()

	c.sendJSON(map[string]any{"type": "CASHOUT_ACCEPTED", "data": map[string]any{
		"slot":   panelToSlot(bet.Panel),
		"payout": centimesToUnits(bet.Payout),
	}})
}

// checkAutoCashout implémente ce que internal/engine ne fait pas : exécuter un cashout
// quand CashoutTarget est atteint. Appelé à chaque TICK depuis forwardEvents.
func (gw *gateway) checkAutoCashout(currentMultiplier float64) {
	gw.mu.RLock()
	clientsSnapshot := make([]*client, 0, len(gw.clients))
	for _, c := range gw.clients {
		clientsSnapshot = append(clientsSnapshot, c)
	}
	gw.mu.RUnlock()

	for _, c := range clientsSnapshot {
		c.mu.Lock()
		var toCashout []*game.Bet
		for panel, bet := range c.activeBets {
			if bet.CashoutTarget <= currentMultiplier {
				toCashout = append(toCashout, bet)
				_ = panel
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

// notifyLostBets force le reset visuel du panel côté frontend (data-active) pour les paris
// perdants — aucune action wallet : le joueur a déjà été débité au moment du pari, la perte
// est déjà actée côté opérateur, un CRASH n'est pas un événement wallet.
func (gw *gateway) notifyLostBets(lost []game.Bet) {
	gw.mu.RLock()
	defer gw.mu.RUnlock()
	for _, bet := range lost {
		c, ok := gw.clients[bet.UserID]
		if !ok {
			continue
		}
		c.mu.Lock()
		delete(c.activeBets, bet.Panel)
		c.mu.Unlock()
		c.sendJSON(map[string]any{"type": "CASHOUT_REJECTED", "data": map[string]any{"slot": panelToSlot(bet.Panel), "reason": "round_crashed"}})
	}
}

func (gw *gateway) handleUpgrade(w http.ResponseWriter, r *http.Request) {
	tokenStr := r.URL.Query().Get("token")
	if tokenStr == "" {
		http.Error(w, "token manquant", http.StatusUnauthorized)
		return
	}

	claims := &wsClaims{}
	tok, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
		return gw.jwtSecret, nil
	}, jwt.WithValidMethods([]string{"HS512"}))
	if err != nil || !tok.Valid || claims.Subject == "" {
		http.Error(w, "token invalide", http.StatusUnauthorized)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws upgrade: %v", err)
		return
	}

	c := &client{
		playerID:   claims.Subject,
		operatorID: claims.OperatorID,
		conn:       conn,
		send:       make(chan []byte, 32),
		activeBets: make(map[game.BetPanel]*game.Bet),
	}

	gw.register(c)
	go c.writePump()
	go gw.readPump(c)

	c.sendJSON(map[string]any{"type": "CONNECTED", "playerId": c.playerID})

	balCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	balance, err := gw.walletClient.GetBalance(balCtx, c.operatorID, c.playerID)
	cancel()
	if err != nil {
		log.Printf("wallet: GetBalance player=%s: %v", c.playerID, err)
		c.sendJSON(map[string]any{"type": "ERROR", "reason": "solde indisponible"})
		balance = 0
	}
	c.sendJSON(map[string]any{"type": "BALANCE", "balance": centimesToUnits(balance)})
}

type clientMessage struct {
	Action      string   `json:"action"`
	Slot        string   `json:"slot"`
	Amount      float64  `json:"amount"`
	AutoCashout *float64 `json:"autoCashout"`
}

func (gw *gateway) readPump(c *client) {
	defer func() {
		gw.unregister(c)
		c.conn.Close()
	}()

	for {
		_, raw, err := c.conn.ReadMessage()
		if err != nil {
			return
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
			c.sendJSON(map[string]any{"type": "BALANCE", "balance": centimesToUnits(balance)})
		case "bet":
			gw.handleBet(c, msg)
		case "cashout":
			gw.handleCashout(c, msg)
		}
	}
}

func (gw *gateway) handleBet(c *client, msg clientMessage) {
	panel, ok := slotToPanel(msg.Slot)
	if !ok {
		c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": "slot invalide"}})
		return
	}

	// msg.Amount arrive en unité majeure (ex: 100 = "100" affiché côté joueur, index.html
	// impose step=1 donc pas de décimales attendues). Toute la chaîne wallet (mockwallet,
	// Callback.Amount, engine.Bet) raisonne en centimes — voir centimesPerUnit ci-dessus.
	// On rejette explicitement tout montant fractionnaire au lieu de le tronquer en silence.
	if msg.Amount != math.Trunc(msg.Amount) {
		c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": "montant invalide"}})
		return
	}
	amount := int64(msg.Amount) * centimesPerUnit
	if amount <= 0 {
		c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": "montant invalide"}})
		return
	}

	round, err := gw.eng.CurrentRound()
	if err != nil {
		c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": "aucun round actif"}})
		return
	}

	// DEBIT synchrone et bloquant : si l'opérateur refuse (fonds insuffisants, down),
	// le pari n'est jamais engagé côté engine. C'est volontairement le seul point
	// synchrone de tout le flux wallet — le reste (CREDIT/ROLLBACK) passe par la queue.
	debitID := fmt.Sprintf("debit-%s-%s-%d", c.playerID, msg.Slot, time.Now().UnixNano())
	debitCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	_, err = gw.walletClient.Debit(debitCtx, c.operatorID, c.playerID, round.ID, debitID, amount)
	cancel()
	if err != nil {
		c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": "débit refusé"}})
		return
	}

	cashoutTarget := 50000.00 // sentinel "pas d'auto-cashout" — jamais atteint en pratique
	if msg.AutoCashout != nil {
		cashoutTarget = *msg.AutoCashout
	}

	bet, err := gw.eng.PlaceBet(c.playerID, panel, amount, cashoutTarget)
	if err != nil {
		// Le débit a réussi mais le pari n'a pas pu être engagé (round fermé entre-temps,
		// slot déjà occupé, etc.) : on rend l'argent via ROLLBACK, asynchrone avec retry —
		// contrairement au DEBIT, on ne peut pas se permettre de bloquer le joueur ici.
		rb := wallet.Callback{
			ID:             fmt.Sprintf("rollback-%s", debitID),
			Type:           wallet.CallbackRollback,
			OperatorID:     c.operatorID,
			PlayerID:       c.playerID,
			RoundID:        round.ID,
			BetID:          debitID,
			Amount:         amount,
			IdempotencyKey: fmt.Sprintf("rollback:%s", debitID),
		}
		if qErr := gw.walletQueue.Enqueue(context.Background(), rb); qErr != nil {
			log.Printf("wallet: enqueue ROLLBACK debit=%s: %v", debitID, qErr)
		}
		c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": err.Error()}})
		return
	}

	c.mu.Lock()
	c.activeBets[panel] = bet
	c.mu.Unlock()

	c.sendJSON(map[string]any{"type": "BET_ACCEPTED", "data": map[string]any{"slot": msg.Slot}})
}

func (gw *gateway) handleCashout(c *client, msg clientMessage) {
	panel, ok := slotToPanel(msg.Slot)
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

func slotToPanel(slot string) (game.BetPanel, bool) {
	switch slot {
	case "A":
		return game.Panel1, true
	case "B":
		return game.Panel2, true
	default:
		return 0, false
	}
}

func panelToSlot(panel game.BetPanel) string {
	switch panel {
	case game.Panel1:
		return "A"
	case game.Panel2:
		return "B"
	default:
		return "?"
	}
}

func centimesToUnits(centimes int64) float64 {
	return float64(centimes) / centimesPerUnit
}
