package adminstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

type Store struct {
	client *mongo.Client
	db     *mongo.Database
}

func Connect(ctx context.Context, uri, database string) (*Store, error) {
	if ctx == nil {
		return nil, errors.New("MongoDB context is required")
	}

	if uri == "" || database == "" {
		return nil, errors.New("MongoDB configuration is incomplete")
	}

	clientOptions := options.Client().
		ApplyURI(uri).
		SetServerSelectionTimeout(10 * time.Second).
		SetConnectTimeout(10 * time.Second)

	client, err := mongo.Connect(clientOptions)
	if err != nil {
		return nil, errors.New("MongoDB client initialization failed")
	}

	pingCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	if err := client.Ping(pingCtx, readpref.Primary()); err != nil {
		disconnectCtx, disconnectCancel := context.WithTimeout(
			context.Background(),
			5*time.Second,
		)
		defer disconnectCancel()

		_ = client.Disconnect(disconnectCtx)
		return nil, errors.New("MongoDB connectivity check failed")
	}

	return &Store{
		client: client,
		db:     client.Database(database),
	}, nil
}

// Database expose la base MongoDB déjà ouverte afin que le composition root la partage avec
// les autres composants (ex. ledger.NewReader). Le Store reste l'UNIQUE propriétaire du client :
// les composants qui empruntent la base ne la ferment jamais et n'ouvrent aucune connexion.
func (s *Store) Database() *mongo.Database {
	if s == nil {
		return nil
	}
	return s.db
}

func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.client == nil {
		return errors.New("MongoDB is not configured")
	}
	return s.client.Ping(ctx, readpref.Primary())
}

func (s *Store) Close(ctx context.Context) error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.Disconnect(ctx)
}

func (s *Store) EnsureIndexes(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("MongoDB is not initialized")
	}

	indexes := []struct {
		collection string
		model      mongo.IndexModel
	}{
		{
			collection: "provider_admins",
			model: mongo.IndexModel{
				Keys:    bson.D{{Key: "email", Value: 1}},
				Options: options.Index().SetUnique(true),
			},
		},
		{
			collection: "provider_admins",
			model: mongo.IndexModel{
				Keys: bson.D{{Key: "role", Value: 1}},
				Options: options.Index().
					SetName("provider_one_founder").
					SetUnique(true).
					SetPartialFilterExpression(
						bson.D{{Key: "role", Value: "founder"}},
					),
			},
		},
		{
			collection: "provider_admin_sessions",
			model: mongo.IndexModel{
				Keys:    bson.D{{Key: "token_hash", Value: 1}},
				Options: options.Index().SetUnique(true),
			},
		},
		{
			collection: "provider_admin_sessions",
			model: mongo.IndexModel{
				Keys:    bson.D{{Key: "expires_at", Value: 1}},
				Options: options.Index().SetExpireAfterSeconds(0),
			},
		},
		{
			collection: "provider_admin_audit",
			model: mongo.IndexModel{
				Keys: bson.D{{Key: "created_at", Value: -1}},
			},
		},
		{
			collection: "provider_operators",
			model: mongo.IndexModel{
				Keys:    bson.D{{Key: "operator_id", Value: 1}},
				Options: options.Index().SetName("operator_id_1").SetUnique(true),
			},
		},
		{
			collection: "provider_operators",
			model: mongo.IndexModel{
				Keys: bson.D{{Key: "code", Value: 1}},
				Options: options.Index().
					SetName("provider_operator_code_unique").
					SetUnique(true).
					SetSparse(true),
			},
		},
		{
			collection: "provider_operators",
			model: mongo.IndexModel{
				Keys: bson.D{
					{Key: "created_at", Value: -1},
					{Key: "_id", Value: 1},
				},
				Options: options.Index().SetName("provider_operators_created_at"),
			},
		},
	}

	for _, item := range indexes {
		_, err := s.db.Collection(item.collection).
			Indexes().
			CreateOne(ctx, item.model)

		if err != nil {
			return fmt.Errorf(
				"cannot initialize administrative indexes: %w",
				err,
			)
		}
	}

	return nil
}
