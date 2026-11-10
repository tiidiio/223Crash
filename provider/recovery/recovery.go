// Package recovery résout les entrées Ledger restées PENDING (résultat wallet indéterminé,
// crash entre Begin et l'appel wallet, dead-letter de la Queue).
//
// Principe : un renvoi avec la MÊME clé d'idempotence est sûr si l'opérateur honore
// Idempotency-Key. Le sweeper ne crée jamais de nouvelle clé pour une opération existante.
package recovery

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/tiidiio/223Gaming/provider/wallet"
	"github.com/tiidiio/223Gaming/provider/ledger"
)

type Store interface {
	ListPendingOlderThan(ctx context.Context, before time.Time) ([]ledger.Entry, error)
	GetByIdempotencyKey(ctx context.Context, operatorID ledger.OperatorID, key ledger.IdempotencyKey) (*ledger.Entry, error)
	GetByTransactionID(ctx context.Context, txnID ledger.TransactionID) (*ledger.Entry, error)
	Begin(ctx context.Context, e ledger.NewEntry) (*ledger.Entry, bool, error)
	Confirm(ctx context.Context, txnID ledger.TransactionID, operatorBalanceAfterMinor *int64) (*ledger.Entry, error)
	Fail(ctx context.Context, txnID ledger.TransactionID, reason string) (*ledger.Entry, error)
}

type Sender interface {
	Send(ctx context.Context, cb wallet.Callback) (int64, wallet.Outcome, error)
}

type Sweeper struct {
	store      Store
	sender     Sender
	startedAt  time.Time
	now        func() time.Time
	debitAfter time.Duration
	moneyAfter time.Duration
	maxAge     time.Duration
	interval   time.Duration
	batch      int
	lastStale  int
}

func New(store Store, sender Sender) *Sweeper {
	return &Sweeper{
		store:      store,
		sender:     sender,
		startedAt:  time.Now(),
		now:        time.Now,
		debitAfter: 30 * time.Second, // > timeout max du flux handleBet (6s)
		moneyAfter: 5 * time.Minute,  // > durée max des retries de la Queue (~65s)
		maxAge:     72 * time.Hour,
		interval:   15 * time.Second,
		batch:      100,
	}
}

func (s *Sweeper) Run(ctx context.Context) {
	log.Printf("recovery: sweeper actif (debit>%s, credit/rollback>%s, age max %s)", s.debitAfter, s.moneyAfter, s.maxAge)
	t := time.NewTicker(s.interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.RunOnce(ctx)
		}
	}
}

func (s *Sweeper) RunOnce(ctx context.Context) {
	now := s.now()
	entries, err := s.store.ListPendingOlderThan(ctx, now.Add(-s.debitAfter))
	if err != nil {
		log.Printf("recovery: list pending: %v", err)
		return
	}

	handled, stale := 0, 0
	for _, e := range entries {
		if ctx.Err() != nil {
			return
		}
		if e.CreatedAt.Before(now.Add(-s.maxAge)) {
			stale++
			continue
		}
		if handled >= s.batch {
			break
		}

		switch e.Type {
		case ledger.EntryDebit:
			handled++
			s.recoverDebit(ctx, e)

		case ledger.EntryCredit, ledger.EntryRollback:
			if e.CreatedAt.After(now.Add(-s.moneyAfter)) {
				continue
			}
			handled++
			s.recoverMoney(ctx, e)
		}
	}

	if stale != s.lastStale {
		s.lastStale = stale
		log.Printf("recovery: %d entrée(s) PENDING > %s ignorée(s) — réconciliation manuelle requise", stale, s.maxAge)
	}
}

// parseKey décompose "kind:op:player:last". Les identifiants ne contiennent jamais ':'.
func parseKey(key, kind string, op ledger.OperatorID, pl ledger.PlayerID) (string, bool) {
	p := strings.Split(key, ":")
	if len(p) != 4 || p[0] != kind || p[1] != string(op) || p[2] != string(pl) || p[3] == "" {
		return "", false
	}
	return p[3], true
}

func parseRound(ref ledger.ExternalRef) string {
	s := string(ref)
	if !strings.HasPrefix(s, "round=") {
		return ""
	}
	s = s[len("round="):]
	if i := strings.IndexByte(s, '|'); i >= 0 {
		s = s[:i]
	}
	return s
}

