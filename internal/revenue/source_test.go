package revenue

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func TestLedgerTotals(t *testing.T) {
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI non défini")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Disconnect(ctx)

	db := client.Database("223gaming_revenue_test")
	t.Cleanup(func() { _ = db.Drop(context.Background()) })
	_ = db.Drop(ctx)

	s, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}

	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	in := start.Add(10 * time.Hour)
	outside := start.Add(-2 * time.Hour)

	entry := func(txn, typ, status string, amount int64, at time.Time, parent string) bson.M {
		e := bson.M{
			"transactionId": txn, "idempotencyKey": "k_" + txn,
			"operatorId": "op1", "gameId": "223crash", "playerId": "p1",
			"type": typ, "status": status, "amountMinor": amount, "createdAt": at,
		}
		if parent != "" {
			e["parentTransactionId"] = parent
		}
		return e
	}

	col := db.Collection(ledgerCollection)
	_, err = col.InsertMany(ctx, []any{
		entry("d1", "DEBIT", "CONFIRMED", 1000, in, ""),
		entry("d2", "DEBIT", "CONFIRMED", 500, in, ""),
		entry("r2", "ROLLBACK", "CONFIRMED", 500, in.Add(time.Hour), "d2"),
		entry("d3", "DEBIT", "FAILED", 700, in, ""),
		entry("c1", "CREDIT", "CONFIRMED", 800, in, ""),
		entry("dX", "DEBIT", "CONFIRMED", 9999, outside, ""),
		entry("cX", "CREDIT", "CONFIRMED", 9999, outside, ""),
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, err := s.LedgerTotals(ctx, "op1", "223crash", start)
	if err != nil {
		t.Fatalf("LedgerTotals: %v", err)
	}
	if want := (Totals{Stakes: 1000, Payouts: 800}); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}

	// Un autre jeu / opérateur ne voit rien.
	other, err := s.LedgerTotals(ctx, "op2", "223crash", start)
	if err != nil || other != (Totals{}) {
		t.Fatalf("other operator: %+v, %v", other, err)
	}

	// Une entrée PENDING dans la période bloque la clôture.
	if _, err := col.InsertOne(ctx, entry("c2", "CREDIT", "PENDING", 300, in, "")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LedgerTotals(ctx, "op1", "223crash", start); !errors.Is(err, ErrPendingEntries) {
		t.Fatalf("expected ErrPendingEntries, got %v", err)
	}
}
