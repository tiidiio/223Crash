//go:build integration

package betstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func TestMongoStoreVoidContract(t *testing.T) {
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI non défini")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatalf("connexion: %v", err)
	}
	defer client.Disconnect(context.Background())

	runVoidContract(t, func(t *testing.T) VoidStore {
		b := make([]byte, 6)
		_, _ = rand.Read(b)
		col := client.Database("betstore_test").Collection("crash_bets_" + hex.EncodeToString(b))
		t.Cleanup(func() { _ = col.Drop(context.Background()) })
		s, err := NewMongoCollection(ctx, col)
		if err != nil {
			t.Fatalf("store: %v", err)
		}
		return s
	})
}
