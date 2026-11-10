package adminstore

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

func (s *Store) FindAdminByID(ctx context.Context, id string) (*Admin, error) {
	if s == nil || s.db == nil || id == "" {
		return nil, errors.New("administrative database unavailable")
	}

	var admin Admin

	err := s.adminCollection().FindOne(
		ctx,
		bson.M{"_id": id},
	).Decode(&admin)

	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, ErrNotFound
		}
		return nil, errors.New("cannot retrieve administrator")
	}

	return &admin, nil
}

func (s *Store) UpdateSessionLastSeen(
	ctx context.Context,
	tokenHash string,
	at time.Time,
) error {
	if s == nil || s.db == nil || tokenHash == "" {
		return errors.New("administrative database unavailable")
	}

	result, err := s.sessionCollection().UpdateOne(
		ctx,
		bson.M{"token_hash": tokenHash},
		bson.M{"$set": bson.M{"last_seen_at": at}},
	)
	if err != nil {
		return errors.New("cannot update administrative session")
	}

	if result.MatchedCount != 1 {
		return ErrNotFound
	}

	return nil
}

func (s *Store) DeleteSessionsByAdminID(
	ctx context.Context,
	adminID string,
) error {
	if s == nil || s.db == nil || adminID == "" {
		return errors.New("administrative database unavailable")
	}

	_, err := s.sessionCollection().DeleteMany(
		ctx,
		bson.M{"admin_id": adminID},
	)
	if err != nil {
		return errors.New("cannot revoke administrator sessions")
	}

	return nil
}
