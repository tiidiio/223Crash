package mongobridge

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const (
	CollBroadcast       = "crash_broadcast"
	CollEvents          = "crash_events"
	CollCommands        = "crash_commands"
	namespaceExistsCode = 48
)

type Store struct {
	Client    *mongo.Client
	DB        *mongo.Database
	Broadcast *mongo.Collection
	Events    *mongo.Collection
	Commands  *mongo.Collection
}

func NewStore(ctx context.Context, uri, dbName string) (*Store, error) {
	clientOpts := options.Client().
		ApplyURI(uri).
		SetServerSelectionTimeout(30 * time.Second).
		SetConnectTimeout(30 * time.Second).
		SetTimeout(30 * time.Second)

	client, err := mongo.Connect(clientOpts)
	if err != nil {
		return nil, fmt.Errorf("mongobridge: connect: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx, nil); err != nil {
		_ = client.Disconnect(context.Background())
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

	// ensureCapped best-effort : ne bloque pas le démarrage si Atlas timeout
	if err := s.ensureCapped(ctx, CollBroadcast, 50_000_000, 100_000); err != nil {
		log.Printf("mongobridge: warn ensureCapped %s: %v (continue)", CollBroadcast, err)
	}
	if err := s.ensureCapped(ctx, CollEvents, 50_000_000, 100_000); err != nil {
		log.Printf("mongobridge: warn ensureCapped %s: %v (continue)", CollEvents, err)
	}
	if err := s.ensureCapped(ctx, CollCommands, 20_000_000, 50_000); err != nil {
		log.Printf("mongobridge: warn ensureCapped %s: %v (continue)", CollCommands, err)
	}

	return s, nil
}

func (s *Store) ensureCapped(ctx context.Context, name string, sizeBytes, maxDocs int64) error {
	// 1. check existence via ListCollectionNames filter — évite CreateCollection si existe
	listCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	names, err := s.DB.ListCollectionNames(listCtx, bson.D{{Key: "name", Value: name}})
	if err == nil && len(names) > 0 {
		return nil
	}
	// 2. sinon tente création
	createCtx, cancel2 := context.WithTimeout(ctx, 15*time.Second)
	defer cancel2()
	opts := options.CreateCollection().
		SetCapped(true).
		SetSizeInBytes(sizeBytes).
		SetMaxDocuments(maxDocs)

	err = s.DB.CreateCollection(createCtx, name, opts)
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

func (s *Store) Ping(ctx context.Context) error {
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return s.Client.Ping(pingCtx, nil)
}

func (s *Store) Close(ctx context.Context) error {
	return s.Client.Disconnect(ctx)
}
