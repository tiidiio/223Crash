package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/tiidiio/223Crash/internal/betstore"
	"github.com/tiidiio/223Crash/internal/game"
	"github.com/tiidiio/223Gaming/provider/ledger"
)

// Persistance durable des paris (internal/betstore). Tout est inactif tant que gw.bets == nil.
//
// Règles :
//   - un pari n'est placé dans le moteur que si son document durable existe et est PENDING pour
//     CETTE instance ;
//   - un échec d'écriture du document d'un DEBIT neuf ne laisse aucun pari : le débit est remboursé ;
//   - un document dans un autre état, ou appartenant à une autre instance, ne produit jamais de
//     pari : aucune décision financière automatique, le dossier reste visible (RECONCILING).

var (
	errBetNotPersisted = errors.New("gateway: pari non persisté")
	errBetStateBlocked = errors.New("gateway: pari bloqué par son état durable")
)

const (
	betStoreTimeout  = 2 * time.Second
	betStoreAttempts = 3

	// orphanGrace doit dépasser la durée maximale d'un round (≈ 3 min à 50000x avec
	// GrowthFactor 0.00006) : un pari PENDING d'une autre instance plus ancien que ce délai
	// n'a plus de round vivant.
	orphanGrace      = 10 * time.Minute
	orphanSweepEvery = time.Minute
)

func newInstanceID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("inst-%d", time.Now().UnixNano())
	}
	return "inst-" + hex.EncodeToString(b)
}

// enableBetPersistence active la persistance, bascule en RECONCILING les paris orphelins d'un
// processus précédent, puis relance ce balayage périodiquement.
func (gw *gateway) enableBetPersistence(ctx context.Context, st betstore.VoidStore, instanceID string, grace time.Duration) error {
	if st == nil || instanceID == "" {
		return errors.New("bet persistence: store et instanceID requis")
	}
	gw.bets = st
	gw.instanceID = instanceID
	gw.sweepOrphans(ctx, grace)

	go func() {
		t := time.NewTicker(orphanSweepEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				gw.sweepOrphans(ctx, grace)
			}
		}
	}()
	log.Printf("betstore: persistance active instance=%s grace=%s", instanceID, grace)
	return nil
}

func (gw *gateway) sweepOrphans(ctx context.Context, grace time.Duration) {
	sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	n, err := gw.bets.ReconcileOrphans(sctx, gw.instanceID, time.Now().Add(-grace),
		"instance précédente : issue du round inconnue, ni crédit ni remboursement automatiques")
	if err != nil {
		log.Printf("betstore: balayage des orphelins en échec: %v", err)
		return
	}
	if n > 0 {
		log.Printf("betstore: ALERTE %d pari(s) PENDING d'une autre instance passés en RECONCILING", n)
	}
}

// persistNewBet crée (ou retrouve) le document durable du pari. nil = le pari peut être placé.
func (gw *gateway) persistNewBet(c *client, msg clientMessage, panel game.BetPanel, amount int64, target float64, round *game.Round, debit *ledger.Entry) error {
	if gw.bets == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), betStoreTimeout)
	defer cancel()

	b, created, err := gw.bets.Insert(ctx, betstore.Bet{
		DebitTransactionID: string(debit.TransactionID),
		ClientBetID:        msg.BetID,
		OperatorID:         c.operatorID,
		PlayerID:           c.playerID,
		GameID:             string(productGameID),
		RoundID:            round.ID,
		Panel:              uint8(panel),
		AmountMinor:        amount,
		CashoutTarget:      target,
		InstanceID:         gw.instanceID,
	})
	if err != nil {
		return fmt.Errorf("%w: %v", errBetNotPersisted, err)
	}
	if created {
		return nil
	}
	if b.State != betstore.StatePending {
		return fmt.Errorf("%w: état=%s", errBetStateBlocked, b.State)
	}
	if b.InstanceID != gw.instanceID {
		if _, mErr := gw.bets.MarkReconciling(ctx, c.operatorID, string(debit.TransactionID), "rejeu depuis une autre instance : round d'origine inconnu"); mErr != nil {
			log.Printf("betstore: CRITIQUE marquage RECONCILING impossible txn=%s: %v", debit.TransactionID, mErr)
		}
		return fmt.Errorf("%w: pari d'une autre instance", errBetStateBlocked)
	}
	return nil
}

// onPersistFailure traite un pari qui ne peut pas être placé faute de document durable valide.
func (gw *gateway) onPersistFailure(c *client, msg clientMessage, amount int64, round *game.Round, debit *ledger.Entry, fresh bool, perr error) {
	if errors.Is(perr, errBetStateBlocked) {
		log.Printf("gateway: CRITIQUE pari non placé, AUCUN remboursement automatique txn=%s op=%s player=%s betId=%s: %v",
			debit.TransactionID, c.operatorID, c.playerID, msg.BetID, perr)
		c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": "pari à vérifier"}})
		return
	}
	log.Printf("gateway: CRITIQUE pari non persisté txn=%s op=%s player=%s betId=%s fresh=%v: %v",
		debit.TransactionID, c.operatorID, c.playerID, msg.BetID, fresh, perr)
	if !fresh {
		c.sendJSON(map[string]any{"type": "BET_REJECTED", "data": map[string]any{"slot": msg.Slot, "reason": "service indisponible, réessayez"}})
		return
	}
	// Débit neuf, aucun pari placé : annulation du document (au cas où l'écriture aurait abouti
	// malgré l'erreur) puis remboursement.
	gw.voidBet(debit, "écriture du pari en échec, débit remboursé")
	gw.rollbackConfirmedDebit(c, msg, amount, round, debit, errBetNotPersisted)
}

