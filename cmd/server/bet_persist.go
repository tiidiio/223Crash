package main

import (
	"context"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/tiidiio/223Crash/provider/ledger"
)

type BetStore interface {
	Remember(ctx context.Context, betID string, debitTxnID ledger.TransactionID, operatorID, playerID string) error
	GetDebitTxnID(betID string) (ledger.TransactionID, bool)
}

// --- MEM (fallback dev) ---
type memBetStore struct {
	mu sync.RWMutex
	m  map[string]ledger.TransactionID
}

func NewMemBetStore() BetStore { return &memBetStore{m: make(map[string]ledger.TransactionID)} }
func (s *memBetStore) Remember(_ context.Context, betID string, debitTxnID ledger.TransactionID, _, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[betID] = debitTxnID
	return nil
}
func (s *memBetStore) GetDebitTxnID(betID string) (ledger.TransactionID, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.m[betID]
	return v, ok
}

// --- MONGO persistant (prod) ---
type mongoBetStore struct{ col *mongo.Collection }

func NewMongoBetStore(db *mongo.Database) BetStore {
	col := db.Collection("bet_debit_map")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = col.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "betId", Value: 1}, {Key: "operatorId", Value: 1}, {Key: "playerId", Value: 1}}, Options: options.Index().SetUnique(true).SetName("betId_operator_player_unique")},
		{Keys: bson.D{{Key: "debitTxnId", Value: 1}}, Options: options.Index().SetUnique(true).SetName("debitTxnId_unique")},
		{Keys: bson.D{{Key: "createdAt", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(72 * 3600).SetName("ttl_72h")},
	})
	return &mongoBetStore{col: col}
}

func (s *mongoBetStore) Remember(ctx context.Context, betID string, debitTxnID ledger.TransactionID, operatorID, playerID string) error {
	filter := bson.M{"betId": betID, "operatorId": operatorID, "playerId": playerID}
	update := bson.M{
		"$setOnInsert": bson.M{"betId": betID, "debitTxnId": string(debitTxnID), "operatorId": operatorID, "playerId": playerID, "createdAt": time.Now()},
		"$set":         bson.M{"updatedAt": time.Now()},
	}
	_, err := s.col.UpdateOne(ctx, filter, update, options.UpdateOne().SetUpsert(true))
	return err
}

func (s *mongoBetStore) GetDebitTxnID(betID string) (ledger.TransactionID, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var doc struct {
		DebitTxnId string `bson:"debitTxnId"`
	}
	// dernier en date si plusieurs (défense)
	opts := options.FindOne().SetSort(bson.D{{Key: "createdAt", Value: -1}})
	err := s.col.FindOne(ctx, bson.M{"betId": betID}, opts).Decode(&doc)
	if err != nil {
		return "", false
	}
	return ledger.TransactionID(doc.DebitTxnId), true
}

func NewMongoBetStoreAlias() BetStore { return NewMemBetStore() }