func debitCallback(e ledger.Entry) (wallet.Callback, bool) {
	bet, ok := parseKey(string(e.IdempotencyKey), "debit", e.OperatorID, e.PlayerID)
	if !ok {
		return wallet.Callback{}, false
	}

	return wallet.Callback{
		ID:             fmt.Sprintf("debit-%s-%s-%s", e.OperatorID, e.PlayerID, bet),
		Type:           wallet.CallbackDebit,
		GameID:         e.GameID,
		TransactionID:  e.TransactionID,
		OperatorID:     string(e.OperatorID),
		PlayerID:       string(e.PlayerID),
		RoundID:        parseRound(e.ExternalRef),
		BetID:          bet,
		Amount:         e.AmountMinor,
		IdempotencyKey: string(e.IdempotencyKey),
		CreatedAt:      e.CreatedAt,
	}, true
}

func creditCallback(e ledger.Entry) (wallet.Callback, bool) {
	bet, ok := parseKey(string(e.IdempotencyKey), "credit", e.OperatorID, e.PlayerID)
	if !ok {
		return wallet.Callback{}, false
	}

	return wallet.Callback{
		ID:             fmt.Sprintf("credit-%s-%s-%s", e.OperatorID, e.PlayerID, bet),
		Type:           wallet.CallbackCredit,
		GameID:         e.GameID,
		TransactionID:  e.TransactionID,
		OperatorID:     string(e.OperatorID),
		PlayerID:       string(e.PlayerID),
		RoundID:        parseRound(e.ExternalRef),
		BetID:          bet,
		Amount:         e.AmountMinor,
		IdempotencyKey: string(e.IdempotencyKey),
		CreatedAt:      e.CreatedAt,
	}, true
}

func rollbackCallback(e ledger.Entry, parent *ledger.Entry) (wallet.Callback, bool) {
	if parent == nil ||
		e.OperatorID != parent.OperatorID ||
		e.PlayerID != parent.PlayerID ||
		e.GameID != parent.GameID ||
		e.ParentTransactionID != parent.TransactionID {
		return wallet.Callback{}, false
	}

	bet, ok := parseKey(string(parent.IdempotencyKey), "debit", parent.OperatorID, parent.PlayerID)
	if !ok {
		return wallet.Callback{}, false
	}

	return wallet.Callback{
		ID:             fmt.Sprintf("rollback-%s", e.ParentTransactionID),
		Type:           wallet.CallbackRollback,
		GameID:         e.GameID,
		TransactionID:  e.TransactionID,
		OperatorID:     string(e.OperatorID),
		PlayerID:       string(e.PlayerID),
		RoundID:        parseRound(e.ExternalRef),
		BetID:          bet,
		Amount:         e.AmountMinor,
		IdempotencyKey: string(e.IdempotencyKey),
		CreatedAt:      e.CreatedAt,
	}, true
}

func (s *Sweeper) stillPending(ctx context.Context, id ledger.TransactionID) (*ledger.Entry, bool) {
	cur, err := s.store.GetByTransactionID(ctx, id)
	if err != nil {
		log.Printf("recovery: relecture txn=%s: %v", id, err)
		return nil, false
	}
	return cur, cur.Status == ledger.StatusPending
}

