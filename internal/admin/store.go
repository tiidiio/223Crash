package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"golang.org/x/crypto/bcrypt"
)

const sessionDuration = 8 * time.Hour

var (
	ErrInvalidCredentials = errors.New("admin: identifiants invalides")
	ErrInvalidSession     = errors.New("admin: session invalide ou expirée")
)

type Config struct {
	Email         string
	PasswordHash  string
	SessionSecret string
}

type Store struct {
	operators     *mongo.Collection
	adminUsers    *mongo.Collection
	sessions      *mongo.Collection
	sessionSecret []byte
}

type Operator struct {
	ID                   bson.ObjectID `bson:"_id,omitempty" json:"id"`
	Code                 string        `bson:"code" json:"code"`
	Name                 string        `bson:"name" json:"name"`
	Currency             string        `bson:"currency" json:"currency"`
	CallbackURL          string        `bson:"callbackUrl" json:"callbackUrl"`
	WalletHMACSecretHash string        `bson:"walletHmacSecretHash" json:"-"`
	Status               string        `bson:"status" json:"status"`
	CreatedAt            time.Time     `bson:"createdAt" json:"createdAt"`
}

type AdminUser struct {
	Email        string    `bson:"email"`
	PasswordHash string    `bson:"passwordHash"`
	CreatedAt    time.Time `bson:"createdAt"`
}

type adminSession struct {
	TokenHash  string    `bson:"tokenHash"`
	AdminEmail string    `bson:"adminEmail"`
	ExpiresAt  time.Time `bson:"expiresAt"`
}

func NewStore(ctx context.Context, db *mongo.Database, cfg Config) (*Store, error) {
	if db == nil {
		return nil, errors.New("admin: MongoDB database is nil")
	}

	cfg.Email = strings.TrimSpace(strings.ToLower(cfg.Email))

	if cfg.Email == "" || cfg.PasswordHash == "" {
		return nil, errors.New("admin: ADMIN_EMAIL et ADMIN_PASSWORD_HASH sont requis")
	}

	if len(cfg.SessionSecret) < 32 {
		return nil, errors.New("admin: ADMIN_SESSION_SECRET doit contenir au moins 32 octets")
	}

	if _, err := bcrypt.Cost([]byte(cfg.PasswordHash)); err != nil {
		return nil, fmt.Errorf("admin: ADMIN_PASSWORD_HASH bcrypt invalide: %w", err)
	}

	s := &Store{
		operators:     db.Collection("operators"),
		adminUsers:    db.Collection("admin_users"),
		sessions:      db.Collection("admin_sessions"),
		sessionSecret: []byte(cfg.SessionSecret),
	}

	if err := s.ensureIndexes(ctx); err != nil {
		return nil, err
	}

	if err := s.bootstrapAdmin(ctx, cfg.Email, cfg.PasswordHash); err != nil {
		return nil, err
	}

	return s, nil
}

func (s *Store) ensureIndexes(ctx context.Context) error {
	indexes := []struct {
		collection *mongo.Collection
		model      mongo.IndexModel
	}{
		{
			s.operators,
			mongo.IndexModel{
				Keys: bson.D{{Key: "code", Value: 1}},
				Options: options.Index().
					SetName("operators_code_unique").
					SetUnique(true),
			},
		},
		{
			s.adminUsers,
			mongo.IndexModel{
				Keys: bson.D{{Key: "email", Value: 1}},
				Options: options.Index().
					SetName("admin_users_email_unique").
					SetUnique(true),
			},
		},
		{
			s.sessions,
			mongo.IndexModel{
				Keys: bson.D{{Key: "tokenHash", Value: 1}},
				Options: options.Index().
					SetName("admin_sessions_token_hash_unique").
					SetUnique(true),
			},
		},
		{
			s.sessions,
			mongo.IndexModel{
				Keys: bson.D{{Key: "expiresAt", Value: 1}},
				Options: options.Index().
					SetName("admin_sessions_expiry").
					SetExpireAfterSeconds(0),
			},
		},
	}

	for _, item := range indexes {
		if _, err := item.collection.Indexes().CreateOne(ctx, item.model); err != nil {
			return fmt.Errorf("admin: création index MongoDB: %w", err)
		}
	}

	return nil
}

func (s *Store) bootstrapAdmin(ctx context.Context, email, passwordHash string) error {
	now := time.Now().UTC()

	_, err := s.adminUsers.UpdateOne(
		ctx,
		bson.D{{Key: "email", Value: email}},
		bson.D{
			{Key: "$set", Value: bson.D{
				{Key: "passwordHash", Value: passwordHash},
			}},
			{Key: "$setOnInsert", Value: bson.D{
				{Key: "createdAt", Value: now},
			}},
		},
		options.UpdateOne().SetUpsert(true),
	)

	if err != nil {
		return fmt.Errorf("admin: initialisation du compte administrateur: %w", err)
	}

	return nil
}

