package main

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/tiidiio/223Crash/internal/product"
	"github.com/tiidiio/223Crash/provider"
	"github.com/tiidiio/223Crash/provider/ledger"
	"github.com/tiidiio/223Crash/provider/wallet"
)

type csLedger struct {
	fn      func(int, ledger.NewEntry) (*ledger.Entry, bool, error)
	call    atomic.Int32
	entries []ledger.NewEntry
}

func (m *csLedger) Begin(_ context.Context, e ledger.NewEntry) (*ledger.Entry, bool, error) {
	c := int(m.call.Add(1))
	m.entries = append(m.entries, e)
	return m.fn(c, e)
}
func (m *csLedger) callCount() int { return int(m.call.Load()) }

type csQueue struct {
	cbs []wallet.Callback
}

func (m *csQueue) Enqueue(_ context.Context, cb wallet.Callback) error {
	m.cbs = append(m.cbs, cb)
	return nil
}
func (m *csQueue) count() int { return len(m.cbs) }

type csLogs struct{ msgs []string }

func (l *csLogs) logf(f string, a ...any) { l.msgs = append(l.msgs, fmt.Sprintf(f, a...)) }

func csEntryFrom(e ledger.NewEntry, status ledger.EntryStatus) *ledger.Entry {
	return &ledger.Entry{
		TransactionID:  provider.TransactionID("txn_test1"),
		IdempotencyKey: e.IdempotencyKey,
		GameID:         e.GameID,
		OperatorID:     e.OperatorID,
		PlayerID:       e.PlayerID,
		ExternalRef:    e.ExternalRef,
		Type:           e.Type,
		AmountMinor:    e.AmountMinor,
		Status:         status,
	}
}
func csCreated(_ int, e ledger.NewEntry) (*ledger.Entry, bool, error) {
	return csEntryFrom(e, ledger.StatusPending), true, nil
}
func csSettler(t *testing.T, led *csLedger, q *csQueue) (*creditSettler, *csLogs) {
	t.Helper()
	logs := &csLogs{}
	s := newCreditSettler(led, q, string(product.GameID))
	s.logf = logs.logf
	s.spawn = func(f func()) { f() }
	return s, logs
}
func csReq() creditRequest {
	return creditRequest{
		OperatorID: "op1", PlayerID: "pl1", RoundID: "r1", BetID: "bet-0001", AmountMinor: 2500,
		DebitTxnID: provider.TransactionID("debit-txn-objid-123"),
	}
}
func TestCreditSettle_Nominal(t *testing.T) {
	led := &csLedger{fn: csCreated}
	q := &csQueue{}
	s, _ := csSettler(t, led, q)
	s.SettleWithDebitTxn(csReq())
	if led.callCount() != 1 || q.count() != 1 {
		t.Fatalf("Begin=%d Enqueue=%d attendu 1/1", led.callCount(), q.count())
	}
	ne := led.entries[0]
	if ne.IdempotencyKey != "credit:debit-txn-objid-123" {
		t.Errorf("idemKey = %q attendu credit:debit-txn-objid-123", ne.IdempotencyKey)
	}
	cb := q.cbs[0]
	if cb.TransactionID != "txn_test1" {
		t.Errorf("TransactionID=%q", cb.TransactionID)
	}
	if cb.IdempotencyKey != "credit:debit-txn-objid-123" {
		t.Errorf("cb idemKey=%q", cb.IdempotencyKey)
	}
}
func TestCreditSettle_FallbackSansDebitTxnID(t *testing.T) {
	led := &csLedger{fn: csCreated}
	q := &csQueue{}
	s, _ := csSettler(t, led, q)
	req := csReq()
	req.DebitTxnID = ""
	s.SettleWithDebitTxn(req)
	ne := led.entries[0]
	if string(ne.IdempotencyKey) != "credit:op1:pl1:bet-0001" {
		t.Errorf("fallback key=%q", ne.IdempotencyKey)
	}
}
