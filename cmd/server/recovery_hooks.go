package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/tiidiio/223Gaming/223Crash/internal/game"
	"github.com/tiidiio/223Gaming/internal/provider/ledger"
)

func refundKey(debit *ledger.Entry) ledger.IdempotencyKey {
	if debit == nil {
		return ""
	}
	return ledger.IdempotencyKey(fmt.Sprintf(
		"rollback:%s:%s:%s",
		debit.OperatorID,
		debit.PlayerID,
		debit.TransactionID,
	))
}

func validateDebitClient(c *client, debit *ledger.Entry) error {
	if c == nil {
		return errors.New("client absent")
	}
	if debit == nil {
		return errors.New("entrée débit absente")
	}
	if c.operatorID != string(debit.OperatorID) || c.playerID != string(debit.PlayerID) {
		return errors.New("identité client différente de l'identité du débit")
	}
	return nil
}

// markDebitForRefund : sur OutcomeUnknown, le joueur reçoit un rejet mais le wallet a pu débiter.
// L'entrée ROLLBACK PENDING est le marqueur « si ce débit a eu lieu, il faut le rembourser » ;
// le sweeper (internal/provider/recovery) la résout.
func (gw *gateway) markDebitForRefund(c *client, debit *ledger.Entry, amount int64) {
	if err := validateDebitClient(c, debit); err != nil {
		log.Printf("ledger: marqueur de remboursement refusé: %v", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, _, err := gw.ledger.Begin(ctx, ledger.NewEntry{
		GameID:              debit.GameID,
		OperatorID:          debit.OperatorID,
		PlayerID:            debit.PlayerID,
		ExternalRef:         debit.ExternalRef,
		IdempotencyKey:      refundKey(debit),
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
	if err := validateDebitClient(c, debit); err != nil {
		return false, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err := gw.ledger.GetByIdempotencyKey(ctx, debit.OperatorID, refundKey(debit))
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
		b, err := gw.eng.GetPanelBet(c.engineID(), p)
		if err != nil || b.State != game.BetPending {
			continue
		}
		bet := b
		c.mu.Lock()
		c.activeBets[p] = &bet
		c.mu.Unlock()
	}
}
