package main

import (
	"context"
	"time"

	"github.com/tiidiio/223Crash/internal/engine"
	"github.com/tiidiio/223Crash/internal/game"
	"github.com/tiidiio/223Gaming/provider"
	"github.com/tiidiio/223Gaming/provider/ledger"
	"github.com/tiidiio/223Gaming/provider/wallet"
)

// Interfaces minimales des dépendances de la gateway. Elles décrivent la surface publique des
// types concrets (*engine.CrashEngine, *ledger.Ledger, *wallet.Client, *wallet.Queue) : la
// production est inchangée, les tests peuvent injecter des doubles déterministes sans MongoDB.

type gameEngine interface {
	Events() <-chan engine.Event
	CurrentRound() (*game.Round, error)
	CurrentFairness() (serverSeedHash, clientSeed string, nonce int64)
	CurrentMultiplier() float64
	PlaceBet(userID string, panel game.BetPanel, amount int64, cashoutTarget float64) (*game.Bet, error)
	GetPanelBet(userID string, panel game.BetPanel) (game.Bet, error)
	Cashout(betID string) (*game.Bet, error)
	UserRoundBets(userID string) []game.Bet
}

type ledgerAPI interface {
	Begin(ctx context.Context, e ledger.NewEntry) (*ledger.Entry, bool, error)
	Confirm(ctx context.Context, txnID ledger.TransactionID, operatorBalanceAfterMinor *int64) (*ledger.Entry, error)
	Fail(ctx context.Context, txnID ledger.TransactionID, reason string) (*ledger.Entry, error)
	GetByTransactionID(ctx context.Context, txnID ledger.TransactionID) (*ledger.Entry, error)
	GetByIdempotencyKey(ctx context.Context, operatorID ledger.OperatorID, key ledger.IdempotencyKey) (*ledger.Entry, error)
	ListPendingOlderThan(ctx context.Context, before time.Time) ([]ledger.Entry, error)
}

type walletAPI interface {
	Send(ctx context.Context, cb wallet.Callback) (int64, wallet.Outcome, error)
	Debit(ctx context.Context, gameID provider.GameID, txnID provider.TransactionID, operatorID, playerID, roundID, betID string, amount int64) (int64, wallet.Outcome, error)
	GetBalance(ctx context.Context, operatorID, playerID string) (int64, error)
}

type queueAPI interface {
	Enqueue(ctx context.Context, cb wallet.Callback) error
	Run(ctx context.Context)
}

var (
	_ gameEngine = (*engine.CrashEngine)(nil)
	_ ledgerAPI  = (*ledger.Ledger)(nil)
	_ walletAPI  = (*wallet.Client)(nil)
	_ queueAPI   = (*wallet.Queue)(nil)
)
