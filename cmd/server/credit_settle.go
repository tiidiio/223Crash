package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/tiidiio/223Gaming/provider"
	"github.com/tiidiio/223Gaming/provider/wallet"
	"github.com/tiidiio/223Gaming/provider/ledger"
)

// Règlement d'un gain (CREDIT) après cashout.
//
// Invariant : AUCUN callback CREDIT n'est enfilé vers le wallet opérateur sans qu'une entrée Ledger
// (Begin) ait été ouverte et corrélée (TransactionID) au préalable. Un gain payé sans trace Ledger
// serait invisible pour le Recovery, la réconciliation et le suivi des revenus du fournisseur.
//
// Un gain n'est jamais abandonné silencieusement :
//   - Begin échoue (Mongo indisponible, timeout) : rien n'est envoyé au wallet ; le gain reste en
//     mémoire dans une goroutine de reprise (backoff plafonné, fenêtre alignée sur le Recovery) et
//     chaque échec est journalisé avec tous les champs nécessaires à un rejeu manuel.
//   - Begin réussit, Enqueue échoue : l'entrée Ledger reste PENDING ; le sweeper
//     (internal/provider/recovery) la reprend avec la MÊME clé d'idempotence.
//
// Aucun double paiement : seul l'appelant qui CRÉE l'entrée (created == true) enfile le callback.
// Une entrée préexistante n'est jamais renvoyée d'ici — elle appartient à la Queue ou au Recovery.

// creditLedger est la partie de *ledger.Ledger nécessaire au règlement d'un gain.
type creditLedger interface {
	Begin(ctx context.Context, e ledger.NewEntry) (*ledger.Entry, bool, error)
}

// creditQueue est la partie de *wallet.Queue nécessaire au règlement d'un gain.
type creditQueue interface {
	Enqueue(ctx context.Context, cb wallet.Callback) error
}

// creditRequest décrit un gain à régler. AmountMinor est en centimes (int64, jamais float64).
type creditRequest struct {
	OperatorID  string
	PlayerID    string
	RoundID     string
	BetID       string
	AmountMinor int64
}

func (r creditRequest) idempotencyKey() ledger.IdempotencyKey {
	return ledger.IdempotencyKey(fmt.Sprintf("credit:%s:%s:%s", r.OperatorID, r.PlayerID, r.BetID))
}

func (r creditRequest) externalRef() ledger.ExternalRef {
	return ledger.ExternalRef(fmt.Sprintf("round=%s|bet=%s", r.RoundID, r.BetID))
}

func (r creditRequest) callbackID() string {
	return fmt.Sprintf("credit-%s-%s-%s", r.OperatorID, r.PlayerID, r.BetID)
}

func (r creditRequest) describe() string {
	return fmt.Sprintf("op=%s player=%s round=%s bet=%s amount=%d", r.OperatorID, r.PlayerID, r.RoundID, r.BetID, r.AmountMinor)
}

// creditOutcome résume ce que Settle a fait ; utilisé par les tests et les logs.
type creditOutcome int

const (
	// creditQueued : entrée Ledger créée et callback enfilé.
	creditQueued creditOutcome = iota
	// creditRecoverable : entrée Ledger créée (PENDING) mais Enqueue en échec — reprise par le sweeper.
	creditRecoverable
	// creditAlreadyKnown : une entrée existait déjà pour cette clé ; rien n'est renvoyé d'ici.
	creditAlreadyKnown
	// creditDeferred : Begin impossible pour l'instant ; reprise en arrière-plan, rien envoyé au wallet.
	creditDeferred
	// creditNeedsReview : requête invalide ou entrée existante incohérente/FAILED ; rien envoyé,
	// intervention (réconciliation) nécessaire.
	creditNeedsReview
)

const (
	creditAttemptTimeout = 3 * time.Second
	creditBgDelayMin     = time.Second
	creditBgDelayMax     = 30 * time.Second
	// 8640 tentatives à 30 s d'intervalle ≈ 72 h, aligné sur la fenêtre maxAge du Recovery.
	creditBgMaxAttempts = 8640
)

// creditSettler règle les gains. Les champs fonction sont injectables pour les tests.
type creditSettler struct {
	led    creditLedger
	queue  creditQueue
	gameID provider.GameID

	logf  func(format string, args ...any)
	sleep func(time.Duration)
	spawn func(func())

	attemptTimeout time.Duration
	bgDelayMin     time.Duration
	bgDelayMax     time.Duration
	bgMaxAttempts  int

	mu       sync.Mutex
	inflight map[ledger.IdempotencyKey]struct{}
}

func newCreditSettler(led creditLedger, q creditQueue, gameID provider.GameID) *creditSettler {
	return &creditSettler{
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
		inflight:       make(map[ledger.IdempotencyKey]struct{}),
	}
}

