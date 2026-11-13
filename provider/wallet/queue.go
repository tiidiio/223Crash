package wallet

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/tiidiio/223Crash/provider/ledger"
)

// Queue persiste les callbacks CREDIT/ROLLBACK dans Mongo et les rejoue en cas
// d'échec, avec backoff exponentiel et dead-letter après maxAttempts tentatives.
// DEBIT n'y passe pas : il doit être synchrone et bloquant (voir Client.Debit).
//
// La Queue est Ledger-aware. process() classe le résultat de Client.Send sur son Outcome
// (jamais sur err seul — un OutcomeAccepted peut porter une erreur ErrBalanceUnavailable si
// le corps de réponse est illisible, voir client.go) et résout le Ledger en conséquence :
//   - OutcomeAccepted : Confirm (l'opération a eu lieu quoi qu'il arrive au parsing ; le solde
//     n'est transmis que s'il est authentique — jamais un faux 0). Le callback n'est retiré de
//     la queue QUE si la résolution Ledger a réussi.
//   - OutcomeRejected : Fail, puis dead-letter (pas de retry HTTP utile — un rejet métier
//     certain ne peut pas réussir au coup suivant, et un ROLLBACK/CREDIT rejeté est une
//     situation d'argent dû qui doit remonter en réconciliation manuelle).
//   - OutcomeUnknown : ni Confirm ni Fail — l'entrée Ledger reste PENDING, retry avec backoff,
//     dead-letter après maxAttempts si toujours indéterminé.
//
// Garde pré-envoi (guard) : AUCUN appel wallet n'est émis sans entrée Ledger durable, relue et
// cohérente avec le callback. Voir guard().
//
// Échec de résolution Ledger (Confirm/Fail renvoie une erreur) — le callback est la dernière
// trace de reprise d'une opération déjà tranchée côté opérateur, il n'est donc jamais supprimé
// silencieusement :
//   - erreur transitoire (infra) : le callback reste en queue, retry avec backoff. Le retry
//     rejoue Send avec le MÊME ID et la MÊME IdempotencyKey : l'opérateur doit répondre de façon
//     idempotente (pas de double crédit) ; Confirm/Fail sont eux-mêmes idempotents. Aucune
//     transaction atomique Ledger+opérateur n'est supposée.
//   - erreur définitive (ErrStateConflict, ErrEntryNotFound) : retenter ne changera rien ; le
//     callback est écrit en dead-letter (trace durable, upsert par _id donc sans doublon) et
//     n'est retiré de la queue qu'après cette écriture réussie.
//   - dans tous les cas, si l'écriture dead-letter échoue, le callback reste en queue.
type Queue struct {
	// col et deadCol sont conservés pour compatibilité (tests d'intégration, outillage).
	col     *mongo.Collection
	deadCol *mongo.Collection

	client sender
	ledger ledgerResolver
	st     callbackStore
}

// sender est la partie de *Client dont la Queue a besoin.
type sender interface {
	Send(ctx context.Context, cb Callback) (int64, Outcome, error)
}

// ledgerResolver est la partie de *ledger.Ledger dont la Queue a besoin.
type ledgerResolver interface {
	Confirm(ctx context.Context, txnID ledger.TransactionID, operatorBalanceAfterMinor *int64) (*ledger.Entry, error)
	Fail(ctx context.Context, txnID ledger.TransactionID, reason string) (*ledger.Entry, error)
}

// entryReader est la capacité de lecture que la garde pré-envoi exige du resolver. *ledger.Ledger
// l'implémente. Un resolver qui ne l'implémente pas (double de test minimal) n'est pas soumis à la
// relecture ; la règle « TransactionID obligatoire » s'applique dans tous les cas.
type entryReader interface {
	GetByTransactionID(ctx context.Context, txnID ledger.TransactionID) (*ledger.Entry, error)
}

// errNoneDue : aucun callback échu à traiter (équivalent applicatif de mongo.ErrNoDocuments).
var errNoneDue = errors.New("wallet queue: aucun callback échu")

