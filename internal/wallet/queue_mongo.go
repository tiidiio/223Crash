package wallet

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/tiidiio/223Gaming/internal/provider/ledger"
)

// NewQueue réutilise la connexion Mongo déjà ouverte (db) au lieu d'en ouvrir une nouvelle —
// aligné sur le pattern de internal/provider/ledger.New. led doit être le même *ledger.Ledger
// que celui utilisé par le gateway pour Begin(), sinon Confirm/Fail échoueront avec
// ErrEntryNotFound.
func NewQueue(ctx context.Context, db *mongo.Database, client *Client, led *ledger.Ledger) (*Queue, error) {
	col := db.Collection("wallet_callbacks")
	deadCol := db.Collection("wallet_callbacks_dead")

	_, err := col.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "status", Value: 1}, {Key: "dueAt", Value: 1}},
	})
	if err != nil {
		return nil, fmt.Errorf("wallet queue: create index: %w", err)
	}

	return &Queue{
		col:     col,
		deadCol: deadCol,
		client:  client,
		ledger:  led,
		st:      mongoCallbackStore{col: col, deadCol: deadCol},
	}, nil
}

// store retourne le stockage de la Queue ; repli sur MongoDB si la Queue a été construite
// sans passer par NewQueue (littéral de struct dans d'anciens tests d'intégration).
func (q *Queue) store() callbackStore {
	if q.st != nil {
		return q.st
	}
	return mongoCallbackStore{col: q.col, deadCol: q.deadCol}
}

// mongoCallbackStore est l'implémentation MongoDB de callbackStore. Volontairement fine :
// aucune logique métier. Elle n'est exécutée que par les tests d'intégration (MONGO_TEST_URI).
type mongoCallbackStore struct {
	col     *mongo.Collection
	deadCol *mongo.Collection
}

func (s mongoCallbackStore) insert(ctx context.Context, cb Callback) error {
	_, err := s.col.InsertOne(ctx, cb)
	return err
}

func (s mongoCallbackStore) lease(ctx context.Context, now time.Time, leaseFor time.Duration) (Callback, error) {
	var cb Callback
	err := s.col.FindOneAndUpdate(
		ctx,
		bson.M{"status": StatusPending, "dueAt": bson.M{"$lte": now}},
		bson.M{"$set": bson.M{"dueAt": now.Add(leaseFor)}},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&cb)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return Callback{}, errNoneDue
	}
	if err != nil {
		return Callback{}, err
	}
	return cb, nil
}

func (s mongoCallbackStore) remove(ctx context.Context, cb Callback) error {
	_, err := s.col.DeleteOne(ctx, bson.M{"_id": cb.ID})
	return err
}

func (s mongoCallbackStore) scheduleRetry(ctx context.Context, cb Callback, dueAt time.Time) error {
	_, err := s.col.UpdateOne(ctx,
		bson.M{"_id": cb.ID},
		bson.M{"$set": bson.M{
			"attempts":  cb.Attempts,
			"lastError": cb.LastError,
			"dueAt":     dueAt,
		}},
	)
	return err
}

// deadLetter est un upsert par _id : idempotent, donc un rejeu après un retrait échoué ne
// crée jamais de doublon dans la dead-letter.
func (s mongoCallbackStore) deadLetter(ctx context.Context, cb Callback) error {
	_, err := s.deadCol.ReplaceOne(ctx,
		bson.M{"_id": cb.ID},
		cb,
		options.Replace().SetUpsert(true),
	)
	return err
}
