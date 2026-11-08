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
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"

	"github.com/tiidiio/223Crash/internal/engine"
	"github.com/tiidiio/223Crash/internal/game"
	"github.com/tiidiio/223Gaming/provider"
	"github.com/tiidiio/223Gaming/provider/ledger"
	"github.com/tiidiio/223Gaming/provider/wallet"
)

// centimesPerUnit convertit l'unité majeure affichée au joueur (frontend, step=1)
// vers l'unité mineure attendue par internal/wallet.Callback.Amount (centimes) —
// même unité que mockwallet.startingBalance (100000 = 1000.00). Toute la chaîne
// DEBIT/CREDIT/ROLLBACK doit rester en centimes ; ne JAMAIS repasser en float ici.
const centimesPerUnit = 100

// productGameID identifie ce Game Product auprès du Provider Core (internal/provider,
// internal/provider/ledger, internal/wallet). Valeur opaque, jamais interprétée en dehors de
// ce fichier — ne jamais la faire remonter dans internal/wallet ou internal/provider sous
// forme de switch/if.
const productGameID provider.GameID = "223crash"

// noAutoCashoutSentinel signifie "pas d'auto-cashout" — valeur jamais atteinte en pratique
// (isValidMultiplier plafonne à 50000.00 côté internal/game).
const noAutoCashoutSentinel = 50000.00

// betIDPattern valide le betId fourni par le client : identifiant opaque généré côté frontend
// (crypto.randomUUID() ou repli — voir ws.js), utilisé comme composant de la clé d'idempotence
// côté Ledger. Longueur bornée pour éviter tout abus (clé d'idempotence surdimensionnée en base).
var betIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)

func validBetID(id string) bool {
	return betIDPattern.MatchString(id)
}

// encodeDebitContext produit une empreinte textuelle opaque du contexte métier d'un DEBIT —
// stockée dans Entry.ExternalRef (champ explicitement non interprété par le Provider Core, voir
// internal/provider/types.go) et relue par la gateway sur un retry pour détecter qu'un même
// betId est réutilisé avec des paramètres différents (montant, panel, round, auto-cashout).
// C'est 223CRASH (Game Product), jamais le Ledger, qui interprète cette valeur.
func encodeDebitContext(roundID, slot string, amount int64, cashoutTarget float64) string {
	return fmt.Sprintf("round=%s|panel=%s|amount=%d|autoCashout=%.2f", roundID, slot, amount, cashoutTarget)
}

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
	activeBets map[game.BetPanel]*game.Bet
}

func (c *client) sendJSON(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	c.trySend(b)
}

// trySend ne bloque jamais et ne panique jamais : c.send n'est jamais fermé.
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
		// buffer plein : client trop lent, on droppe plutôt que de bloquer le hub
	}
}

// writePump est l'unique écrivain de la connexion (gorilla n'autorise qu'un écrivain concurrent).
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
	eng          gameEngine
	jwtSecret    []byte
	walletClient walletAPI
	walletQueue  queueAPI
	ledger       ledgerAPI
	credits      *creditSettler

	// refs relie chaque DEBIT confirmé à son pari moteur (placement idempotent, voir bet_refs.go).
	refs betRefTable

	mu      sync.RWMutex
	clients map[string]*client // engineID (operatorID:playerID) -> client
}

func newGateway(eng *engine.CrashEngine, jwtSecret string, walletClient *wallet.Client, walletQueue *wallet.Queue, ldg *ledger.Ledger) *gateway {
	return &gateway{
		eng:          eng,
		jwtSecret:    []byte(jwtSecret),
		walletClient: walletClient,
		walletQueue:  walletQueue,
		ledger:       ldg,
		credits:      newCreditSettler(ldg, walletQueue, productGameID),
		clients:      make(map[string]*client),
	}
}

func (gw *gateway) register(c *client) {
	gw.mu.Lock()
	if old, ok := gw.clients[c.engineID()]; ok {
		_ = old.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(4001, "session_replaced"), time.Now().Add(time.Second))
		old.conn.Close()
	}
	gw.clients[c.engineID()] = c
	gw.mu.Unlock()
}

