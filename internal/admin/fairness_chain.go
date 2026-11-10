package admin

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/tiidiio/223Gaming/internal/engine"
)

// chainCollection : collection dédiée (évite tout conflit de schéma avec une éventuelle
// collection "fairness_chain" héritée d'anciennes versions).
const chainCollection = "engine_fairness_chain"

type chainDoc struct {
	Nonce          int64     `bson:"_id"` // _id = nonce : un nonce ne peut être commité qu'une fois
	RoundID        string    `bson:"roundId"`
	ServerSeed     string    `bson:"serverSeed"` // secret serveur : ne jamais exposer tant que Revealed == false
	ServerSeedHash string    `bson:"serverSeedHash"`
	ClientSeed     string    `bson:"clientSeed"`
	CrashPoint     float64   `bson:"crashPoint"`
	Revealed       bool      `bson:"revealed"`
	Aborted        bool      `bson:"aborted"`
	CommittedAt    time.Time `bson:"committedAt"`
	RevealedAt     time.Time `bson:"revealedAt,omitempty"`
}

// MongoChainStore persiste la chaîne de fairness du moteur (implémente engine.ChainStore).
type MongoChainStore struct {
	collection *mongo.Collection
}

var _ engine.ChainStore = (*MongoChainStore)(nil)

func NewMongoChainStore(db *mongo.Database) *MongoChainStore {
	if db == nil {
		return &MongoChainStore{}
	}
	return &MongoChainStore{collection: db.Collection(chainCollection)}
}

func (s *MongoChainStore) ready() error {
	if s == nil || s.collection == nil {
		return errors.New("chain store MongoDB non initialisé")
	}
	return nil
}

func (s *MongoChainStore) LoadLast(ctx context.Context) (*engine.ChainRecord, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	var d chainDoc
	err := s.collection.FindOne(ctx, bson.D{},
		options.FindOne().SetSort(bson.D{{Key: "_id", Value: -1}})).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("chain MongoDB LoadLast: %w", err)
	}
	return &engine.ChainRecord{
		RoundID: d.RoundID, Nonce: d.Nonce, ServerSeed: d.ServerSeed, ServerSeedHash: d.ServerSeedHash,
		ClientSeed: d.ClientSeed, CrashPoint: d.CrashPoint, Revealed: d.Revealed, Aborted: d.Aborted,
		CommittedAt: d.CommittedAt, RevealedAt: d.RevealedAt,
	}, nil
}

func (s *MongoChainStore) Commit(ctx context.Context, rec engine.ChainRecord) error {
	if err := s.ready(); err != nil {
		return err
	}
	_, err := s.collection.InsertOne(ctx, chainDoc{
		Nonce: rec.Nonce, RoundID: rec.RoundID, ServerSeed: rec.ServerSeed,
		ServerSeedHash: rec.ServerSeedHash, ClientSeed: rec.ClientSeed, CrashPoint: rec.CrashPoint,
		CommittedAt: rec.CommittedAt,
	})
	if err != nil {
		return fmt.Errorf("chain MongoDB Commit (nonce %d): %w", rec.Nonce, err)
	}
	return nil
}

// Reveal est idempotent : rejouer la révélation d'un round déjà révélé ne change rien.
func (s *MongoChainStore) Reveal(ctx context.Context, nonce int64, crashPoint float64, aborted bool) error {
	if err := s.ready(); err != nil {
		return err
	}
	_, err := s.collection.UpdateOne(ctx,
		bson.D{{Key: "_id", Value: nonce}, {Key: "revealed", Value: false}},
		bson.D{{Key: "$set", Value: bson.D{
			{Key: "revealed", Value: true},
			{Key: "aborted", Value: aborted},
			{Key: "crashPoint", Value: crashPoint},
			{Key: "revealedAt", Value: time.Now()},
		}}})
	if err != nil {
		return fmt.Errorf("chain MongoDB Reveal (nonce %d): %w", nonce, err)
	}
	return nil
}
