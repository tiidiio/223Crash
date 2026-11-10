package adminstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const (
	operatorsCollection = "provider_operators"

	DefaultOperatorListLimit int64 = 50
	MaxOperatorListLimit     int64 = 100
)

var (
	ErrInvalidOperator  = errors.New("invalid operator")
	ErrOperatorConflict = errors.New("operator already exists")
	ErrOperatorNotFound = errors.New("operator not found")

	operatorCodePattern = regexp.MustCompile(`^[A-Z0-9_]{3,20}$`)

	allowedOperatorCurrencies = map[string]struct{}{
		"XOF": {},
		"XAF": {},
		"EUR": {},
		"USD": {},
		"GNF": {},
		"MAD": {},
	}
)

// Operator représente un opérateur B2B enregistré par le Provider Core.
// OperatorID est l'identifiant technique ; Code est le code métier visible.
type Operator struct {
	ID          string    `bson:"_id" json:"id"`
	OperatorID  string    `bson:"operator_id" json:"operatorId"`
	Name        string    `bson:"name" json:"name"`
	Code        string    `bson:"code" json:"code"`
	Currency    string    `bson:"currency" json:"currency"`
	CallbackURL string    `bson:"callback_url,omitempty" json:"callbackUrl,omitempty"`
	Status      string    `bson:"status" json:"status"`
	CreatedBy   string    `bson:"created_by,omitempty" json:"createdBy,omitempty"`
	CreatedAt   time.Time `bson:"created_at" json:"createdAt"`
	UpdatedAt   time.Time `bson:"updated_at" json:"updatedAt"`
}

// CreateOperatorInput contient exclusivement les champs acceptés à la création.
// L'identifiant, le statut et les dates sont générés côté serveur.
type CreateOperatorInput struct {
	Name        string `json:"name"`
	Code        string `json:"code"`
	Currency    string `json:"currency"`
	CallbackURL string `json:"callbackUrl"`
}

// ValidateOperatorInput normalise les champs et vérifie les règles métier.
// La valeur retournée peut être directement utilisée pour la persistance.
func ValidateOperatorInput(input CreateOperatorInput) (CreateOperatorInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Code = strings.ToUpper(strings.TrimSpace(input.Code))
	input.Currency = strings.ToUpper(strings.TrimSpace(input.Currency))
	input.CallbackURL = strings.TrimSpace(input.CallbackURL)

	if !utf8.ValidString(input.Name) || utf8.RuneCountInString(input.Name) < 2 ||
		utf8.RuneCountInString(input.Name) > 60 {
		return CreateOperatorInput{}, fmt.Errorf(
			"%w: name must contain between 2 and 60 characters",
			ErrInvalidOperator,
		)
	}

	if !operatorCodePattern.MatchString(input.Code) {
		return CreateOperatorInput{}, fmt.Errorf(
			"%w: code must contain 3 to 20 characters (A-Z, 0-9, underscore)",
			ErrInvalidOperator,
		)
	}

	if _, ok := allowedOperatorCurrencies[input.Currency]; !ok {
		return CreateOperatorInput{}, fmt.Errorf(
			"%w: unsupported currency",
			ErrInvalidOperator,
		)
	}

	if input.CallbackURL != "" {
		if err := validateOperatorCallbackURL(input.CallbackURL); err != nil {
			return CreateOperatorInput{}, err
		}
	}

	return input, nil
}

func validateOperatorCallbackURL(raw string) error {
	if strings.Contains(raw, "#") {
		return fmt.Errorf("%w: callbackUrl must not contain a fragment", ErrInvalidOperator)
	}

	if len(raw) > 2048 {
		return fmt.Errorf("%w: callbackUrl is too long", ErrInvalidOperator)
	}

	parsed, err := url.ParseRequestURI(raw)
	if err != nil {
		return fmt.Errorf("%w: callbackUrl is not a valid URL", ErrInvalidOperator)
	}

	if parsed.Scheme != "https" ||
		parsed.Host == "" ||
		parsed.Hostname() == "" ||
		parsed.User != nil ||
		parsed.Fragment != "" {
		return fmt.Errorf(
			"%w: callbackUrl must be an HTTPS URL without user information or fragment",
			ErrInvalidOperator,
		)
	}

	return nil
}

func generateOperatorID() (string, error) {
	var raw [16]byte

	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate operator identifier: %w", err)
	}

	return hex.EncodeToString(raw[:]), nil
}

