package main

import (
	"context"
	"fmt"
	"log"

	"github.com/tiidiio/223Crash/provider"
	"github.com/tiidiio/223Crash/provider/ledger"
	"github.com/tiidiio/223Crash/provider/wallet"
)

type creditRequest struct {
	OperatorID  string
	PlayerID    string
	RoundID     string
	BetID       string
	AmountMinor int64
	DebitTxnID  provider.TransactionID // FIX BUG1 : ObjectID unique, jamais réutilisé après restart
}

type ledgerBeginner interface {
	Begin(ctx context.Context, e ledger.NewEntry) (*ledger.Entry, bool, error)
}

type queueEnqueuer interface {
	Enqueue(ctx context.Context, cb wallet.Callback) error
}

type creditSettler struct {
	ledger ledgerBeginner
	queue  queueEnqueuer
	gameID string
	logf   func(string, ...any)
	sleep  func(dur func() int) // placeholder overwritten in prod, kept for test compatibility
	spawn  func(f func())
}

func newCreditSettler(ldg ledgerBeginner, q queueEnqueuer, gameID string) *creditSettler {
	return &creditSettler{
		ledger: ldg,
		queue:  q,
		gameID: gameID,
		logf:   func(f string, a ...any) { log.Printf(f, a...) },
		spawn:  func(f func()) { go f() },
	}
}

func (s *creditSettler) Settle(req creditRequest) {
	s.SettleWithDebitTxn(req)
}

func (s *creditSettler) SettleWithDebitTxn(req creditRequest) {
	if req.AmountMinor <= 0 {
		return
	}
	var idemKey string
	if req.DebitTxnID != "" {
		// FIX BUG 1 : clé unique basée sur debitTxnID (ObjectID Mongo unique, jamais réutilisé après restart)
		idemKey = fmt.Sprintf("credit:%s", req.DebitTxnID)
	} else {
		log.Printf("creditSettler: CRITIQUE fallback betID sans debitTxnID bet=%s (risque collision après restart)", req.BetID)
		idemKey = fmt.Sprintf("credit:%s:%s:%s", req.OperatorID, req.PlayerID, req.BetID)
	}
	ctx := context.Background()
	entry, _, err := s.ledger.Begin(ctx, ledger.NewEntry{
		GameID:         ledger.GameID(s.gameID),
		OperatorID:     ledger.OperatorID(req.OperatorID),
		PlayerID:       ledger.PlayerID(req.PlayerID),
		ExternalRef:    ledger.ExternalRef(fmt.Sprintf("credit:%s:%s", req.RoundID, req.BetID)),
		IdempotencyKey: ledger.IdempotencyKey(idemKey),
		Type:           ledger.EntryCredit,
		AmountMinor:    req.AmountMinor,
	})
	if err != nil {
		log.Printf("ledger: begin credit bet=%s: %v", req.BetID, err)
		return
	}
	cb := wallet.Callback{
		ID:             fmt.Sprintf("credit-%s", req.BetID),
		Type:           wallet.CallbackCredit,
		GameID:         provider.GameID(s.gameID),
		OperatorID:     req.OperatorID,
		PlayerID:       req.PlayerID,
		RoundID:        req.RoundID,
		BetID:          req.BetID,
		Amount:         req.AmountMinor,
		IdempotencyKey: idemKey,
		TransactionID:  entry.TransactionID,
	}
	if qErr := s.queue.Enqueue(ctx, cb); qErr != nil {
		log.Printf("wallet: enqueue CREDIT bet=%s: %v", req.BetID, qErr)
	}
}
