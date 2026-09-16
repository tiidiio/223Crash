package wallet

import (
	"context"
	"fmt"
	"log"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Queue persiste les callbacks CREDIT/ROLLBACK dans Mongo et les rejoue en cas
// d'échec, avec backoff exponentiel et dead-letter après 6 tentatives.
// DEBIT n'y passe pas : il doit être synchrone et bloquant (voir Client.Debit).
//
// NOTE Tranche 3 (scope volontairement limité) : cette Queue n'est pas encore Ledger-aware.
// process() ignore l'Outcome renvoyé par Send — le comportement de retry/dead-letter reste
// identique à avant. Rendre la Queue Ledger-aware (classification Accepted/Rejected/Unknown →
// Confirm/Fail/PENDING, y compris sur dead-letter) est le sujet dédié de la Tranche 3bis —
// voir /areas/223gaming-provider-core pour le contrat exact attendu.
type Queue struct {
	col     *mongo.Collection
	deadCol *mongo.Collection
	client  *Client
}

const (
	maxAttempts  = 6
	baseBackoff  = 500 * time.Millisecond
	maxBackoff   = 60 * time.Second
	leaseSeconds = 30
)

func NewQueue(ctx context.Context, mongoURI, mongoDB string, client *Client) (*Queue, error) {
	mc, err := mongo.Connect(options.Client().ApplyURI(mongoURI))
	if err != nil {
		return nil, fmt.Errorf("wallet queue: connect mongo: %w", err)
	}
	if err := mc.Ping(ctx, nil); err != nil {
		return nil, fmt.Errorf("wallet queue: ping mongo: %w", err)
	}

	db := mc.Database(mongoDB)
	col := db.Collection("wallet_callbacks")
	deadCol := db.Collection("wallet_callbacks_dead")

	_, err = col.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "status", Value: 1}, {Key: "dueAt", Value: 1}},
	})
	if err != nil {
		return nil, fmt.Errorf("wallet queue: create index: %w", err)
	}

	return &Queue{col: col, deadCol: deadCol, client: client}, nil
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

	// Outcome ignoré pour l'instant — voir note de package ci-dessus (Tranche 3bis).
	_, _, err := q.client.Send(sendCtx, cb)
	if err == nil {
		if _, delErr := q.col.DeleteOne(ctx, bson.M{"_id": cb.ID}); delErr != nil {
			log.Printf("wallet queue: delete %s après succès: %v", cb.ID, delErr)
		}
		return
	}

	cb.Attempts++
	cb.LastError = err.Error()

	if cb.Attempts >= maxAttempts {
		log.Printf("wallet queue: %s dead-letter après %d tentatives: %v", cb.ID, cb.Attempts, err)
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
