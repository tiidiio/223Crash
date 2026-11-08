package betstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// CollectionName est la collection des paris durables. Aucune collection existante n'est
// modifiée ou supprimée : celle-ci est créée à la demande.
const CollectionName = "crash_bets"

// MongoStore est l'implémentation MongoDB de Store. Aucune règle métier n'y est dupliquée : les
// garanties viennent des index uniques et des mises à jour conditionnelles à l'état.
type MongoStore struct {
	col *mongo.Collection
}

var _ Store = (*MongoStore)(nil)

func NewMongo(ctx context.Context, db *mongo.Database) (*MongoStore, error) {
	return NewMongoCollection(ctx, db.Collection(CollectionName))
}

// NewMongoCollection permet d'injecter une collection dédiée (tests d'intégration).
func NewMongoCollection(ctx context.Context, col *mongo.Collection) (*MongoStore, error) {
	s := &MongoStore{col: col}
	if err := s.ensureIndexes(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *MongoStore) ensureIndexes(ctx context.Context) error {
	defs := []struct {
		name   string
		keys   bson.D
		unique bool
	}{
		{"debitTransactionId_1", bson.D{{Key: "debitTransactionId", Value: 1}}, true},
		{"operatorId_1_playerId_1_clientBetId_1", bson.D{{Key: "operatorId", Value: 1}, {Key: "playerId", Value: 1}, {Key: "clientBetId", Value: 1}}, true},
		{"state_1_updatedAt_1", bson.D{{Key: "state", Value: 1}, {Key: "updatedAt", Value: 1}}, false},
		{"operatorId_1_playerId_1_state_1", bson.D{{Key: "operatorId", Value: 1}, {Key: "playerId", Value: 1}, {Key: "state", Value: 1}}, false},
	}
	for _, d := range defs {
		opts := options.Index().SetName(d.name)
		if d.unique {
			opts = opts.SetUnique(true)
		}
		if _, err := s.col.Indexes().CreateOne(ctx, mongo.IndexModel{Keys: d.keys, Options: opts}); err != nil {
			return fmt.Errorf("betstore: création index %s: %w", d.name, err)
		}
	}
	return nil
}

func (s *MongoStore) find(ctx context.Context, operatorID, debitTxn string) (*Bet, error) {
	var b Bet
	err := s.col.FindOne(ctx, bson.M{"operatorId": operatorID, "debitTransactionId": debitTxn}).Decode(&b)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("betstore: lecture: %w", err)
	}
	return &b, nil
}

// transition applique set (+ version++) si et seulement si le document est dans l'état from et
// satisfait extra. matched=false signifie qu'aucun document n'a correspondu (absent, autre état
// ou condition non satisfaite) : l'appelant relit pour trancher.
func (s *MongoStore) transition(ctx context.Context, operatorID, debitTxn string, from State, extra, set bson.M) (*Bet, bool, error) {
	filter := bson.M{"operatorId": operatorID, "debitTransactionId": debitTxn, "state": from}
	for k, v := range extra {
		filter[k] = v
	}
	var b Bet
	err := s.col.FindOneAndUpdate(
		ctx, filter,
		bson.M{"$set": set, "$inc": bson.M{"version": 1}},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&b)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("betstore: transition: %w", err)
	}
	return &b, true, nil
}

func (s *MongoStore) Insert(ctx context.Context, in Bet) (*Bet, bool, error) {
	if err := in.validate(); err != nil {
		return nil, false, err
	}
	now := time.Now()
	in.State = StatePending
	in.CreatedAt, in.UpdatedAt = now, now
	in.Version = 1
	in.CreditTransactionID, in.RollbackTransactionID = "", ""
	in.CashoutMultiplier, in.PayoutMinor, in.CashoutAt, in.ReconcileReason = 0, 0, time.Time{}, ""

	_, err := s.col.InsertOne(ctx, in)
	if err == nil {
		return &in, true, nil
	}
	if !mongo.IsDuplicateKeyError(err) {
		return nil, false, fmt.Errorf("betstore: insertion: %w", err)
	}
	existing, getErr := s.find(ctx, in.OperatorID, in.DebitTransactionID)
	if errors.Is(getErr, ErrNotFound) {
		return nil, false, fmt.Errorf("betstore: conflit d'unicité (clientBetId déjà associé à une autre txn) pour %s", in.DebitTransactionID)
	}
	if getErr != nil {
		return nil, false, getErr
	}
	return existing, false, nil
}

func (s *MongoStore) Get(ctx context.Context, operatorID, debitTxn string) (*Bet, error) {
	if err := checkRef(operatorID, debitTxn); err != nil {
		return nil, err
	}
	return s.find(ctx, operatorID, debitTxn)
}

