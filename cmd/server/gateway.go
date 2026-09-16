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

	"github.com/tiidiio/223Gaming/223Crash/internal/engine"
	"github.com/tiidiio/223Gaming/223Crash/internal/game"
	"github.com/tiidiio/223Gaming/223Crash/internal/provider"
	"github.com/tiidiio/223Gaming/223Crash/internal/provider/ledger"
	"github.com/tiidiio/223Gaming/223Crash/internal/wallet"
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
	ledger       *ledger.Ledger

	mu      sync.RWMutex
	clients map[string]*client // playerID -> client
}

func newGateway(eng *engine.CrashEngine, jwtSecret string, walletClient *wallet.Client, walletQueue *wallet.Queue, ldg *ledger.Ledger) *gateway {
	return &gateway{
		eng:          eng,
		jwtSecret:    []byte(jwtSecret),
		walletClient: walletClient,
		walletQueue:  walletQueue,
		ledger:       ldg,
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
//
// Scope Tranche 3 : une entrée Ledger EntryCredit est ouverte et corrélée au Callback avant
// l'enqueue, mais reste PENDING après — la Queue n'est pas encore Ledger-aware (Tranche 3bis).
// Ce n'est pas une perte de traçabilité : l'entrée existe, avec son TransactionID, prête à être
// résolue dès que la Queue saura appeler Confirm/Fail. Hors scope C2 (idempotency DEBIT) —
// inchangé, à l'exception de l'adaptation mécanique au nouveau retour à 3 valeurs de Begin.
func (gw *gateway) settleCashout(c *client, bet *game.Bet) {
	if bet.Payout > 0 {
		entry, _, err := gw.ledger.Begin(context.Background(), ledger.NewEntry{
			GameID:         productGameID,
			OperatorID:     ledger.OperatorID(c.operatorID),
			PlayerID:       ledger.PlayerID(c.playerID),
			ExternalRef:    ledger.ExternalRef(bet.ID),
			IdempotencyKey: ledger.IdempotencyKey(fmt.Sprintf("credit:%s", bet.ID)),
			Type:           ledger.EntryCredit,
			AmountMinor:    bet.Payout,
		})
		if err != nil {
			log.Printf("ledger: begin credit bet=%s: %v", bet.ID, err)
		}

		cb := wallet.Callback{
			ID:             fmt.Sprintf("credit-%s", bet.ID),
			Type:           wallet.CallbackCredit,
			GameID:         productGameID,
			OperatorID:     c.operatorID,
			PlayerID:       c.playerID,
			RoundID:        bet.RoundID,
			BetID:          bet.ID,
			Amount:         bet.Payout,
			IdempotencyKey: fmt.Sprintf("credit:%s", bet.ID),
		}
		if entry != nil {
			cb.TransactionID = entry.TransactionID
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
	BetID       string   `json:"betId"`
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

	cashoutTarget := noAutoCashoutSentinel
	if msg.AutoCashout != nil {
		cashoutTarget = *msg.AutoCashout
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
			gw.placeBetOrReuse(c, msg, panel, amount, cashoutTarget, round, resolved)
			return
		default:
			log.Printf("ledger: statut inattendu après résolution betId=%s txn=%s status=%s", msg.BetID, resolved.TransactionID, resolved.Status)
			c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": "état de débit incohérent"}})
			return
		}
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
		gw.placeBetOrReuse(c, msg, panel, amount, cashoutTarget, round, confirmed)
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

// placeBetOrReuse engage le pari côté engine après un DEBIT confirmé (nouveau ou retrouvé via
// idempotency). BetManager.PlaceBet est protégé par son propre mutex et rejette tout second pari
// sur un panel déjà occupé (game.ErrBetAlreadyExists) — c'est ce mécanisme, pas un lock côté
// gateway, qui garantit qu'un DEBIT confirmé ne produit jamais deux paris engine même si
// plusieurs goroutines (créateur + retries concurrents résolus) appellent PlaceBet en parallèle
// pour le même betId : une seule gagne, les autres retrouvent le pari existant et le renvoient
// tel quel — aucune nouvelle opération financière n'est déclenchée dans les deux cas.
func (gw *gateway) placeBetOrReuse(c *client, msg clientMessage, panel game.BetPanel, amount int64, cashoutTarget float64, round *game.Round, debitEntry *ledger.Entry) {
	bet, err := gw.eng.PlaceBet(c.playerID, panel, amount, cashoutTarget)
	if err != nil {
		if errors.Is(err, game.ErrBetAlreadyExists) {
			existing, getErr := gw.eng.GetPanelBet(c.playerID, panel)
			if getErr != nil {
				// Incohérence transitoire (ex. le round a été réglé entre les deux lectures) : le
				// DEBIT reste confirmé et sain, rien à rembourser — on informe seulement le client.
				log.Printf("gateway: pari existant introuvable après ErrBetAlreadyExists player=%s panel=%s betId=%s: %v", c.playerID, panel, msg.BetID, getErr)
				c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": "pari déjà traité"}})
				return
			}
			b := existing
			c.mu.Lock()
			c.activeBets[panel] = &b
			c.mu.Unlock()
			c.sendJSON(map[string]any{"type": "BET_ACCEPTED", "data": map[string]any{"slot": msg.Slot}})
			return
		}

		gw.rollbackConfirmedDebit(c, msg, amount, round, debitEntry, err)
		return
	}

	c.mu.Lock()
	c.activeBets[panel] = bet
	c.mu.Unlock()
	c.sendJSON(map[string]any{"type": "BET_ACCEPTED", "data": map[string]any{"slot": msg.Slot}})
}

// rollbackConfirmedDebit rembourse un DEBIT déjà CONFIRMED lorsque PlaceBet échoue pour une
// raison qui n'est PAS un doublon (ErrBetAlreadyExists — voir placeBetOrReuse) : round fermé
// entre-temps, état interne incohérent, etc. Le ROLLBACK ouvre sa PROPRE entrée Ledger
// (ParentTransactionID = TransactionID du DEBIT), jamais une réécriture du DEBIT existant — la
// chaîne comptable DEBIT → ROLLBACK reste auditable. Asynchrone via la Queue, idempotent au même
// titre que le DEBIT (clé scopée operator/player/transactionId, jamais réutilisable pour un
// autre DEBIT).
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
	c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": placeErr.Error()}})
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