// callbackStore est le stockage durable de la Queue. Contrat :
//   - lease renvoie le prochain callback PENDING échu et repousse son dueAt de leaseFor
//     (lease optimiste, le document reste en base) ; errNoneDue s'il n'y en a pas ;
//   - deadLetter est idempotent (upsert par ID) : l'appeler deux fois ne crée pas de doublon ;
//   - remove supprime le callback de la queue (jamais la dead-letter).
type callbackStore interface {
	insert(ctx context.Context, cb Callback) error
	lease(ctx context.Context, now time.Time, leaseFor time.Duration) (Callback, error)
	remove(ctx context.Context, cb Callback) error
	scheduleRetry(ctx context.Context, cb Callback, dueAt time.Time) error
	deadLetter(ctx context.Context, cb Callback) error
}

const (
	maxAttempts  = 6
	baseBackoff  = 500 * time.Millisecond
	maxBackoff   = 60 * time.Second
	leaseSeconds = 30
)

// pollInterval est la période de la boucle Run (variable pour les tests uniquement).
var pollInterval = time.Second

// backoffFor calcule le délai avant la prochaine tentative : exponentiel, plafonné à maxBackoff.
func backoffFor(attempts int) time.Duration {
	if attempts >= 16 { // évite tout dépassement de 1<<attempts ; très au-delà du plafond
		return maxBackoff
	}
	d := baseBackoff * time.Duration(1<<uint(attempts))
	if d > maxBackoff {
		return maxBackoff
	}
	return d
}

// Enqueue programme un callback pour envoi immédiat (dueAt = maintenant).
func (q *Queue) Enqueue(ctx context.Context, cb Callback) error {
	cb.Status = StatusPending
	cb.DueAt = time.Now()
	cb.CreatedAt = time.Now()
	cb.Attempts = 0

	if err := q.store().insert(ctx, cb); err != nil {
		return fmt.Errorf("wallet queue: enqueue %s: %w", cb.Type, err)
	}
	return nil
}

// Run boucle en tâche de fond, à lancer dans une goroutine dédiée depuis main.go.
// Lease optimiste : le document reste en base tant que le traitement n'est pas confirmé
// réussi — pour ne jamais perdre un CREDIT si le process crashe pendant l'appel HTTP.
func (q *Queue) Run(ctx context.Context) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			q.drainDue(ctx)
		}
	}
}

func (q *Queue) drainDue(ctx context.Context) {
	for {
		cb, err := q.store().lease(ctx, time.Now(), leaseSeconds*time.Second)
		if err != nil {
			if !errors.Is(err, errNoneDue) {
				log.Printf("wallet queue: lease: %v", err)
			}
			return
		}

		q.process(ctx, cb)
	}
}

