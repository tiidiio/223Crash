package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/tiidiio/223Gaming/223Crash/internal/game"
	"github.com/tiidiio/223Gaming/223Crash/internal/provider/ledger"
)

func refundKey(c *client, debit *ledger.Entry) ledger.IdempotencyKey {
	return ledger.IdempotencyKey(fmt.Sprintf("rollback:%s:%s:%s", c.operatorID, c.playerID, debit.TransactionID))
}

// markDebitForRefund : sur OutcomeUnknown, le joueur reçoit un rejet mais le wallet a pu débiter.
// L'entrée ROLLBACK PENDING est le marqueur « si ce débit a eu lieu, il faut le rembourser » ;
// le sweeper (internal/provider/recovery) la résout.
func (gw *gateway) markDebitForRefund(c *client, debit *ledger.Entry, amount int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _, err := gw.ledger.Begin(ctx, ledger.NewEntry{
		GameID:              productGameID,
		OperatorID:          ledger.OperatorID(c.operatorID),
		PlayerID:            ledger.PlayerID(c.playerID),
		ExternalRef:         debit.ExternalRef,
		IdempotencyKey:      refundKey(c, debit),
		Type:                ledger.EntryRollback,
		AmountMinor:         amount,
		ParentTransactionID: debit.TransactionID,
	})
	if err != nil {
		log.Printf("ledger: marqueur de remboursement txn=%s: %v", debit.TransactionID, err)
	}
}

// debitMarkedForRefund : vrai si un remboursement existe pour ce débit (le pari ne doit alors
// jamais être placé). Une erreur de lecture est renvoyée : l'appelant refuse sans placer le pari.
func (gw *gateway) debitMarkedForRefund(c *client, debit *ledger.Entry) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := gw.ledger.GetByIdempotencyKey(ctx, refundKey(c, debit))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, ledger.ErrEntryNotFound) {
		return false, nil
	}
	return false, err
}

// rehydrateBets restaure les mises PENDING du joueur après reconnexion (cashout et auto-cashout).
func (gw *gateway) rehydrateBets(c *client) {
	for _, p := range []game.BetPanel{game.Panel1, game.Panel2} {
		b, err := gw.eng.GetPanelBet(c.playerID, p)
		if err != nil || b.State != game.BetPending {
			continue
		}
		bet := b
		c.mu.Lock()
		c.activeBets[p] = &bet
		c.mu.Unlock()
	}
}
