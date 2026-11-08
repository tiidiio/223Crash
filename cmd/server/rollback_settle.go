package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/tiidiio/223Gaming/provider"
	"github.com/tiidiio/223Gaming/provider/ledger"
	"github.com/tiidiio/223Gaming/provider/wallet"
)

// Règlement d'un remboursement (ROLLBACK) d'un DEBIT confirmé dont le pari n'a pas pu être placé.
//
// Invariants :
//   - AUCUN callback ROLLBACK n'est enfilé sans entrée Ledger (Begin) ouverte et corrélée
//     (TransactionID) au préalable ;
//   - seul l'appelant qui CRÉE l'entrée (created == true) enfile le callback : une entrée
//     préexistante (PENDING, CONFIRMED, FAILED) n'est jamais renvoyée d'ici ;
//   - un ROLLBACK n'est ouvert que pour un DEBIT CONFIRMED. Un DEBIT dont l'issue est inconnue
//     ne se rembourse pas sur supposition : il relève de la réconciliation (Recovery) ;
//   - si Begin échoue, rien n'est envoyé ; la reprise en arrière-plan réessaie, avec la MÊME clé
//     d'idempotence (rollback:<operator>:<player>:<debitTxn>, identique à refundKey) ;
//   - si Enqueue échoue après Begin, l'entrée reste PENDING : reprise par le Recovery.

type rollbackLedger interface {
	Begin(ctx context.Context, e ledger.NewEntry) (*ledger.Entry, bool, error)
	Confirm(ctx context.Context, txnID ledger.TransactionID, operatorBalanceAfterMinor *int64) (*ledger.Entry, error)
}

type rollbackQueue interface {
	Enqueue(ctx context.Context, cb wallet.Callback) error
}

// rollbackRequest décrit un remboursement. WalletAccepted n'est vrai que si CE processus vient
// d'obtenir OutcomeAccepted du wallet pour ce DEBIT : seule cette preuve autorise à réparer un
// Confirm Ledger raté avant de rembourser.
type rollbackRequest struct {
	Debit          *ledger.Entry
	RoundID        string
	BetID          string
	WalletAccepted bool
}

func (r rollbackRequest) describe() string {
	if r.Debit == nil {
		return fmt.Sprintf("debit=<nil> round=%s bet=%s", r.RoundID, r.BetID)
	}
	return fmt.Sprintf("debit=%s op=%s player=%s round=%s bet=%s amount=%d status=%s",
		r.Debit.TransactionID, r.Debit.OperatorID, r.Debit.PlayerID, r.RoundID, r.BetID, r.Debit.AmountMinor, r.Debit.Status)
}

type rollbackOutcome int

const (
	// rollbackQueued : entrée ROLLBACK créée et callback enfilé.
	rollbackQueued rollbackOutcome = iota
	// rollbackRecoverable : entrée créée (PENDING) mais Enqueue en échec — reprise par le Recovery.
	rollbackRecoverable
	// rollbackAlreadyKnown : une entrée existait déjà ; rien n'est renvoyé d'ici.
	rollbackAlreadyKnown
	// rollbackDeferred : opération impossible pour l'instant ; reprise en arrière-plan, rien envoyé.
	rollbackDeferred
	// rollbackNeedsReview : requête invalide, débit non confirmé/FAILED ou entrée incohérente ;
	// rien envoyé, réconciliation nécessaire.
	rollbackNeedsReview
)

// rollbackSettler règle les remboursements. Les champs fonction sont injectables pour les tests.
type rollbackSettler struct {
	led    rollbackLedger
	queue  rollbackQueue
	gameID provider.GameID

	logf  func(format string, args ...any)
	sleep func(time.Duration)
	spawn func(func())

	attemptTimeout time.Duration
	bgDelayMin     time.Duration
	bgDelayMax     time.Duration
	bgMaxAttempts  int

	mu       sync.Mutex
	inflight map[string]struct{}
}

func newRollbackSettler(led rollbackLedger, q rollbackQueue, gameID provider.GameID) *rollbackSettler {
	return &rollbackSettler{
		led:            led,
		queue:          q,
		gameID:         gameID,
		logf:           log.Printf,
		sleep:          time.Sleep,
		spawn:          func(f func()) { go f() },
		attemptTimeout: creditAttemptTimeout,
		bgDelayMin:     creditBgDelayMin,
		bgDelayMax:     creditBgDelayMax,
		bgMaxAttempts:  creditBgMaxAttempts,
		inflight:       make(map[string]struct{}),
	}
}

