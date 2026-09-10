package mongobridge

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const (
	CollBroadcast = "crash_broadcast"
	CollEvents    = "crash_events"
	CollCommands  = "crash_commands"

	namespaceExistsCode = 48
)

type Store struct {
	Client    *mongo.Client
	DB        *mongo.Database
	Broadcast *mongo.Collection
	Events    *mongo.Collection
	Commands  *mongo.Collection
}

// NewStore connects to Atlas, verifies the connection, and ensures the three
// capped collections exist. Safe to call concurrently from engine and
// gateway at boot — a racing creator's "NamespaceExists" is swallowed.
func NewStore(ctx context.Context, uri, dbName string) (*Store, error) {
	clientOpts := options.Client().
		ApplyURI(uri).
		SetServerSelectionTimeout(10 * time.Second)

	client, err := mongo.Connect(clientOpts)
	if err != nil {
		return nil, fmt.Errorf("mongobridge: connect: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx, nil); err != nil {
		return nil, fmt.Errorf("mongobridge: ping: %w", err)
	}

	db := client.Database(dbName)
	s := &Store{
		Client:    client,
		DB:        db,
		Broadcast: db.Collection(CollBroadcast),
		Events:    db.Collection(CollEvents),
		Commands:  db.Collection(CollCommands),
	}

	if err := s.ensureCapped(ctx, CollBroadcast, 50_000_000, 100_000); err != nil {
		return nil, err
	}
	if err := s.ensureCapped(ctx, CollEvents, 50_000_000, 100_000); err != nil {
		return nil, err
	}
	if err := s.ensureCapped(ctx, CollCommands, 20_000_000, 50_000); err != nil {
		return nil, err
	}

	return s, nil
}

func (s *Store) ensureCapped(ctx context.Context, name string, sizeBytes, maxDocs int64) error {
	opts := options.CreateCollection().
		SetCapped(true).
		SetSizeInBytes(sizeBytes).
		SetMaxDocuments(maxDocs)

	err := s.DB.CreateCollection(ctx, name, opts)
	if err == nil || isNamespaceExists(err) {
		return nil
	}
	return fmt.Errorf("mongobridge: create collection %s: %w", name, err)
}

func isNamespaceExists(err error) bool {
	var cmdErr mongo.CommandError
	if errors.As(err, &cmdErr) {
		return cmdErr.Code == namespaceExistsCode
	}
	return false
}

func (s *Store) Close(ctx context.Context) error {
	return s.Client.Disconnect(ctx)
}