func (gw *gateway) unregister(c *client) {
	gw.mu.Lock()
	if cur, ok := gw.clients[c.engineID()]; ok && cur == c {
		delete(gw.clients, c.engineID())
	}
	gw.mu.Unlock()
	c.closeOnce.Do(func() { close(c.done) })
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
		c.trySend(b)
	}
}

// settleCashout centralise le crédit wallet après un cashout réussi (manuel ou auto),
// pour ne pas dupliquer la logique CREDIT entre handleCashout et checkAutoCashout.
// bet.Payout est en centimes (même unité que le DEBIT initial) — reconverti en unité
// majeure uniquement pour l'affichage client (CASHOUT_ACCEPTED).
//
// Le règlement du gain est délégué à creditSettler (credit_settle.go) : l'entrée Ledger
// EntryCredit est ouverte AVANT tout envoi, aucun callback CREDIT ne part sans TransactionID,
// seul le créateur de l'entrée enfile le callback (pas de double envoi), et un échec de Begin ou
// d'Enqueue reste récupérable (reprise en arrière-plan / Provider Recovery). Les appels sont
// bornés dans le temps : cette méthode est aussi appelée depuis la boucle de ticks (auto-cashout).
func (gw *gateway) settleCashout(c *client, bet *game.Bet) {
	if bet.Payout > 0 {
		gw.credits.Settle(creditRequest{
			OperatorID:  c.operatorID,
			PlayerID:    c.playerID,
			RoundID:     bet.RoundID,
			BetID:       bet.ID,
			AmountMinor: bet.Payout,
		})
	}

	c.mu.Lock()
	delete(c.activeBets, bet.Panel)
	c.mu.Unlock()

	if bet.Payout > 0 {
		gw.refreshBalance(c, 500*time.Millisecond, 2*time.Second, 5*time.Second)
	}

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
		activeBets: make(map[game.BetPanel]*game.Bet),
	}

	gw.register(c)
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
	c.sendJSON(map[string]any{"type": "BALANCE", "balance": centimesToUnits(balance)})
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
	log.Printf("ws: connecté player=%s", c.playerID)

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
			c.sendJSON(map[string]any{"type": "BALANCE", "balance": centimesToUnits(balance)})
		case "ping":
			c.sendJSON(map[string]any{"type": "PONG"})
		case "bet":
			gw.handleBet(c, msg)
		case "cashout":
			gw.handleCashout(c, msg)
		}
	}
}