// CreateOperator valide et enregistre un opérateur dans MongoDB.
// Le code métier est protégé par l'index unique créé par EnsureIndexes.
func (s *Store) CreateOperator(
	ctx context.Context,
	input CreateOperatorInput,
	createdBy string,
) (*Operator, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("MongoDB is not initialized")
	}
	if ctx == nil {
		return nil, errors.New("context is required")
	}

	normalized, err := ValidateOperatorInput(input)
	if err != nil {
		return nil, err
	}

	recordID, err := generateOperatorID()
	if err != nil {
		return nil, fmt.Errorf("generate MongoDB record ID: %w", err)
	}

	operatorID, err := generateOperatorID()
	if err != nil {
		return nil, fmt.Errorf("generate business operator ID: %w", err)
	}

	if recordID == operatorID {
		return nil, errors.New("generated operator identifiers must be distinct")
	}

	now := time.Now().UTC()

	operator := &Operator{
		ID:          recordID,
		OperatorID:  operatorID,
		Name:        normalized.Name,
		Code:        normalized.Code,
		Currency:    normalized.Currency,
		CallbackURL: normalized.CallbackURL,
		Status:      "active",
		CreatedBy:   strings.TrimSpace(createdBy),
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	_, err = s.db.Collection(operatorsCollection).InsertOne(ctx, operator)
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return nil, ErrOperatorConflict
		}
		return nil, fmt.Errorf("insert operator: %w", err)
	}

	return operator, nil
}

// FindOperatorByID recherche un opérateur par son identifiant technique.
func (s *Store) FindOperatorByID(
	ctx context.Context,
	operatorID string,
) (*Operator, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("MongoDB is not initialized")
	}
	if ctx == nil {
		return nil, errors.New("context is required")
	}

	operatorID = strings.TrimSpace(operatorID)
	if operatorID == "" {
		return nil, fmt.Errorf("%w: operator ID is required", ErrInvalidOperator)
	}

	var operator Operator
	err := s.db.Collection(operatorsCollection).
		FindOne(ctx, bson.D{{Key: "operator_id", Value: operatorID}}).
		Decode(&operator)

	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrOperatorNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find operator: %w", err)
	}

	return &operator, nil
}

// FindOperatorByCode recherche un opérateur par son code métier.
func (s *Store) FindOperatorByCode(
	ctx context.Context,
	code string,
) (*Operator, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("MongoDB is not initialized")
	}
	if ctx == nil {
		return nil, errors.New("context is required")
	}

	code = strings.ToUpper(strings.TrimSpace(code))
	if !operatorCodePattern.MatchString(code) {
		return nil, fmt.Errorf("%w: invalid operator code", ErrInvalidOperator)
	}

	var operator Operator
	err := s.db.Collection(operatorsCollection).
		FindOne(ctx, bson.D{{Key: "code", Value: code}}).
		Decode(&operator)

	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrOperatorNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find operator by code: %w", err)
	}

	return &operator, nil
}

// ListOperators retourne les opérateurs les plus récemment créés.
// Une limite absente ou invalide est remplacée par la limite par défaut.
func (s *Store) ListOperators(
	ctx context.Context,
	limit int64,
) ([]Operator, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("MongoDB is not initialized")
	}
	if ctx == nil {
		return nil, errors.New("context is required")
	}

	if limit <= 0 {
		limit = DefaultOperatorListLimit
	}
	if limit > MaxOperatorListLimit {
		limit = MaxOperatorListLimit
	}

	findOptions := options.Find().
		SetLimit(limit).
		SetSort(bson.D{
			{Key: "created_at", Value: -1},
			{Key: "_id", Value: 1},
		})

	cursor, err := s.db.Collection(operatorsCollection).Find(
		ctx,
		bson.D{},
		findOptions,
	)
	if err != nil {
		return nil, fmt.Errorf("list operators: %w", err)
	}
	defer cursor.Close(ctx)

	operators := make([]Operator, 0)
	if err := cursor.All(ctx, &operators); err != nil {
		return nil, fmt.Errorf("decode operators: %w", err)
	}

	// La base effectue déjà le tri ; ce tri secondaire garantit un résultat
	// déterministe si les dates sont identiques dans des données historiques.
	sort.SliceStable(operators, func(i, j int) bool {
		if operators[i].CreatedAt.Equal(operators[j].CreatedAt) {
			return operators[i].ID < operators[j].ID
		}
		return operators[i].CreatedAt.After(operators[j].CreatedAt)
	})

	return operators, nil
}
