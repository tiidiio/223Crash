package adminstore

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

var (
	ErrNotFound = errors.New("administrative record not found")
	ErrConflict = errors.New("administrative record already exists")
)

type Admin struct {
	ID           string    `bson:"_id" json:"id"`
	Email        string    `bson:"email" json:"email"`
	PasswordHash string    `bson:"password_hash" json:"-"`
	Role         string    `bson:"role" json:"role"`
	OperatorID   string    `bson:"operator_id,omitempty" json:"operator_id,omitempty"`
	Status       string    `bson:"status" json:"status"`
	CreatedAt    time.Time `bson:"created_at" json:"created_at"`
	UpdatedAt    time.Time `bson:"updated_at" json:"updated_at"`
	LastLoginAt  time.Time `bson:"last_login_at,omitempty" json:"last_login_at,omitempty"`
}

type AdminSession struct {
	ID         string    `bson:"_id" json:"id"`
	AdminID    string    `bson:"admin_id" json:"admin_id"`
	Role       string    `bson:"role" json:"role"`
	OperatorID string    `bson:"operator_id,omitempty" json:"operator_id,omitempty"`
	TokenHash  string    `bson:"token_hash" json:"-"`
	CSRFToken  string    `bson:"csrf_token" json:"-"`
	CreatedAt  time.Time `bson:"created_at" json:"created_at"`
	ExpiresAt  time.Time `bson:"expires_at" json:"expires_at"`
	LastSeenAt time.Time `bson:"last_seen_at" json:"last_seen_at"`
}

type AdminAudit struct {
	ID        string    `bson:"_id" json:"id"`
	AdminID   string    `bson:"admin_id,omitempty" json:"admin_id,omitempty"`
	Email     string    `bson:"email,omitempty" json:"email,omitempty"`
	Event     string    `bson:"event" json:"event"`
	IPAddress string    `bson:"ip_address,omitempty" json:"ip_address,omitempty"`
	UserAgent string    `bson:"user_agent,omitempty" json:"user_agent,omitempty"`
	CreatedAt time.Time `bson:"created_at" json:"created_at"`
}

func (s *Store) adminCollection() *mongo.Collection {
	return s.db.Collection("provider_admins")
}

func (s *Store) sessionCollection() *mongo.Collection {
	return s.db.Collection("provider_admin_sessions")
}

func (s *Store) auditCollection() *mongo.Collection {
	return s.db.Collection("provider_admin_audit")
}

func (s *Store) FindAdminByEmail(ctx context.Context, email string) (*Admin, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("administrative database unavailable")
	}

	var admin Admin
	err := s.adminCollection().FindOne(ctx, bson.M{
		"email": email,
	}).Decode(&admin)

	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, errors.New("cannot retrieve administrator")
	}

	return &admin, nil
}

func (s *Store) InsertAdmin(ctx context.Context, admin *Admin) error {
	if s == nil || s.db == nil || admin == nil {
		return errors.New("invalid administrative database operation")
	}

	_, err := s.adminCollection().InsertOne(ctx, admin)
	if mongo.IsDuplicateKeyError(err) {
		return ErrConflict
	}
	if err != nil {
		return errors.New("cannot create administrator")
	}

	return nil
}

func (s *Store) CountFounders(ctx context.Context) (int64, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("administrative database unavailable")
	}

	count, err := s.adminCollection().CountDocuments(ctx, bson.M{
		"role": "founder",
	})
	if err != nil {
		return 0, errors.New("cannot verify founder provisioning")
	}

	return count, nil
}

func (s *Store) UpdateAdminLastLogin(ctx context.Context, adminID string, at time.Time) error {
	if s == nil || s.db == nil {
		return errors.New("administrative database unavailable")
	}

	result, err := s.adminCollection().UpdateOne(
		ctx,
		bson.M{"_id": adminID},
		bson.M{"$set": bson.M{
			"last_login_at": at,
			"updated_at":    at,
		}},
	)
	if err != nil || result.MatchedCount != 1 {
		return errors.New("cannot update administrator login")
	}

	return nil
}

func (s *Store) InsertSession(ctx context.Context, session *AdminSession) error {
	if s == nil || s.db == nil || session == nil {
		return errors.New("invalid session operation")
	}

	_, err := s.sessionCollection().InsertOne(ctx, session)
	if mongo.IsDuplicateKeyError(err) {
		return ErrConflict
	}
	if err != nil {
		return errors.New("cannot create administrative session")
	}

	return nil
}

func (s *Store) FindSession(ctx context.Context, tokenHash string, now time.Time) (*AdminSession, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("administrative database unavailable")
	}

	var session AdminSession
	err := s.sessionCollection().FindOne(ctx, bson.M{
		"token_hash": tokenHash,
		"expires_at": bson.M{"$gt": now},
	}).Decode(&session)

	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, errors.New("cannot retrieve administrative session")
	}

	return &session, nil
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	if s == nil || s.db == nil {
		return errors.New("administrative database unavailable")
	}

	_, err := s.sessionCollection().DeleteOne(ctx, bson.M{
		"token_hash": tokenHash,
	})
	if err != nil {
		return errors.New("cannot revoke administrative session")
	}

	return nil
}

func (s *Store) InsertAudit(ctx context.Context, event *AdminAudit) error {
	if s == nil || s.db == nil || event == nil {
		return errors.New("invalid audit operation")
	}

	_, err := s.auditCollection().InsertOne(ctx, event)
	if err != nil {
		return errors.New("cannot persist administrative audit event")
	}

	return nil
}