// handleBet — flux Tranche 3A (C2) : idempotence garantie côté MongoDB via l'index unique sur
// idempotencyKey (internal/provider/ledger), jamais une simple vérification applicative.
//
// idemKey := "debit:<operatorId>:<playerId>:<betId>" — betId fourni par le client (voir ws.js).
// gw.ledger.Begin() est le point d'atomicité réel : sur N requêtes concurrentes portant le même
// betId (même process ou plusieurs instances), exactement une InsertOne réussit (created=true),
// toutes les autres se heurtent à l'index unique et relisent l'entrée existante (created=false).
//
//   - created == true  : ce goroutine est seul responsable d'appeler le wallet. Flux inchangé :
//     Debit synchrone → Confirm/Fail → PlaceBet (ou ROLLBACK si PlaceBet échoue).
//   - created == false : une entrée existe déjà pour ce betId.
//     1. Le contexte métier stocké (ExternalRef) est comparé au contexte de la requête courante
//     (round/panel/montant/autoCashout) — toute divergence est un rejet immédiat, sans aucun
//     appel wallet ni écriture Ledger supplémentaire (protection contre la réutilisation d'un
//     betId avec un payload différent).
//     2. Si l'entrée est encore PENDING (DEBIT en cours, potentiellement sur un autre appel
//     concurrent), on attend sa résolution par polling borné (waitForLedgerResolution) —
//     jamais de second appel wallet.
//     3. Le résultat déjà connu (CONFIRMED/FAILED) est utilisé pour répondre, sans jamais
//     rejouer l'opération financière.
//
// Un rejeu (created == false) ne déclenche JAMAIS de remboursement : voir placeBetOrReuseEx.
func (gw *gateway) handleBet(c *client, msg clientMessage) {
	panel, ok := slotToPanel(msg.Slot)
	if !ok {
		c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": "slot invalide"}})
		return
	}

	if !validBetID(msg.BetID) {
		c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": "betId invalide ou manquant"}})
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
	if msg.Amount > wsSecurity().maxBetUnits {
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

	// Contrôle de phase AVANT tout accès Ledger/wallet : hors fenêtre de mises, aucun mouvement
	// d'argent n'est ouvert (ni DEBIT, ni ROLLBACK compensatoire). CREATED est toléré : le moteur
	// ouvre les mises du BetManager juste avant de basculer l'état du round. La validation côté
	// moteur (PlaceBet) reste l'arbitre final pour les courses de fermeture.
	if round.State != game.StateCreated && round.State != game.StateBettingOpen {
		c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": "paris fermés"}})
		gw.announceExistingBet(c, panel, msg.Slot)
		return
	}

	cashoutTarget := noAutoCashoutSentinel
	if msg.AutoCashout != nil {
		t := *msg.AutoCashout
		if math.IsNaN(t) || math.IsInf(t, 0) || t < wsMinAutoCashout || t > wsMaxAutoCashout {
			c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": "autoCashout invalide"}})
			return
		}
		cashoutTarget = t
	}

	opCtx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	idemKey := ledger.IdempotencyKey(fmt.Sprintf("debit:%s:%s:%s", c.operatorID, c.playerID, msg.BetID))
	externalRef := ledger.ExternalRef(encodeDebitContext(round.ID, msg.Slot, amount, cashoutTarget))

	// Begin AVANT tout appel réseau : aucun mouvement d'argent sans trace Ledger préalable.
	// Si le Ledger est indisponible, on rejette sans jamais appeler le wallet.
	debitEntry, created, err := gw.ledger.Begin(opCtx, ledger.NewEntry{
		GameID:         productGameID,
		OperatorID:     ledger.OperatorID(c.operatorID),
		PlayerID:       ledger.PlayerID(c.playerID),
		ExternalRef:    externalRef,
		IdempotencyKey: idemKey,
		Type:           ledger.EntryDebit,
		AmountMinor:    amount,
	})
	if err != nil {
		log.Printf("ledger: begin debit betId=%s: %v", msg.BetID, err)
		c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": "service indisponible"}})
		return
	}

	if !created {
		if debitEntry.ExternalRef != externalRef {
			c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": "betId déjà utilisé avec des paramètres différents"}})
			return
		}

		resolved := debitEntry
		if resolved.Status == ledger.StatusPending {
			resolved, err = gw.waitForLedgerResolution(opCtx, debitEntry.TransactionID)
			if err != nil {
				log.Printf("ledger: attente résolution betId=%s txn=%s: %v", msg.BetID, debitEntry.TransactionID, err)
				c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": "débit en cours de traitement, réessayez"}})
				return
			}
		}

		switch resolved.Status {
		case ledger.StatusFailed:
			c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": "débit refusé"}})
			return
		case ledger.StatusConfirmed:
			if refunded, chkErr := gw.debitMarkedForRefund(c, resolved); chkErr != nil || refunded {
				reason := "débit annulé"
				if chkErr != nil {
					reason = "service indisponible, réessayez"
				}
				c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": reason}})
				return
			}
			gw.refreshBalance(c, 0)
			gw.placeBetOrReuse(c, msg, panel, amount, cashoutTarget, round, resolved)
			return
		default:
			log.Printf("ledger: statut inattendu après résolution betId=%s txn=%s status=%s", msg.BetID, resolved.TransactionID, resolved.Status)
			c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": "état de débit incohérent"}})
			return
		}
	}

	// Nouveau betId : si le panel porte déjà un pari en cours, ce DEBIT ne pourrait produire aucun pari.
	// On le clôture AVANT tout appel wallet (aucun mouvement d'argent) et on resynchronise le client.
	if existing, getErr := gw.eng.GetPanelBet(c.engineID(), panel); getErr == nil && existing.State == game.BetPending {
		if _, fErr := gw.ledger.Fail(opCtx, debitEntry.TransactionID, "panel occupé"); fErr != nil {
			log.Printf("ledger: fail debit (panel occupé) txn=%s: %v", debitEntry.TransactionID, fErr)
		}
		c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": errPanelOccupied.Error()}})
		gw.announceExistingBet(c, panel, msg.Slot)
		return
	}

	// created == true : ce goroutine est seul responsable de l'appel wallet pour ce betId —
	// garanti par l'index unique idempotencyKey de internal/provider/ledger (InsertOne atomique
	// côté MongoDB, jamais une simple vérification applicative).
	debitCtx, cancelDebit := context.WithTimeout(opCtx, 3*time.Second)
	balance, outcome, err := gw.walletClient.Debit(debitCtx, productGameID, debitEntry.TransactionID, c.operatorID, c.playerID, round.ID, msg.BetID, amount)
	cancelDebit()

	switch outcome {
	case wallet.OutcomeAccepted:
		confirmed, cErr := gw.ledger.Confirm(opCtx, debitEntry.TransactionID, &balance)
		if cErr != nil {
			log.Printf("ledger: confirm debit txn=%s: %v", debitEntry.TransactionID, cErr)
			confirmed = debitEntry // best effort : le DEBIT est accepté côté wallet quoi qu'il arrive au Ledger
		}
		c.sendJSON(map[string]any{"type": "BALANCE", "balance": centimesToUnits(balance)})
		gw.placeBetOrReuseEx(c, msg, panel, amount, cashoutTarget, round, confirmed, true)
	case wallet.OutcomeRejected:
		reason := ""
		if err != nil {
			reason = err.Error()
		}
		if _, fErr := gw.ledger.Fail(opCtx, debitEntry.TransactionID, reason); fErr != nil {
			log.Printf("ledger: fail debit txn=%s: %v", debitEntry.TransactionID, fErr)
		}
		c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": "débit refusé"}})
	case wallet.OutcomeUnknown:
		// Ni Confirm ni Fail : l'entrée reste PENDING. Le pari est refusé côté joueur (on ne
		// peut pas l'engager sans certitude que le débit a eu lieu), mais l'ambiguïté est tracée
		// dans le Ledger — un retry avec le même betId ne rappellera jamais le wallet tant que
		// cette entrée reste PENDING (voir la branche !created ci-dessus), il attendra sa
		// résolution ou s'arrêtera proprement sur timeout.
		log.Printf("wallet: debit outcome indéterminé txn=%s betId=%s: %v", debitEntry.TransactionID, msg.BetID, err)
		gw.markDebitForRefund(c, debitEntry, amount)
		c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": "débit indéterminé, réessayez"}})
	}
}

// waitForLedgerResolution attend qu'une entrée Ledger PENDING quitte cet état — cas d'un retry
// (même betId) arrivant pendant qu'un autre appel (même process ou un autre) est encore en train
// de résoudre le DEBIT correspondant. Poll court sur MongoDB plutôt qu'une notification en
// mémoire : fonctionne correctement même si le retry arrive sur une autre instance du serveur.
// Ne renvoie jamais un résultat inventé : sur dépassement du délai, l'appelant doit répondre par
// un rejet temporaire, jamais engager d'opération financière sur une issue encore incertaine.
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

// betRejectReason convertit une erreur interne en raison affichable : jamais d'err.Error() brut
// vers le client.
func betRejectReason(err error) string {
	switch {
	case errors.Is(err, errPanelOccupied), errors.Is(err, game.ErrBetAlreadyExists):
		return errPanelOccupied.Error()
	case errors.Is(err, game.ErrBettingClosed), errors.Is(err, game.ErrNoActiveRound), errors.Is(err, game.ErrRoundNotRunning):
		return "paris fermés"
	default:
		return "pari non accepté"
	}
}

// placeBetOrReuse engage le pari côté engine après un DEBIT confirmé retrouvé via l'idempotence
// (rejeu, fresh == false).
func (gw *gateway) placeBetOrReuse(c *client, msg clientMessage, panel game.BetPanel, amount int64, cashoutTarget float64, round *game.Round, debitEntry *ledger.Entry) {
	gw.placeBetOrReuseEx(c, msg, panel, amount, cashoutTarget, round, debitEntry, false)
}

// placeBetOrReuseEx engage le pari côté engine après un DEBIT confirmé.
//
// Le placement est idempotent par DEBIT (betRefTable, clé = TransactionID) : le créateur du débit
// et ses rejeux concurrents obtiennent le même pari, jamais deux.
//
// fresh == true : ce DEBIT vient d'être créé par cet appel. Si le placement échoue, le débit ne
// correspond à aucun pari : remboursement via rollbackConfirmedDebit.
//
// fresh == false : rejeu d'un DEBIT historique confirmé. Un échec de placement (phase fermée,
// round terminé, panel pris…) ne justifie JAMAIS un remboursement automatique : le pari peut être
// vivant ou déjà perdu. Le cas est journalisé en CRITIQUE pour réconciliation, rien d'autre
// n'est déclenché côté argent.
func (gw *gateway) placeBetOrReuseEx(c *client, msg clientMessage, panel game.BetPanel, amount int64, cashoutTarget float64, round *game.Round, debitEntry *ledger.Entry, fresh bool) {
	bet, err := gw.refs.placeOnce(gw.eng, round.ID, string(debitEntry.TransactionID), c.engineID(), panel, amount, cashoutTarget)
	if err == nil {
		if bet.State != game.BetPending {
			// Rejeu d'un pari déjà réglé (encaissé ou perdu) : rien à réactiver ni à rembourser.
			c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": "pari déjà réglé"}})
			return
		}
		b := bet
		c.mu.Lock()
		c.activeBets[panel] = &b
		c.mu.Unlock()
		c.sendJSON(map[string]any{"type": "BET_ACCEPTED", "data": map[string]any{"slot": msg.Slot}})
		return
	}

	if errors.Is(err, errBetRefStale) {
		c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": "pari déjà traité"}})
		return
	}

	if !fresh {
		gw.reportUnplacedDebit(c, msg, debitEntry, err)
		c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": betRejectReason(err)}})
		if errors.Is(err, game.ErrBetAlreadyExists) {
			gw.announceExistingBet(c, panel, msg.Slot)
		}
		return
	}

	if errors.Is(err, game.ErrBetAlreadyExists) {
		// Nouveau DEBIT sur un panel déjà occupé par un AUTRE pari (course avec le contrôle de
		// handleBet) : ce débit ne correspond à aucun pari. Remboursement, puis resynchronisation.
		gw.rollbackConfirmedDebit(c, msg, amount, round, debitEntry, errPanelOccupied)
		gw.announceExistingBet(c, panel, msg.Slot)
		return
	}

	gw.rollbackConfirmedDebit(c, msg, amount, round, debitEntry, err)
}

// reportUnplacedDebit trace un DEBIT confirmé rejoué sans pari engine retrouvable. Aucune action
// financière automatique : tous les champs nécessaires à une réconciliation manuelle sont logués.
func (gw *gateway) reportUnplacedDebit(c *client, msg clientMessage, debit *ledger.Entry, cause error) {
	log.Printf("gateway: CRITIQUE rejeu d'un DEBIT confirmé sans pari engine, AUCUN remboursement automatique txn=%s op=%s player=%s betId=%s amount=%d ref=%s: %v",
		debit.TransactionID, c.operatorID, c.playerID, msg.BetID, debit.AmountMinor, debit.ExternalRef, cause)
}

// rollbackConfirmedDebit rembourse un DEBIT déjà CONFIRMED lorsque PlaceBet échoue pour une
// raison qui n'est PAS un doublon (ErrBetAlreadyExists — voir placeBetOrReuse) : round fermé
// entre-temps, état interne incohérent, etc. Le ROLLBACK ouvre sa PROPRE entrée Ledger
// (ParentTransactionID = TransactionID du DEBIT), jamais une réécriture du DEBIT existant — la
// chaîne comptable DEBIT → ROLLBACK reste auditable. Asynchrone via la Queue, idempotent au même
// titre que le DEBIT (clé scopée operator/player/transactionId, jamais réutilisable pour un
// autre DEBIT). Réservé aux débits NEUFS (fresh == true).
func (gw *gateway) rollbackConfirmedDebit(c *client, msg clientMessage, amount int64, round *game.Round, debitEntry *ledger.Entry, placeErr error) {
	idemKey := fmt.Sprintf("rollback:%s:%s:%s", c.operatorID, c.playerID, debitEntry.TransactionID)
	rollbackEntry, _, rbErr := gw.ledger.Begin(context.Background(), ledger.NewEntry{
		GameID:              productGameID,
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
		GameID:         productGameID,
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
	c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": betRejectReason(placeErr)}})
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
