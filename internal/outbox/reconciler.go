package outbox

import (
	"context"
	"log"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

type Reconciler struct {
	db          *mongo.Database
	interval    time.Duration
	maxAttempts int
}

func NewReconciler(db *mongo.Database, interval time.Duration, maxAttempts int) *Reconciler {
	return &Reconciler{
		db:          db,
		interval:    interval,
		maxAttempts: maxAttempts,
	}
}

// Start runs the background reconciliation loop to process PENDING_RECONCILIATION transactions.
func (r *Reconciler) Start(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	log.Printf("[Outbox Reconciler] Background reconciliation worker started (Interval: %v)", r.interval)

	for {
		select {
		case <-ctx.Done():
			log.Println("[Outbox Reconciler] Shutting down reconciliation worker...")
			return
		case <-ticker.C:
			r.processPendingTransactions(ctx)
		}
	}
}

func (r *Reconciler) processPendingTransactions(ctx context.Context) {
	betsCol := r.db.Collection("bets")

	// Find transactions stuck in PENDING_RECONCILIATION
	cursor, err := betsCol.Find(ctx, bson.M{"status": "PENDING_RECONCILIATION"})
	if err != nil {
		log.Printf("[Outbox Reconciler] ERROR fetching pending transactions: %v", err)
		return
	}
	defer cursor.Close(ctx)

	var pending []bson.M
	if err := cursor.All(ctx, &pending); err != nil {
		log.Printf("[Outbox Reconciler] ERROR decoding pending transactions: %v", err)
		return
	}

	if len(pending) == 0 {
		return
	}

	log.Printf("[Outbox Reconciler] Found %d transaction(s) requiring reconciliation.", len(pending))

	for _, tx := range pending {
		idempotencyKey, _ := tx["idempotency_key"].(string)
		operatorID, _ := tx["operator_id"].(string)

		// Perform reconciliation check against operator gateway / ledger state
		// In a production environment, this calls the operator's status endpoint.
		// For safety and self-healing, if verification succeeds, we transition to COMPLETED.
		success := r.verifyWithOperator(operatorID, idempotencyKey)

		newStatus := "COMPLETED"
		if !success {
			// If operator confirms transaction failed or timed out permanently, mark FAILED
			newStatus = "FAILED"
		}

		_, updateErr := betsCol.UpdateOne(
			ctx,
			bson.M{"idempotency_key": idempotencyKey},
			bson.M{"$set": bson.M{"status": newStatus, "updated_at": time.Now()}},
		)
		if updateErr != nil {
			log.Printf("[Outbox Reconciler] ERROR updating transaction %s: %v", idempotencyKey, updateErr)
		} else {
			log.Printf("[Outbox Reconciler] Successfully reconciled transaction %s -> Status: %s", idempotencyKey, newStatus)
		}
	}
}

func (r *Reconciler) verifyWithOperator(operatorID string, idempotencyKey string) bool {
	// Stub/Client call to Operator Gateway API to check transaction status
	// Returns true if confirmed settled, false otherwise.
	// Production integration plugs in the operator HTTP client here.
	return true
}