func (s *Sweeper) recoverDebit(ctx context.Context, e ledger.Entry) {
	if _, ok := s.stillPending(ctx, e.TransactionID); !ok {
		return
	}

	cb, ok := debitCallback(e)
	if !ok {
		log.Printf("recovery: DEBIT txn=%s clé d'idempotence non reconnue, ignoré", e.TransactionID)
		return
	}

	rbKey := ledger.IdempotencyKey(fmt.Sprintf("rollback:%s:%s:%s", e.OperatorID, e.PlayerID, e.TransactionID))
	rb, err := s.store.GetByIdempotencyKey(ctx, e.OperatorID, rbKey)
	hasMarker := err == nil
	if err != nil && !errors.Is(err, ledger.ErrEntryNotFound) {
		log.Printf("recovery: lecture marqueur rollback txn=%s: %v", e.TransactionID, err)
		return
	}

	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	bal, outcome, sendErr := s.sender.Send(sctx, cb)
	cancel()

	switch outcome {
	case wallet.OutcomeRejected:
		reason := "rejeté lors de la reprise"
		if sendErr != nil {
			reason = sendErr.Error()
		}
		if _, fErr := s.store.Fail(ctx, e.TransactionID, reason); fErr != nil {
			log.Printf("recovery: fail debit txn=%s: %v", e.TransactionID, fErr)
			return
		}
		if hasMarker && rb.Status == ledger.StatusPending {
			if _, fErr := s.store.Fail(ctx, rb.TransactionID, "débit initial rejeté : rien à rembourser"); fErr != nil {
				log.Printf("recovery: fail marqueur rollback txn=%s: %v", rb.TransactionID, fErr)
			}
		}
		log.Printf("recovery: DEBIT txn=%s FAILED (rejet certain)", e.TransactionID)

	case wallet.OutcomeAccepted:
		refund := hasMarker || e.CreatedAt.Before(s.startedAt)
		if refund {
			// Le ROLLBACK est ouvert AVANT de confirmer le DEBIT : un crash entre les deux laisse
			// le marqueur en base, et le tick suivant reprend (le renvoi est idempotent).
			rb, _, err = s.store.Begin(ctx, ledger.NewEntry{
				GameID:              e.GameID,
				OperatorID:          e.OperatorID,
				PlayerID:            e.PlayerID,
				ExternalRef:         e.ExternalRef,
				IdempotencyKey:      rbKey,
				Type:                ledger.EntryRollback,
				AmountMinor:         e.AmountMinor,
				ParentTransactionID: e.TransactionID,
			})
			if err != nil {
				log.Printf("recovery: begin rollback txn=%s: %v", e.TransactionID, err)
				return
			}
		}

		b := bal
		if _, cErr := s.store.Confirm(ctx, e.TransactionID, &b); cErr != nil {
			log.Printf("recovery: confirm debit txn=%s: %v", e.TransactionID, cErr)
			return
		}
		if !refund {
			log.Printf("recovery: WARN DEBIT txn=%s confirmé sans remboursement (entrée du process courant, pari présumé placé)", e.TransactionID)
			return
		}

		log.Printf("recovery: DEBIT txn=%s CONFIRMED tardivement, remboursement via ROLLBACK txn=%s", e.TransactionID, rb.TransactionID)
		if rb.Status == ledger.StatusPending {
			if rcb, ok := rollbackCallback(*rb, &e); ok {
				s.sendAndResolve(ctx, *rb, rcb)
			}
		}

	default:
		log.Printf("recovery: DEBIT txn=%s toujours indéterminé: %v", e.TransactionID, sendErr)
	}
}

func (s *Sweeper) recoverMoney(ctx context.Context, e ledger.Entry) {
	if _, ok := s.stillPending(ctx, e.TransactionID); !ok {
		return
	}

	var cb wallet.Callback
	var ok bool

	switch e.Type {
	case ledger.EntryCredit:
		cb, ok = creditCallback(e)

	case ledger.EntryRollback:
		parent, err := s.store.GetByTransactionID(ctx, e.ParentTransactionID)
		if err != nil {
			log.Printf("recovery: parent introuvable pour ROLLBACK txn=%s: %v", e.TransactionID, err)
			return
		}

		switch parent.Status {
		case ledger.StatusPending:
			return // résolu par recoverDebit
		case ledger.StatusFailed:
			if _, fErr := s.store.Fail(ctx, e.TransactionID, "débit initial échoué : rien à rembourser"); fErr != nil {
				log.Printf("recovery: fail rollback txn=%s: %v", e.TransactionID, fErr)
			}
			return
		}

		cb, ok = rollbackCallback(e, parent)
	}

	if !ok {
		log.Printf("recovery: %s txn=%s clé d'idempotence non reconnue, ignoré", e.Type, e.TransactionID)
		return
	}

	s.sendAndResolve(ctx, e, cb)
}

func (s *Sweeper) sendAndResolve(ctx context.Context, e ledger.Entry, cb wallet.Callback) {
	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	bal, outcome, err := s.sender.Send(sctx, cb)
	cancel()

	switch outcome {
	case wallet.OutcomeAccepted:
		b := bal
		if _, cErr := s.store.Confirm(ctx, e.TransactionID, &b); cErr != nil {
			log.Printf("recovery: confirm %s txn=%s: %v", e.Type, e.TransactionID, cErr)
			return
		}
		log.Printf("recovery: %s txn=%s CONFIRMED après reprise", e.Type, e.TransactionID)

	case wallet.OutcomeRejected:
		reason := "rejeté lors de la reprise"
		if err != nil {
			reason = err.Error()
		}
		if _, fErr := s.store.Fail(ctx, e.TransactionID, reason); fErr != nil {
			log.Printf("recovery: fail %s txn=%s: %v", e.Type, e.TransactionID, fErr)
			return
		}
		log.Printf("recovery: ALERTE %s txn=%s player=%s montant=%d REJETÉ — réconciliation manuelle requise", e.Type, e.TransactionID, e.PlayerID, e.AmountMinor)

	default:
		log.Printf("recovery: %s txn=%s toujours indéterminé: %v", e.Type, e.TransactionID, err)
	}
}
