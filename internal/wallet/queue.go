package wallet

import (
	"context"
	"fmt"
	"log"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/tiidiio/223Gaming/223Crash/internal/provider/ledger"
)

// Queue persiste les callbacks CREDIT/ROLLBACK dans Mongo et les rejoue en cas
// d'échec, avec backoff exponentiel et dead-letter après 6 tentatives.
// DEBIT n'y passe pas : il doit être synchrone et bloquant (voir Client.Debit).
//
// Tranche 3bis : la Queue est désormais Ledger-aware. process() classe le résultat de
// Client.Send sur son Outcome (jamais sur err seul — un OutcomeAccepted peut porter un err
// non-nil si le corps de réponse est illisible, voir client.go) et appelle Confirm/Fail sur
// le Ledger en conséquence :
//   - OutcomeAccepted : Confirm (l'opération a eu lieu quoi qu'il arrive au parsing), retiré
//     de la queue.
//   - OutcomeRejected : Fail immédiat, dead-letter direct (pas de retry — un rejet métier
//     certain ne peut pas réussir au coup suivant, et un ROLLBACK/CREDIT rejeté est une
//     situation d'argent dû qui doit remonter en réconciliation manuelle sans délai).
//   - OutcomeUnknown : ni Confirm ni Fail — l'entrée Ledger reste PENDING, retry avec backoff
//     comme avant, dead-letter après maxAttempts si toujours indéterminé.
type Queue struct {
	col     *mongo.Collection
	deadCol *mongo.Collection
	client  *Client
	ledger  *ledger.Ledger
}

const (
	maxAttempts  = 6
	baseBackoff  = 500 * time.Millisecond
	maxBackoff   = 60 * time.Second
	leaseSeconds = 30
)

// NewQueue réutilise la connexion Mongo déjà ouverte (db) au lieu d'en ouvrir une nouvelle —
// aligné sur le pattern de internal/provider/ledger.New, corrige la 3e/4e connexion Mongo
// dédiée que ce package ouvrait jusqu'ici (coût réel en pool de connexions/goroutines sous
// Termux). led doit être le même *ledger.Ledger que celui utilisé par le gateway pour Begin(),
// sinon Confirm/Fail échoueront avec ErrEntryNotFound.
func NewQueue(ctx context.Context, db *mongo.Database, client *Client, led *ledger.Ledger) (*Queue, error) {
	col := db.Collection("wallet_callbacks")
	deadCol := db.Collection("wallet_callbacks_dead")

	_, err := col.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "status", Value: 1}, {Key: "dueAt", Value: 1}},
	})
	if err != nil {
		return nil, fmt.Errorf("wallet queue: create index: %w", err)
	}

	return &Queue{col: col, deadCol: deadCol, client: client, ledger: led}, nil
}

// Enqueue programme un callback pour envoi immédiat (dueAt = maintenant).
func (q *Queue) Enqueue(ctx context.Context, cb Callback) error {
	cb.Status = StatusPending
	cb.DueAt = time.Now()
	cb.CreatedAt = time.Now()
	cb.Attempts = 0

	_, err := q.col.InsertOne(ctx, cb)
	if err != nil {
		return fmt.Errorf("wallet queue: enqueue %s: %w", cb.Type, err)
	}
	return nil
}

// Run boucle en tâche de fond, à lancer dans une goroutine dédiée depuis main.go.
// Lease optimiste (FindOneAndUpdate) : le document reste en base tant que le
// traitement n'est pas confirmé réussi — même pattern que callback_queue.go du monorepo,
// pour ne jamais perdre un CREDIT si le process crashe pendant l'appel HTTP.
func (q *Queue) Run(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Second)
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
		var cb Callback
		now := time.Now()

		err := q.col.FindOneAndUpdate(
			ctx,
			bson.M{"status": StatusPending, "dueAt": bson.M{"$lte": now}},
			bson.M{"$set": bson.M{"dueAt": now.Add(leaseSeconds * time.Second)}},
			options.FindOneAndUpdate().SetReturnDocument(options.After),
		).Decode(&cb)

		if err != nil {
			if err != mongo.ErrNoDocuments {
				log.Printf("wallet queue: lease: %v", err)
			}
			return
		}

		q.process(ctx, cb)
	}
}