// permanentBeginError : erreurs de validation qu'aucune nouvelle tentative ne corrigera.
func permanentBeginError(err error) bool {
	for _, target := range []error{
		ledger.ErrIdempotencyKeyRequired,
		ledger.ErrGameIDRequired,
		ledger.ErrOperatorIDRequired,
		ledger.ErrPlayerIDRequired,
		ledger.ErrInvalidAmount,
		ledger.ErrInvalidEntryType,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// Settle ouvre l'entrée Ledger du gain puis enfile le callback CREDIT. Borné dans le temps : au
// plus un Begin et un Enqueue de attemptTimeout chacun (appelé depuis la boucle de ticks pour les
// auto-cashouts, il ne doit jamais bloquer indéfiniment).
func (s *creditSettler) Settle(req creditRequest) creditOutcome {
	if req.AmountMinor <= 0 {
		s.logf("ledger: gain ignoré (montant non positif) %s", req.describe())
		return creditNeedsReview
	}

	entry, created, err := s.beginOnce(req)
	if err != nil {
		if permanentBeginError(err) {
			s.logf("ledger: CRITIQUE begin credit refusé définitivement, aucun envoi wallet %s: %v", req.describe(), err)
			return creditNeedsReview
		}
		s.logf("ledger: CRITIQUE begin credit en échec, aucun envoi wallet, reprise en arrière-plan %s: %v", req.describe(), err)
		s.scheduleBeginRetry(req)
		return creditDeferred
	}
	return s.afterBegin(req, entry, created)
}

func (s *creditSettler) beginOnce(req creditRequest) (*ledger.Entry, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.attemptTimeout)
	defer cancel()
	return s.led.Begin(ctx, ledger.NewEntry{
		GameID:         s.gameID,
		OperatorID:     ledger.OperatorID(req.OperatorID),
		PlayerID:       ledger.PlayerID(req.PlayerID),
		ExternalRef:    req.externalRef(),
		IdempotencyKey: req.idempotencyKey(),
		Type:           ledger.EntryCredit,
		AmountMinor:    req.AmountMinor,
	})
}

func (s *creditSettler) afterBegin(req creditRequest, e *ledger.Entry, created bool) creditOutcome {
	if e == nil {
		s.logf("ledger: CRITIQUE begin credit sans entrée retournée, aucun envoi wallet %s", req.describe())
		return creditNeedsReview
	}
	if !created {
		return s.existing(req, e)
	}

	cb := wallet.Callback{
		ID:             req.callbackID(),
		Type:           wallet.CallbackCredit,
		GameID:         s.gameID,
		TransactionID:  e.TransactionID,
		OperatorID:     req.OperatorID,
		PlayerID:       req.PlayerID,
		RoundID:        req.RoundID,
		BetID:          req.BetID,
		Amount:         req.AmountMinor,
		IdempotencyKey: string(req.idempotencyKey()),
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.attemptTimeout)
	err := s.queue.Enqueue(ctx, cb)
	cancel()
	if err != nil {
		s.logf("wallet: ALERTE enqueue CREDIT en échec txn=%s %s: %v — entrée Ledger PENDING, reprise par le Recovery", e.TransactionID, req.describe(), err)
		return creditRecoverable
	}
	return creditQueued
}

// existing traite une entrée préexistante : jamais de second envoi depuis ici.
func (s *creditSettler) existing(req creditRequest, e *ledger.Entry) creditOutcome {
	if e.Type != ledger.EntryCredit ||
		e.AmountMinor != req.AmountMinor ||
		string(e.OperatorID) != req.OperatorID ||
		string(e.PlayerID) != req.PlayerID {
		s.logf("ledger: CRITIQUE entrée existante incohérente txn=%s (type=%s amount=%d) pour %s — aucun envoi", e.TransactionID, e.Type, e.AmountMinor, req.describe())
		return creditNeedsReview
	}
	switch e.Status {
	case ledger.StatusFailed:
		s.logf("ledger: CRITIQUE gain déjà marqué FAILED txn=%s %s — réconciliation manuelle requise, aucun renvoi", e.TransactionID, req.describe())
		return creditNeedsReview
	case ledger.StatusPending:
		s.logf("ledger: gain déjà enregistré PENDING txn=%s %s — pris en charge par la Queue ou le Recovery, aucun second envoi", e.TransactionID, req.describe())
	}
	return creditAlreadyKnown
}

// scheduleBeginRetry relance Begin en arrière-plan jusqu'à succès (backoff plafonné). Une seule
// reprise par clé d'idempotence à la fois.
func (s *creditSettler) scheduleBeginRetry(req creditRequest) {
	key := req.idempotencyKey()
	s.mu.Lock()
	if _, busy := s.inflight[key]; busy {
		s.mu.Unlock()
		return
	}
	s.inflight[key] = struct{}{}
	s.mu.Unlock()

	s.spawn(func() {
		defer func() {
			s.mu.Lock()
			delete(s.inflight, key)
			s.mu.Unlock()
		}()

		delay := s.bgDelayMin
		for attempt := 1; attempt <= s.bgMaxAttempts; attempt++ {
			s.sleep(delay)
			entry, created, err := s.beginOnce(req)
			if err == nil {
				s.logf("ledger: gain enregistré après %d reprise(s) en arrière-plan %s", attempt, req.describe())
				s.afterBegin(req, entry, created)
				return
			}
			if permanentBeginError(err) {
				s.logf("ledger: CRITIQUE begin credit refusé définitivement en reprise %s: %v", req.describe(), err)
				return
			}
			delay *= 2
			if delay > s.bgDelayMax {
				delay = s.bgDelayMax
			}
		}
		s.logf("ledger: CRITIQUE gain NON enregistré après %d reprises, intervention manuelle requise %s", s.bgMaxAttempts, req.describe())
	})
}
