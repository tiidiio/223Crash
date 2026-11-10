package adminstore

import (
	"context"
	"errors"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readconcern"
	"go.mongodb.org/mongo-driver/v2/mongo/writeconcern"
)

func (s *Store) ProvisionFounder(
	ctx context.Context,
	admin *Admin,
	audit *AdminAudit,
) error {
	if s == nil || s.client == nil || s.db == nil {
		return errors.New("administrative database unavailable")
	}

	if admin == nil || audit == nil {
		return errors.New("invalid founder provisioning request")
	}

	if admin.ID == "" ||
		admin.Email == "" ||
		admin.PasswordHash == "" ||
		admin.Role != "founder" ||
		admin.Status != "active" {
		return errors.New("invalid founder account")
	}

	if strings.TrimSpace(admin.Email) != admin.Email ||
		strings.ToLower(admin.Email) != admin.Email {
		return errors.New("founder email must be normalized")
	}

	if audit.ID == "" ||
		audit.AdminID != admin.ID ||
		audit.Email != admin.Email ||
		audit.Event != "founder_created" {
		return errors.New("invalid founder audit event")
	}

	session, err := s.client.StartSession()
	if err != nil {
		return errors.New("cannot start founder provisioning session")
	}
	defer session.EndSession(context.Background())

	transactionOptions := options.Transaction().
		SetReadConcern(readconcern.Snapshot()).
		SetWriteConcern(writeconcern.Majority())

	_, err = session.WithTransaction(
		ctx,
		func(txCtx context.Context) (any, error) {
			founderCount, err := s.adminCollection().CountDocuments(
				txCtx,
				bson.M{"role": "founder"},
			)
			if err != nil {
				return nil, errors.New("cannot verify existing founder")
			}
			if founderCount != 0 {
				return nil, ErrConflict
			}

			emailCount, err := s.adminCollection().CountDocuments(
				txCtx,
				bson.M{"email": admin.Email},
			)
			if err != nil {
				return nil, errors.New("cannot verify administrator email")
			}
			if emailCount != 0 {
				return nil, ErrConflict
			}

			if _, err := s.adminCollection().InsertOne(txCtx, admin); err != nil {
				if mongo.IsDuplicateKeyError(err) {
					return nil, ErrConflict
				}
				return nil, errors.New("cannot insert founder account")
			}

			if _, err := s.auditCollection().InsertOne(txCtx, audit); err != nil {
				return nil, errors.New("cannot insert founder audit event")
			}

			return nil, nil
		},
		transactionOptions,
	)

	if errors.Is(err, ErrConflict) || mongo.IsDuplicateKeyError(err) {
		return ErrConflict
	}
	if err != nil {
		return errors.New("founder provisioning transaction failed")
	}

	return nil
}