func (q *Queue) process(ctx context.Context, cb Callback) {
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
		q.resolveLedger(ctx, cb, true, &balance, "")
		if _, delErr := q.col.DeleteOne(ctx, bson.M{"_id": cb.ID}); delErr != nil {
			log.Printf("wallet queue: delete %s après succès: %v", cb.ID, delErr)
		}
		return

	case OutcomeRejected:
		// Rejet métier certain : retenter ne changera rien. Fail immédiat + dead-letter
		// direct (jamais un simple delete — c'est une situation d'argent dû qui doit
		// remonter en réconciliation manuelle, pas disparaître silencieusement).
		reason := ""
		if err != nil {
			reason = err.Error()
		}
		q.resolveLedger(ctx, cb, false, nil, reason)
		cb.LastError = reason
		if _, insErr := q.deadCol.InsertOne(ctx, cb); insErr != nil {
			log.Printf("wallet queue: insert dead-letter (rejet) %s: %v", cb.ID, insErr)
			return
		}
		if _, delErr := q.col.DeleteOne(ctx, bson.M{"_id": cb.ID}); delErr != nil {
			log.Printf("wallet queue: delete %s après rejet: %v", cb.ID, delErr)
		}
		return

	default: // OutcomeUnknown
		// Ni Confirm ni Fail : l'entrée Ledger reste PENDING, c'est le comportement voulu.
	}

	cb.Attempts++
	if err != nil {
		cb.LastError = err.Error()
	}

	if cb.Attempts >= maxAttempts {
		log.Printf("wallet queue: %s dead-letter après %d tentatives (résultat indéterminé) txn=%s: %v", cb.Type, cb.Attempts, cb.TransactionID, err)
		if _, insErr := q.deadCol.InsertOne(ctx, cb); insErr != nil {
			log.Printf("wallet queue: insert dead-letter %s: %v", cb.ID, insErr)
			return
		}
		if _, delErr := q.col.DeleteOne(ctx, bson.M{"_id": cb.ID}); delErr != nil {
			log.Printf("wallet queue: delete %s après dead-letter: %v", cb.ID, delErr)
		}
		return
	}

	backoff := baseBackoff * time.Duration(1<<uint(cb.Attempts))
	if backoff > maxBackoff {
		backoff = maxBackoff
	}

	_, updErr := q.col.UpdateOne(ctx,
		bson.M{"_id": cb.ID},
		bson.M{"$set": bson.M{
			"attempts":  cb.Attempts,
			"lastError": cb.LastError,
			"dueAt":     time.Now().Add(backoff),
		}},
	)
	if updErr != nil {
		log.Printf("wallet queue: update retry %s: %v", cb.ID, updErr)
	}
}

// resolveLedger appelle Confirm/Fail sur l'entrée Ledger corrélée à ce Callback via son
// TransactionID. Callback.TransactionID reste omitempty pour compat avec d'éventuels appelants
// qui n'ont pas (encore) ouvert d'entrée Ledger via Begin() avant l'enqueue — dans ce cas on
// journalise et on ne bloque pas le traitement du callback lui-même.
func (q *Queue) resolveLedger(ctx context.Context, cb Callback, accepted bool, balance *int64, reason string) {
	if cb.TransactionID == "" {
		log.Printf("wallet queue: %s sans TransactionID Ledger, résolution Confirm/Fail sautée (id=%s)", cb.Type, cb.ID)
		return
	}
	var err error
	if accepted {
		_, err = q.ledger.Confirm(ctx, cb.TransactionID, balance)
	} else {
		_, err = q.ledger.Fail(ctx, cb.TransactionID, reason)
	}
	if err != nil {
		log.Printf("wallet queue: résolution ledger txn=%s accepted=%v: %v", cb.TransactionID, accepted, err)
	}
}
