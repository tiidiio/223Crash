package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"

	"github.com/tiidiio/223Gaming/223Crash/internal/engine"
	"github.com/tiidiio/223Gaming/223Crash/internal/game"
)

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
	eng       *engine.CrashEngine
	jwtSecret []byte

	mu      sync.RWMutex
	clients map[string]*client // playerID -> client
}

func newGateway(eng *engine.CrashEngine, jwtSecret string) *gateway {
	return &gateway{
		eng:       eng,
		jwtSecret: []byte(jwtSecret),
		clients:   make(map[string]*client),
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

// checkAutoCashout implémente ce que internal/engine ne fait pas : exécuter un cashout
// quand CashoutTarget est atteint. Appelé à chaque TICK depuis forwardEvents.
func (gw *gateway) checkAutoCashout(currentMultiplier float64) {
	gw.mu.RLock()
	defer gw.mu.RUnlock()
	for _, c := range gw.clients {
		c.mu.Lock()
		for panel, bet := range c.activeBets {
			if bet.CashoutTarget <= currentMultiplier {
				if _, err := gw.eng.Cashout(bet.ID); err == nil {
					c.sendJSON(map[string]any{"type": "CASHOUT_ACCEPTED", "data": map[string]any{"slot": panelToSlot(panel)}})
				}
				delete(c.activeBets, panel)
			}
		}
		c.mu.Unlock()
	}
}

// notifyLostBets force le reset visuel du panel côté frontend (data-active) pour les paris
// perdants — index.html ne gère aucun event dédié pour ce cas, on réutilise CASHOUT_REJECTED.
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
	// pas d'intégration wallet dans ce repo — solde placeholder
	c.sendJSON(map[string]any{"type": "BALANCE", "balance": 0})
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
			c.sendJSON(map[string]any{"type": "BALANCE", "balance": 0})
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

	amount := int64(msg.Amount)
	if amount <= 0 {
		c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": "montant invalide"}})
		return
	}

	cashoutTarget := 50000.00 // sentinel "pas d'auto-cashout" — jamais atteint en pratique
	if msg.AutoCashout != nil {
		cashoutTarget = *msg.AutoCashout
	}

	bet, err := gw.eng.PlaceBet(c.playerID, panel, amount, cashoutTarget)
	if err != nil {
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

	if _, err := gw.eng.Cashout(bet.ID); err != nil {
		c.sendJSON(map[string]any{"type": "CASHOUT_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": err.Error()}})
		return
	}

	c.mu.Lock()
	delete(c.activeBets, panel)
	c.mu.Unlock()

	c.sendJSON(map[string]any{"type": "CASHOUT_ACCEPTED", "data": map[string]any{"slot": msg.Slot}})
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
