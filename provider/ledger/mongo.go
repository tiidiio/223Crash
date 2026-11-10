package ledger

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// New connecte le Ledger à la base Mongo déjà ouverte (réutilise une connexion existante —
// ne pas ouvrir une nouvelle connexion mongo.Connect par composant) et garantit les index
// nécessaires, dont l'index unique composé (operatorId, idempotencyKey) qui fait office de
// protection anti-doublon au niveau base de données, pas seulement applicatif.
//
// L'ancien index global éventuel est conservé : aucun index n'est supprimé
// ou modifié automatiquement. Tant qu'il existe, son unicité reste globale.
func New(ctx context.Context, db *mongo.Database) (*Ledger, error) {
	col := db.Collection(collectionName)
	l := &Ledger{
		col: col,
		st:  mongoStore{col: col},
	}
	if err := ensureIndexes(ctx, l.st); err != nil {
		return nil, err
	}
	return l, nil
}

// mongoStore est l'implémentation MongoDB de store. Volontairement fine : aucune logique
// métier, uniquement la traduction des erreurs du driver vers les sentinelles de store.
type mongoStore struct {
	col *mongo.Collection
}

// isOne accepte toutes les représentations numériques BSON de la valeur 1 (int32, int64,
// double) : un index créé par un autre outil peut utiliser un autre type que int32.
func isOne(v any) bool {
	switch n := v.(type) {
	case int32:
		return n == 1
	case int64:
		return n == 1
	case float64:
		return n == 1
	default:
		return false
	}
}

func (s mongoStore) ensureIndex(ctx context.Context, d indexDef) error {
	keys := make(bson.D, 0, len(d.fields))
	for _, field := range d.fields {
		keys = append(keys, bson.E{Key: field, Value: 1})
	}

	opts := options.Index()
	if d.unique {
		opts = opts.SetUnique(true)
	}
	if d.name != "" {
		opts = opts.SetName(d.name)
	}

	_, err := s.col.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    keys,
		Options: opts,
	})
	return err
}

func hasIdempotencyKeyPattern(raw bson.Raw) bool {
	if len(raw) == 0 {
		return false
	}

	value := raw.Lookup("keyPattern")
	if value.Type == 0 {
		return false
	}

	pattern, ok := value.DocumentOK()
	if !ok {
		return false
	}

	var fields bson.D
	if err := bson.Unmarshal(pattern, &fields); err != nil {
		return false
	}
	if len(fields) != 2 {
		return false
	}

	seenOperator := false
	seenKey := false
	for _, field := range fields {
		switch field.Key {
		case "operatorId":
			seenOperator = isOne(field.Value)
		case "idempotencyKey":
			seenKey = isOne(field.Value)
		default:
			return false
		}
	}
	return seenOperator && seenKey
}

// isIdempotencyDuplicate ne classe errDuplicate que si MongoDB identifie
// précisément l'index composé d'idempotence. Une collision transactionId,
// ou tout autre DuplicateKey, reste une erreur distincte.
//
// mongo.WriteException est un type VALEUR dans le driver v2 : errors.As doit cibler
// mongo.WriteException, pas *mongo.WriteException (qui ne correspond jamais).
func isIdempotencyDuplicate(err error) bool {
	if err == nil || !mongo.IsDuplicateKeyError(err) {
		return false
	}

	var writeErr mongo.WriteException
	if !errors.As(err, &writeErr) {
		return false
	}

	for _, item := range writeErr.WriteErrors {
		if item.Code != 11000 && item.Code != 11001 {
			continue
		}

		if hasIdempotencyKeyPattern(item.Raw) ||
			hasIdempotencyKeyPattern(item.Details) {
			return true
		}

		// Certaines versions/configurations MongoDB ne fournissent pas
		// keyPattern dans Raw/Details. Dans ce cas, le nom explicite de
		// l'index est le seul fallback accepté.
		if strings.Contains(item.Message, idempotencyIndexName) {
			return true
		}
	}
	return false
}

func (s mongoStore) insert(ctx context.Context, e Entry) error {
	_, err := s.col.InsertOne(ctx, e)
	if err == nil {
		return nil
	}
	if isIdempotencyDuplicate(err) {
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

func (s mongoStore) byIdempotencyKey(
	ctx context.Context,
	operatorID OperatorID,
	key IdempotencyKey,
) (*Entry, error) {
	return s.findOne(ctx, bson.M{
		"operatorId":     operatorID,
		"idempotencyKey": key,
	})
}

func (s mongoStore) byTransactionID(ctx context.Context, id TransactionID) (*Entry, error) {
	return s.findOne(ctx, bson.M{"transactionId": id})
}

func (s mongoStore) resolvePending(
	ctx context.Context,
	id TransactionID,
	t transitionSpec,
) (*Entry, error) {
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
		bson.M{
			"transactionId": id,
			"status":        StatusPending,
		},
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