func (s *Store) Authenticate(ctx context.Context, email, password string) (string, error) {
	if s == nil || s.adminUsers == nil || s.sessions == nil {
		return "", errors.New("admin: store non initialisé")
	}

	email = strings.TrimSpace(strings.ToLower(email))

	if email == "" || password == "" {
		return "", ErrInvalidCredentials
	}

	var user AdminUser

	err := s.adminUsers.FindOne(
		ctx,
		bson.D{{Key: "email", Value: email}},
	).Decode(&user)

	if errors.Is(err, mongo.ErrNoDocuments) {
		return "", ErrInvalidCredentials
	}

	if err != nil {
		return "", fmt.Errorf("admin: lecture du compte administrateur: %w", err)
	}

	compareErr := bcrypt.CompareHashAndPassword(
		[]byte(user.PasswordHash),
		[]byte(password),
	)

	if errors.Is(compareErr, bcrypt.ErrMismatchedHashAndPassword) {
		return "", ErrInvalidCredentials
	}

	if compareErr != nil {
		return "", fmt.Errorf("admin: vérification du hash bcrypt: %w", compareErr)
	}

	tokenBytes := make([]byte, 32)

	if _, err := rand.Read(tokenBytes); err != nil {
		return "", fmt.Errorf("admin: génération session: %w", err)
	}

	token := base64.RawURLEncoding.EncodeToString(tokenBytes)

	expiresAt := time.Now().UTC().Add(sessionDuration)

	session := adminSession{
		TokenHash:  s.hashToken(token),
		AdminEmail: user.Email,
		ExpiresAt:  expiresAt,
	}

	if _, err := s.sessions.InsertOne(ctx, session); err != nil {
		return "", fmt.Errorf("admin: création session: %w", err)
	}

	return token, nil
}

func (s *Store) hashToken(token string) string {
	sum := sha256.Sum256(
		append(
			append([]byte(nil), s.sessionSecret...),
			[]byte(":"+token)...,
		),
	)

	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (s *Store) ValidateSession(ctx context.Context, token string) (string, error) {
	if s == nil || s.sessions == nil {
		return "", errors.New("admin: store de sessions non initialisé")
	}

	if token == "" || len(token) != 43 {
		return "", ErrInvalidSession
	}

	rawToken, decodeErr := base64.RawURLEncoding.DecodeString(token)
	if decodeErr != nil || len(rawToken) != 32 {
		return "", ErrInvalidSession
	}

	var session adminSession

	err := s.sessions.FindOne(
		ctx,
		bson.D{
			{Key: "tokenHash", Value: s.hashToken(token)},
			{Key: "expiresAt", Value: bson.D{
				{Key: "$gt", Value: time.Now().UTC()},
			}},
		},
	).Decode(&session)

	if errors.Is(err, mongo.ErrNoDocuments) {
		return "", ErrInvalidSession
	}

	if err != nil {
		return "", fmt.Errorf("admin: vérification session MongoDB: %w", err)
	}

	if strings.TrimSpace(session.AdminEmail) == "" {
		return "", ErrInvalidSession
	}

	return session.AdminEmail, nil
}

func (s *Store) DeleteSession(ctx context.Context, token string) error {
	if s == nil || s.sessions == nil {
		return errors.New("admin: store de sessions non initialisé")
	}

	if token == "" {
		return nil
	}

	if len(token) != 43 {
		return nil
	}

	rawToken, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(rawToken) != 32 {
		return nil
	}

	_, err = s.sessions.DeleteOne(
		ctx,
		bson.D{{Key: "tokenHash", Value: s.hashToken(token)}},
	)

	if err != nil {
		return fmt.Errorf("admin: suppression session MongoDB: %w", err)
	}

	return nil
}

func (s *Store) ListOperators(ctx context.Context) ([]Operator, error) {
	if s == nil || s.operators == nil {
		return nil, errors.New("admin: store opérateurs non initialisé")
	}

	cursor, err := s.operators.Find(
		ctx,
		bson.D{},
		options.Find().SetSort(
			bson.D{
				{Key: "createdAt", Value: -1},
				{Key: "_id", Value: 1},
			},
		),
	)

	if err != nil {
		return nil, err
	}

	defer cursor.Close(ctx)

	result := make([]Operator, 0)

	if err := cursor.All(ctx, &result); err != nil {
		return nil, err
	}

	return result, nil
}

func (s *Store) CreateOperator(ctx context.Context, operator Operator) error {
	if s == nil || s.operators == nil {
		return errors.New("admin: store opérateurs non initialisé")
	}

	operator.Status = "active"

	if operator.CreatedAt.IsZero() {
		operator.CreatedAt = time.Now().UTC()
	}

	_, err := s.operators.InsertOne(ctx, operator)

	return err
}

// MongoNonceStore fournit un compteur monotone atomique partagé entre les
// instances du moteur qui utilisent la même base MongoDB.
type MongoNonceStore struct {
	collection *mongo.Collection
}

func NewMongoNonceStore(db *mongo.Database) *MongoNonceStore {
	if db == nil {
		return &MongoNonceStore{}
	}

	return &MongoNonceStore{
		collection: db.Collection("counters"),
	}
}

func (s *MongoNonceStore) NextNonce(ctx context.Context) (int64, error) {
	if s == nil || s.collection == nil {
		return 0, errors.New("nonce store MongoDB non initialisé")
	}

	var result struct {
		Value int64 `bson:"value"`
	}

	err := s.collection.FindOneAndUpdate(
		ctx,
		bson.D{{Key: "_id", Value: "nonce"}},
		bson.D{{Key: "$inc", Value: bson.D{
			{Key: "value", Value: int64(1)},
		}}},
		options.FindOneAndUpdate().
			SetUpsert(true).
			SetReturnDocument(options.After),
	).Decode(&result)

	if err != nil {
		return 0, fmt.Errorf("nonce MongoDB: %w", err)
	}

	return result.Value, nil
}

func (s *Store) DB() *mongo.Database {
	if s == nil || s.operators == nil {
		return nil
	}
	return s.operators.Database()
}