func permanentRollbackBeginError(err error) bool {
	return permanentBeginError(err) || errors.Is(err, ledger.ErrParentTransactionIDRequired)
}

// Settle ouvre l'entrée ROLLBACK puis enfile le callback. Borné dans le temps : chaque appel
// Ledger/Queue est limité à attemptTimeout ; les reprises passent en arrière-plan.
func (s *rollbackSettler) Settle(req rollbackRequest) rollbackOutcome {
	out, retry := s.attempt(req)
	if retry {
		s.scheduleRetry(req)
		return rollbackDeferred
	}
	return out
}

// attempt exécute une tentative complète. Le bool indique qu'une nouvelle tentative a du sens
// (erreur transitoire) ; sinon l'outcome est définitif.
func (s *rollbackSettler) attempt(req rollbackRequest) (rollbackOutcome, bool) {
	d := req.Debit
	if d == nil || d.Type != ledger.EntryDebit || d.AmountMinor <= 0 || d.TransactionID == "" {
		s.logf("ledger: CRITIQUE rollback refusé, débit absent ou invalide, aucun envoi wallet %s", req.describe())
		return rollbackNeedsReview, false
	}

	switch d.Status {
	case ledger.StatusConfirmed:
	case ledger.StatusFailed:
		s.logf("ledger: CRITIQUE rollback refusé, débit FAILED, aucun envoi wallet %s", req.describe())
		return rollbackNeedsReview, false
	default:
		if !req.WalletAccepted {
			s.logf("ledger: CRITIQUE rollback refusé, débit non confirmé (issue inconnue), réconciliation requise %s", req.describe())
			return rollbackNeedsReview, false
		}
		// Le wallet a accepté ce DEBIT dans ce process : on répare le Confirm manquant (idempotent,
		// sans solde plutôt qu'un faux solde) avant d'ouvrir le ROLLBACK.
		ctx, cancel := context.WithTimeout(context.Background(), s.attemptTimeout)
		_, err := s.led.Confirm(ctx, d.TransactionID, nil)
		cancel()
		if errors.Is(err, ledger.ErrStateConflict) {
			s.logf("ledger: CRITIQUE réparation Confirm du débit en conflit d'état, aucun envoi wallet %s: %v", req.describe(), err)
			return rollbackNeedsReview, false
		}
		if err != nil {
			s.logf("ledger: CRITIQUE réparation Confirm du débit en échec, rollback différé %s: %v", req.describe(), err)
			return rollbackDeferred, true
		}
	}

	entry, created, err := s.beginOnce(d)
	if err != nil {
		if permanentRollbackBeginError(err) {
			s.logf("ledger: CRITIQUE begin rollback refusé définitivement, aucun envoi wallet %s: %v", req.describe(), err)
			return rollbackNeedsReview, false
		}
		s.logf("ledger: CRITIQUE begin rollback en échec, aucun envoi wallet, reprise en arrière-plan %s: %v", req.describe(), err)
		return rollbackDeferred, true
	}
	return s.afterBegin(req, entry, created), false
}

func (s *rollbackSettler) beginOnce(d *ledger.Entry) (*ledger.Entry, bool, error) {
	gameID := d.GameID
	if gameID == "" {
		gameID = s.gameID
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.attemptTimeout)
	defer cancel()
	return s.led.Begin(ctx, ledger.NewEntry{
		GameID:              gameID,
		OperatorID:          d.OperatorID,
		PlayerID:            d.PlayerID,
		ExternalRef:         d.ExternalRef,
		IdempotencyKey:      refundKey(d),
		Type:                ledger.EntryRollback,
		AmountMinor:         d.AmountMinor,
		ParentTransactionID: d.TransactionID,
	})
}

func (s *rollbackSettler) afterBegin(req rollbackRequest, e *ledger.Entry, created bool) rollbackOutcome {
	if e == nil {
		s.logf("ledger: CRITIQUE begin rollback sans entrée retournée, aucun envoi wallet %s", req.describe())
		return rollbackNeedsReview
	}
	d := req.Debit
	if !created {
		return s.existing(req, e)
	}

	cb := wallet.Callback{
		ID:             fmt.Sprintf("rollback-%s", d.TransactionID),
		Type:           wallet.CallbackRollback,
		GameID:         d.GameID,
		TransactionID:  e.TransactionID,
		OperatorID:     string(d.OperatorID),
		PlayerID:       string(d.PlayerID),
		RoundID:        req.RoundID,
		BetID:          req.BetID,
		Amount:         d.AmountMinor,
		IdempotencyKey: string(refundKey(d)),
	}
	if cb.GameID == "" {
		cb.GameID = s.gameID
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.attemptTimeout)
	err := s.queue.Enqueue(ctx, cb)
	cancel()
	if err != nil {
		s.logf("wallet: ALERTE enqueue ROLLBACK en échec txn=%s %s: %v — entrée Ledger PENDING, reprise par le Recovery", e.TransactionID, req.describe(), err)
		return rollbackRecoverable
	}
	return rollbackQueued
}