// guard est la garde pré-envoi. Elle renvoie true si l'envoi peut avoir lieu ; sinon elle a déjà
// traité le callback (retrait, retry ou dead-letter) et process doit s'arrêter.
//
// Règles :
//   - seuls CREDIT et ROLLBACK transitent par la Queue (DEBIT est synchrone) ;
//   - TransactionID obligatoire : aucun mouvement d'argent sans entrée Ledger durable ;
//   - l'entrée doit exister (sinon dead-letter) ; une erreur de lecture est transitoire (retry) ;
//   - entrée CONFIRMED : jamais de renvoi, le callback est retiré ;
//   - entrée FAILED : jamais d'envoi, dead-letter pour réconciliation ;
//   - champs du callback (type, opérateur, joueur, montant, clé d'idempotence) identiques à
//     l'entrée, sinon dead-letter ;
//   - ROLLBACK : ParentTransactionID obligatoire ; le parent doit être un DEBIT du même opérateur
//     et joueur, de même montant, et CONFIRMED. Un parent PENDING (issue du débit inconnue) ne
//     justifie jamais un remboursement : retry puis dead-letter, visible en réconciliation.
//
// Aucune de ces branches n'appelle Fail : un blocage de garde n'est pas un rejet certain du wallet.
func (q *Queue) guard(ctx context.Context, cb Callback) bool {
	if cb.Type != CallbackCredit && cb.Type != CallbackRollback {
		q.blockCallback(ctx, cb, fmt.Sprintf("type %q non autorisé en queue", cb.Type))
		return false
	}
	if cb.TransactionID == "" {
		q.blockCallback(ctx, cb, "TransactionID Ledger absent")
		return false
	}

	reader, ok := q.ledger.(entryReader)
	if !ok {
		return true
	}

	gctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	entry, err := reader.GetByTransactionID(gctx, cb.TransactionID)
	if errors.Is(err, ledger.ErrEntryNotFound) {
		q.blockCallback(ctx, cb, "entrée Ledger introuvable")
		return false
	}
	if err != nil {
		q.retryOrDeadLetter(ctx, cb, fmt.Errorf("garde: lecture ledger: %w", err))
		return false
	}

	switch entry.Status {
	case ledger.StatusConfirmed:
		log.Printf("wallet queue: %s déjà CONFIRMED, aucun renvoi txn=%s", cb.Type, cb.TransactionID)
		q.remove(ctx, cb, "entrée déjà CONFIRMED")
		return false
	case ledger.StatusFailed:
		q.blockCallback(ctx, cb, "entrée Ledger FAILED")
		return false
	}

	if string(entry.Type) != string(cb.Type) ||
		string(entry.OperatorID) != cb.OperatorID ||
		string(entry.PlayerID) != cb.PlayerID ||
		entry.AmountMinor != cb.Amount ||
		string(entry.IdempotencyKey) != cb.IdempotencyKey {
		q.blockCallback(ctx, cb, fmt.Sprintf("callback incohérent avec l'entrée Ledger (type=%s amount=%d)", entry.Type, entry.AmountMinor))
		return false
	}

	if cb.Type != CallbackRollback {
		return true
	}

	if entry.ParentTransactionID == "" {
		q.blockCallback(ctx, cb, "ROLLBACK sans ParentTransactionID")
		return false
	}
	parent, err := reader.GetByTransactionID(gctx, entry.ParentTransactionID)
	if errors.Is(err, ledger.ErrEntryNotFound) {
		q.blockCallback(ctx, cb, "débit parent introuvable")
		return false
	}
	if err != nil {
		q.retryOrDeadLetter(ctx, cb, fmt.Errorf("garde: lecture parent: %w", err))
		return false
	}
	if parent.Type != ledger.EntryDebit ||
		parent.OperatorID != entry.OperatorID ||
		parent.PlayerID != entry.PlayerID ||
		parent.AmountMinor != entry.AmountMinor {
		q.blockCallback(ctx, cb, "débit parent incohérent")
		return false
	}
	if parent.Status != ledger.StatusConfirmed {
		q.retryOrDeadLetter(ctx, cb, fmt.Errorf("débit parent %s non CONFIRMED (statut=%s), aucun remboursement sur supposition", parent.TransactionID, parent.Status))
		return false
	}
	return true
}

// blockCallback écrit la trace durable (dead-letter) d'un callback refusé par la garde, sans
// l'envoyer et sans toucher à l'entrée Ledger.
func (q *Queue) blockCallback(ctx context.Context, cb Callback, reason string) {
	log.Printf("wallet queue: CRITIQUE %s bloqué avant envoi id=%s txn=%s: %s", cb.Type, cb.ID, cb.TransactionID, reason)
	cb.LastError = "garde pré-envoi: " + reason
	q.deadLetterAndRemove(ctx, cb, "garde pré-envoi")
}

func (q *Queue) process(ctx context.Context, cb Callback) {
	if !q.guard(ctx, cb) {
		return
	}

	sendCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	balance, outcome, err := q.client.Send(sendCtx, cb)

	switch outcome {
	case OutcomeAccepted:
		// L'opération a eu lieu côté opérateur, quoi qu'il arrive au parsing de la réponse
		// (err peut être non-nil ici — voir client.go — mais l'Outcome fait foi, pas err).
		if err != nil {
			log.Printf("wallet queue: %s accepté mais réponse dégradée txn=%s: %v", cb.Type, cb.TransactionID, err)
		}
		if resErr := q.resolveLedger(ctx, cb, true, BalanceOrNil(balance, err), ""); resErr != nil {
			q.onResolveFailure(ctx, cb, fmt.Errorf("opérateur a accepté mais résolution ledger en échec: %w", resErr), resErr)
			return
		}
		q.remove(ctx, cb, "après succès")
		return

	case OutcomeRejected:
		// Rejet métier certain : retenter ne changera rien. Fail + dead-letter direct (jamais
		// un simple delete — c'est une situation d'argent dû qui doit remonter en
		// réconciliation manuelle, pas disparaître silencieusement).
		reason := ""
		if err != nil {
			reason = err.Error()
		}
		if resErr := q.resolveLedger(ctx, cb, false, nil, reason); resErr != nil {
			q.onResolveFailure(ctx, cb, fmt.Errorf("%s; résolution ledger en échec: %w", reason, resErr), resErr)
			return
		}
		cb.LastError = reason
		q.deadLetterAndRemove(ctx, cb, "rejet")
		return

	default: // OutcomeUnknown
		// Ni Confirm ni Fail : l'entrée Ledger reste PENDING, c'est le comportement voulu.
		q.retryOrDeadLetter(ctx, cb, err)
	}
}

