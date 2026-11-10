package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	v1 "github.com/tiidiio/223Gaming/contracts/product/v1"
	"github.com/tiidiio/223Gaming/provider/ledger"
)

func refundKey(debit *ledger.Entry) ledger.IdempotencyKey {
	if debit == nil { return "" }
	return ledger.IdempotencyKey(fmt.Sprintf("rollback:%s:%s:%s", debit.OperatorID, debit.PlayerID, debit.TransactionID))
}
func validateDebitClient(c *client, debit *ledger.Entry) error {
	if c == nil { return errors.New("client absent") }
	if debit == nil { return errors.New("entrée débit absente") }
	if c.operatorID != string(debit.OperatorID) || c.playerID != string(debit.PlayerID) {
		return errors.New("identité client différente de l'identité du débit")
	}
	return nil
}
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
	if err != nil { log.Printf("ledger: marqueur de remboursement txn=%s: %v", debit.TransactionID, err) }
}
func (gw *gateway) debitMarkedForRefund(c *client, debit *ledger.Entry) (bool, error) {
	if err := validateDebitClient(c, debit); err != nil { return false, err }
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := gw.ledger.GetByIdempotencyKey(ctx, debit.OperatorID, refundKey(debit))
	if err == nil { return true, nil }
	if errors.Is(err, ledger.ErrEntryNotFound) { return false, nil }
	return false, err
}
func (gw *gateway) rehydrateBets(c *client) {
	for _, p := range []v1.PanelID{v1.PanelA, v1.PanelB} {
		b, ok, err := gw.eng.GetPanelBet(c.engineID(), p)
		if err != nil || !ok { continue }
		if b.State != v1.BetPending { continue }
		bet := b
		c.mu.Lock()
		c.activeBets[p] = &bet
		c.mu.Unlock()
	}
}
