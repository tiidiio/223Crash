package ledger

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// New connecte le Ledger à la base Mongo déjà ouverte (réutilise une connexion existante —
// ne pas ouvrir une nouvelle connexion mongo.Connect par composant) et garantit les index
// nécessaires, dont l'index unique sur idempotencyKey qui fait office de protection anti-doublon
// au niveau base de données, pas seulement applicatif.
func New(ctx context.Context, db *mongo.Database) (*Ledger, error) {
	col := db.Collection(collectionName)
	l := &Ledger{col: col, st: mongoStore{col: col}}
	if err := ensureIndexes(ctx, l.st); err != nil {
		return nil, err
	}
	return l, nil
}

// mongoStore est l'implémentation MongoDB de store. Volontairement fine : aucune logique
// métier, uniquement la traduction des erreurs du driver vers les sentinelles de store. Ce
// code n'est exécuté que par les tests d'intégration (MONGO_TEST_URI).
type mongoStore struct {
	col *mongo.Collection
}

func (s mongoStore) ensureIndex(ctx context.Context, d indexDef) error {
	keys := make(bson.D, 0, len(d.fields))
	for _, f := range d.fields {
		keys = append(keys, bson.E{Key: f, Value: 1})
	}
	opts := options.Index()
	if d.unique {
		opts = opts.SetUnique(true)
	}
	_, err := s.col.Indexes().CreateOne(ctx, mongo.IndexModel{Keys: keys, Options: opts})
	return err
}

func (s mongoStore) insert(ctx context.Context, e Entry) error {
	_, err := s.col.InsertOne(ctx, e)
	if err != nil && mongo.IsDuplicateKeyError(err) {
		return errDuplicate
	}
	return err
}

func (s mongoStore) findOne(ctx context.Context, filter bson.M) (*Entry, error) {
	var entry Entry
	err := s.col.FindOne(ctx, filter).Decode(&entry)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	return &entry, nil
}

func (s mongoStore) byIdempotencyKey(ctx context.Context, key IdempotencyKey) (*Entry, error) {
	return s.findOne(ctx, bson.M{"idempotencyKey": key})
}

func (s mongoStore) byTransactionID(ctx context.Context, id TransactionID) (*Entry, error) {
	return s.findOne(ctx, bson.M{"transactionId": id})
}

func (s mongoStore) resolvePending(ctx context.Context, id TransactionID, t transitionSpec) (*Entry, error) {
	set := bson.M{
		"status":    t.to,
		"updatedAt": t.at,
	}
	switch t.to {
	case StatusConfirmed:
		set["confirmedAt"] = t.at
		if t.balance != nil {
			set["operatorBalanceAfterMinor"] = *t.balance
		}
	case StatusFailed:
		set["failedAt"] = t.at
		set["failureReason"] = t.reason
	}

	var entry Entry
	err := s.col.FindOneAndUpdate(
		ctx,
		bson.M{"transactionId": id, "status": StatusPending},
		bson.M{"$set": set},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&entry)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	return &entry, nil
}

func (s mongoStore) pendingBefore(ctx context.Context, before time.Time) ([]Entry, error) {
	cur, err := s.col.Find(ctx, bson.M{
		"status":    StatusPending,
		"createdAt": bson.M{"$lt": before},
	})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)

	var entries []Entry
	if err := cur.All(ctx, &entries); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return entries, nil
}
