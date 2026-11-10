package financial

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// EnsureFinancialIndexes creates all mandatory unique and performance indexes for MongoDB.
func EnsureFinancialIndexes(ctx context.Context, db *mongo.Database) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	transactionsColl := db.Collection("financial_transactions")

	// 1. Unique index on (operator_id, idempotency_key) to prevent double debit/credit
	_, err := transactionsColl.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "operator_id", Value: 1},
			{Key: "idempotency_key", Value: 1},
		},
		Options: options.Index().SetUnique(true).SetName("idx_operator_idempotency_unique"),
	})
	if err != nil {
		return fmt.Errorf("failed to create idempotency unique index: %w", err)
	}

	// 2. Unique index on transaction_id
	_, err = transactionsColl.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "transaction_id", Value: 1}},
		Options: options.Index().SetUnique(true).SetName("idx_transaction_id_unique"),
	})
	if err != nil {
		return fmt.Errorf("failed to create transaction_id unique index: %w", err)
	}

	// 3. Compound index for player history & reporting isolation per operator
	_, err = transactionsColl.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "operator_id", Value: 1},
			{Key: "player_id", Value: 1},
			{Key: "created_at", Value: -1},
		},
		Options: options.Index().SetName("idx_operator_player_history"),
	})
	if err != nil {
		return fmt.Errorf("failed to create player history index: %w", err)
	}

	// Outbox Collection Indexes
	outboxColl := db.Collection("financial_outbox")

	// 4. Index for outbox worker polling & lease recovery
	_, err = outboxColl.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "status", Value: 1},
			{Key: "next_retry_at", Value: 1},
			{Key: "locked_until", Value: 1},
		},
		Options: options.Index().SetName("idx_outbox_worker_poll"),
	})
	if err != nil {
		return fmt.Errorf("failed to create outbox poll index: %w", err)
	}

	return nil
}