// onResolveFailure traite l'échec de Confirm/Fail. cause est ce qui sera tracé dans LastError ;
// resErr (l'erreur Ledger brute) décide de la stratégie.
func (q *Queue) onResolveFailure(ctx context.Context, cb Callback, cause, resErr error) {
	if errors.Is(resErr, ledger.ErrStateConflict) || errors.Is(resErr, ledger.ErrEntryNotFound) {
		// Incohérence définitive : un retry ne la corrigera pas. Trace durable en dead-letter
		// pour réconciliation manuelle, puis retrait de la queue.
		log.Printf("wallet queue: %s résolution ledger définitivement impossible txn=%s: %v", cb.Type, cb.TransactionID, resErr)
		cb.LastError = cause.Error()
		q.deadLetterAndRemove(ctx, cb, "incohérence ledger")
		return
	}
	// Erreur transitoire : le callback reste en queue et sera rejoué (même ID, même clé).
	log.Printf("wallet queue: %s résolution ledger en échec, callback conservé pour retry txn=%s: %v", cb.Type, cb.TransactionID, resErr)
	q.retryOrDeadLetter(ctx, cb, cause)
}

// retryOrDeadLetter incrémente le compteur de tentatives puis reprogramme le callback avec
// backoff, ou l'écrit en dead-letter une fois maxAttempts atteint.
func (q *Queue) retryOrDeadLetter(ctx context.Context, cb Callback, cause error) {
	cb.Attempts++
	if cause != nil {
		cb.LastError = cause.Error()
	}

	if cb.Attempts >= maxAttempts {
		log.Printf("wallet queue: %s dead-letter après %d tentatives txn=%s: %v", cb.Type, cb.Attempts, cb.TransactionID, cause)
		q.deadLetterAndRemove(ctx, cb, "tentatives épuisées")
		return
	}

	dueAt := time.Now().Add(backoffFor(int(cb.Attempts)))
	if err := q.store().scheduleRetry(ctx, cb, dueAt); err != nil {
		log.Printf("wallet queue: update retry %s: %v", cb.ID, err)
	}
}

// deadLetterAndRemove écrit la trace durable puis retire le callback de la queue. Le retrait
// n'a lieu QUE si l'écriture dead-letter a réussi : sinon le callback reste en queue (c'est
// alors la seule trace). Si le retrait échoue après une dead-letter réussie, le prochain
// passage ré-écrit la même dead-letter (upsert par ID) : pas de doublon.
func (q *Queue) deadLetterAndRemove(ctx context.Context, cb Callback, why string) {
	if err := q.store().deadLetter(ctx, cb); err != nil {
		log.Printf("wallet queue: insert dead-letter (%s) %s: %v", why, cb.ID, err)
		return
	}
	q.remove(ctx, cb, "après dead-letter ("+why+")")
}

func (q *Queue) remove(ctx context.Context, cb Callback, when string) {
	if err := q.store().remove(ctx, cb); err != nil {
		log.Printf("wallet queue: delete %s %s: %v", cb.ID, when, err)
	}
}

// resolveLedger appelle Confirm/Fail sur l'entrée Ledger corrélée à ce Callback via son
// TransactionID et renvoie l'erreur éventuelle — l'appelant ne doit JAMAIS supprimer le
// callback si elle est non-nil. La garde (guard) garantit en amont que TransactionID est
// renseigné ; le test ci-dessous reste une défense en profondeur.
func (q *Queue) resolveLedger(ctx context.Context, cb Callback, accepted bool, balance *int64, reason string) error {
	if cb.TransactionID == "" {
		return fmt.Errorf("callback %s sans TransactionID Ledger: %w", cb.ID, ledger.ErrEntryNotFound)
	}
	var err error
	if accepted {
		_, err = q.ledger.Confirm(ctx, cb.TransactionID, balance)
	} else {
		_, err = q.ledger.Fail(ctx, cb.TransactionID, reason)
	}
	if err != nil {
		return fmt.Errorf("txn=%s accepted=%v: %w", cb.TransactionID, accepted, err)
	}
	return nil
}