// existing traite une entrée ROLLBACK préexistante : jamais de second envoi depuis ici.
func (s *rollbackSettler) existing(req rollbackRequest, e *ledger.Entry) rollbackOutcome {
	d := req.Debit
	if e.Type != ledger.EntryRollback ||
		e.ParentTransactionID != d.TransactionID ||
		e.AmountMinor != d.AmountMinor ||
		e.OperatorID != d.OperatorID ||
		e.PlayerID != d.PlayerID {
		s.logf("ledger: CRITIQUE entrée rollback existante incohérente txn=%s (type=%s parent=%s amount=%d) pour %s — aucun envoi", e.TransactionID, e.Type, e.ParentTransactionID, e.AmountMinor, req.describe())
		return rollbackNeedsReview
	}
	switch e.Status {
	case ledger.StatusFailed:
		s.logf("ledger: CRITIQUE rollback déjà marqué FAILED txn=%s %s — réconciliation manuelle requise, aucun renvoi", e.TransactionID, req.describe())
		return rollbackNeedsReview
	case ledger.StatusPending:
		s.logf("ledger: rollback déjà enregistré PENDING txn=%s %s — pris en charge par la Queue ou le Recovery, aucun second envoi", e.TransactionID, req.describe())
	}
	return rollbackAlreadyKnown
}

// scheduleRetry relance attempt en arrière-plan (backoff plafonné). Une seule reprise par débit.
func (s *rollbackSettler) scheduleRetry(req rollbackRequest) {
	key := "rollback:" + string(req.Debit.TransactionID)
	if !s.claim(key) {
		return
	}
	s.spawn(func() {
		defer s.release(key)
		delay := s.bgDelayMin
		for n := 1; n <= s.bgMaxAttempts; n++ {
			s.sleep(delay)
			out, retry := s.attempt(req)
			if !retry {
				s.logf("ledger: rollback traité après %d reprise(s) en arrière-plan outcome=%d %s", n, out, req.describe())
				return
			}
			delay *= 2
			if delay > s.bgDelayMax {
				delay = s.bgDelayMax
			}
		}
		s.logf("ledger: CRITIQUE rollback NON enregistré après %d reprises, intervention manuelle requise %s", s.bgMaxAttempts, req.describe())
	})
}

// confirmInBackground répare un Confirm Ledger de DEBIT raté alors que le wallet a accepté le
// débit et que le pari a été placé. Confirm est idempotent ; ErrStateConflict arrête la reprise
// (incohérence réelle, jamais forcée).
func (s *rollbackSettler) confirmInBackground(txn ledger.TransactionID, balance *int64) {
	key := "confirm:" + string(txn)
	if !s.claim(key) {
		return
	}
	s.spawn(func() {
		defer s.release(key)
		delay := s.bgDelayMin
		for n := 1; n <= s.bgMaxAttempts; n++ {
			s.sleep(delay)
			ctx, cancel := context.WithTimeout(context.Background(), s.attemptTimeout)
			_, err := s.led.Confirm(ctx, txn, balance)
			cancel()
			if err == nil {
				s.logf("ledger: confirm débit réparé après %d reprise(s) txn=%s", n, txn)
				return
			}
			if errors.Is(err, ledger.ErrStateConflict) {
				s.logf("ledger: CRITIQUE confirm débit en conflit d'état txn=%s: %v", txn, err)
				return
			}
			delay *= 2
			if delay > s.bgDelayMax {
				delay = s.bgDelayMax
			}
		}
		s.logf("ledger: CRITIQUE confirm débit NON réparé après %d reprises txn=%s, intervention manuelle requise", s.bgMaxAttempts, txn)
	})
}

func (s *rollbackSettler) claim(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, busy := s.inflight[key]; busy {
		return false
	}
	s.inflight[key] = struct{}{}
	return true
}

func (s *rollbackSettler) release(key string) {
	s.mu.Lock()
	delete(s.inflight, key)
	s.mu.Unlock()
}