// voidBet passe le document en VOIDED (aucun pari placé, débit remboursé). Un document absent
// n'est pas une anomalie ici.
func (gw *gateway) voidBet(debit *ledger.Entry, reason string) {
	if gw.bets == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), betStoreTimeout)
	defer cancel()
	_, err := gw.bets.MarkVoided(ctx, string(debit.OperatorID), string(debit.TransactionID), reason)
	if err != nil && !errors.Is(err, betstore.ErrNotFound) {
		log.Printf("betstore: CRITIQUE annulation du pari impossible txn=%s: %v", debit.TransactionID, err)
	}
}

// reconcileUnplaced rend visible un DEBIT confirmé rejoué sans pari retrouvable.
func (gw *gateway) reconcileUnplaced(debit *ledger.Entry, reason string) {
	if gw.bets == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), betStoreTimeout)
	defer cancel()
	_, err := gw.bets.MarkReconciling(ctx, string(debit.OperatorID), string(debit.TransactionID), reason)
	if err != nil && !errors.Is(err, betstore.ErrNotFound) && !errors.Is(err, betstore.ErrStateConflict) {
		log.Printf("betstore: CRITIQUE marquage RECONCILING impossible txn=%s: %v", debit.TransactionID, err)
	}
}

// persistCashout enregistre durablement l'encaissement AVANT tout crédit. Renvoie l'identifiant
// à utiliser dans la clé du CREDIT et ok=false si le crédit ne doit PAS partir.
//
//   - sans persistance : comportement historique (ID moteur) ;
//   - conflit d'état, pari absent ou paramètres invalides : aucun crédit (un pari LOST ou
//     RECONCILING n'est jamais encaissé automatiquement) ;
//   - erreur d'infrastructure persistante : le crédit part quand même (le gain est dû, la clé
//     d'idempotence Ledger interdit le doublon), le document reste PENDING et sera visible en
//     réconciliation.
func (gw *gateway) persistCashout(c *client, bet *game.Bet) (string, bool) {
	if gw.bets == nil {
		return bet.ID, true
	}
	ref, found := gw.refs.lookup(bet.ID)
	if !found {
		log.Printf("gateway: CRITIQUE cashout sans identité durable, AUCUN crédit émis op=%s player=%s bet=%s round=%s payout=%d",
			c.operatorID, c.playerID, bet.ID, bet.RoundID, bet.Payout)
		return "", false
	}

	var err error
	for i := 0; i < betStoreAttempts; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), betStoreTimeout)
		_, err = gw.bets.MarkCashedOut(ctx, ref.operatorID, ref.txn, bet.CashoutMulti, bet.Payout, bet.CashoutAt)
		cancel()
		if err == nil {
			return ref.txn, true
		}
		if errors.Is(err, betstore.ErrStateConflict) || errors.Is(err, betstore.ErrNotFound) || errors.Is(err, betstore.ErrInvalid) {
			log.Printf("gateway: CRITIQUE cashout refusé par l'état durable, AUCUN crédit émis txn=%s op=%s player=%s payout=%d: %v",
				ref.txn, ref.operatorID, c.playerID, bet.Payout, err)
			return "", false
		}
		time.Sleep(time.Duration(i+1) * 100 * time.Millisecond)
	}
	log.Printf("gateway: CRITIQUE cashout non enregistré après %d tentatives, crédit émis (idempotent) txn=%s op=%s payout=%d: %v",
		betStoreAttempts, ref.txn, ref.operatorID, bet.Payout, err)
	return ref.txn, true
}

// persistLost enregistre les paris perdus d'un crash. Les identités sont résolues tout de suite
// (la table est remise à zéro au round suivant), l'écriture se fait hors de la boucle d'événements.
func (gw *gateway) persistLost(lost []game.Bet) {
	if gw.bets == nil || len(lost) == 0 {
		return
	}
	refs := make([]betRef, 0, len(lost))
	for _, b := range lost {
		ref, ok := gw.refs.lookup(b.ID)
		if !ok {
			log.Printf("betstore: CRITIQUE pari perdu sans identité durable user=%s bet=%s round=%s", b.UserID, b.ID, b.RoundID)
			continue
		}
		refs = append(refs, ref)
	}
	at := time.Now()
	go func() {
		for _, ref := range refs {
			var err error
			for i := 0; i < betStoreAttempts; i++ {
				ctx, cancel := context.WithTimeout(context.Background(), betStoreTimeout)
				_, err = gw.bets.MarkLost(ctx, ref.operatorID, ref.txn, at)
				cancel()
				if err == nil || errors.Is(err, betstore.ErrStateConflict) || errors.Is(err, betstore.ErrNotFound) {
					break
				}
				time.Sleep(time.Duration(i+1) * 100 * time.Millisecond)
			}
			if err != nil {
				log.Printf("betstore: CRITIQUE pari perdu non enregistré txn=%s op=%s: %v", ref.txn, ref.operatorID, err)
			}
		}
	}()
}