func (s *MongoStore) MarkCashedOut(ctx context.Context, operatorID, debitTxn string, multiplier float64, payoutMinor int64, at time.Time) (*Bet, error) {
	if err := checkRef(operatorID, debitTxn); err != nil {
		return nil, err
	}
	if err := checkCashout(multiplier, payoutMinor); err != nil {
		return nil, err
	}
	b, ok, err := s.transition(ctx, operatorID, debitTxn, StatePending, nil, bson.M{
		"state":             StateCashedOut,
		"cashoutMultiplier": multiplier,
		"payoutMinor":       payoutMinor,
		"cashoutAt":         at,
		"updatedAt":         time.Now(),
	})
	if err != nil || ok {
		return b, err
	}
	cur, err := s.find(ctx, operatorID, debitTxn)
	if err != nil {
		return nil, err
	}
	if cur.State == StateCashedOut && cur.PayoutMinor == payoutMinor && cur.CashoutMultiplier == multiplier {
		return cur, nil
	}
	return nil, conflict(debitTxn, cur.State, StateCashedOut)
}

func (s *MongoStore) MarkLost(ctx context.Context, operatorID, debitTxn string, at time.Time) (*Bet, error) {
	if err := checkRef(operatorID, debitTxn); err != nil {
		return nil, err
	}
	b, ok, err := s.transition(ctx, operatorID, debitTxn, StatePending, nil, bson.M{
		"state":     StateLost,
		"cashoutAt": at,
		"updatedAt": time.Now(),
	})
	if err != nil || ok {
		return b, err
	}
	cur, err := s.find(ctx, operatorID, debitTxn)
	if err != nil {
		return nil, err
	}
	if cur.State == StateLost {
		return cur, nil
	}
	return nil, conflict(debitTxn, cur.State, StateLost)
}

func (s *MongoStore) MarkReconciling(ctx context.Context, operatorID, debitTxn, reason string) (*Bet, error) {
	if err := checkRef(operatorID, debitTxn); err != nil {
		return nil, err
	}
	b, ok, err := s.transition(ctx, operatorID, debitTxn, StatePending, nil, bson.M{
		"state":           StateReconciling,
		"reconcileReason": reason,
		"updatedAt":       time.Now(),
	})
	if err != nil || ok {
		return b, err
	}
	cur, err := s.find(ctx, operatorID, debitTxn)
	if err != nil {
		return nil, err
	}
	if cur.State == StateReconciling {
		return cur, nil
	}
	return nil, conflict(debitTxn, cur.State, StateReconciling)
}

func (s *MongoStore) SetCreditRef(ctx context.Context, operatorID, debitTxn, creditTxn string) (*Bet, error) {
	if err := checkRef(operatorID, debitTxn); err != nil {
		return nil, err
	}
	if creditTxn == "" {
		return nil, fmt.Errorf("%w: creditTransactionId requis", ErrInvalid)
	}
	b, ok, err := s.transition(ctx, operatorID, debitTxn, StateCashedOut,
		bson.M{"creditTransactionId": bson.M{"$exists": false}},
		bson.M{"creditTransactionId": creditTxn, "updatedAt": time.Now()},
	)
	if err != nil || ok {
		return b, err
	}
	cur, err := s.find(ctx, operatorID, debitTxn)
	if err != nil {
		return nil, err
	}
	if cur.State == StateCashedOut && cur.CreditTransactionID == creditTxn {
		return cur, nil
	}
	return nil, conflict(debitTxn, cur.State, StateCashedOut)
}

func (s *MongoStore) ReconcileOrphans(ctx context.Context, instanceID string, olderThan time.Time, reason string) (int64, error) {
	if instanceID == "" {
		return 0, fmt.Errorf("%w: instanceId requis", ErrInvalid)
	}
	res, err := s.col.UpdateMany(ctx,
		bson.M{
			"state":      StatePending,
			"instanceId": bson.M{"$ne": instanceID},
			"updatedAt":  bson.M{"$lt": olderThan},
		},
		bson.M{
			"$set": bson.M{"state": StateReconciling, "reconcileReason": reason, "updatedAt": time.Now()},
			"$inc": bson.M{"version": 1},
		},
	)
	if err != nil {
		return 0, fmt.Errorf("betstore: réconciliation des orphelins: %w", err)
	}
	return res.ModifiedCount, nil
}

func (s *MongoStore) ListByState(ctx context.Context, state State, limit int) ([]Bet, error) {
	opts := options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}})
	if limit > 0 {
		opts = opts.SetLimit(int64(limit))
	}
	cur, err := s.col.Find(ctx, bson.M{"state": state}, opts)
	if err != nil {
		return nil, fmt.Errorf("betstore: liste: %w", err)
	}
	defer cur.Close(ctx)
	out := make([]Bet, 0)
	if err := cur.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("betstore: décodage: %w", err)
	}
	return out, nil
}
