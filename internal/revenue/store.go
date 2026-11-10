package revenue

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const (
	ratesCollection   = "provider_revenue_rates"
	journalCollection = "provider_revenue_journal"
)

var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

// Store persiste l'historique des taux et le journal de revenus.
// Il n'expose aucune méthode de mise à jour ni de suppression.
type Store struct {
	db *mongo.Database
}

// NewStore construit un Store sur une base MongoDB existante.
func NewStore(db *mongo.Database) (*Store, error) {
	if db == nil {
		return nil, errors.New("revenue: nil database")
	}
	return &Store{db: db}, nil
}

// EnsureIndexes crée les index (idempotent).
func (s *Store) EnsureIndexes(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	_, err := s.db.Collection(ratesCollection).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "operator_id", Value: 1},
			{Key: "effective_from", Value: 1},
		},
		Options: options.Index().SetUnique(true).SetName("idx_revenue_rate_operator_effective_unique"),
	})
	if err != nil {
		return fmt.Errorf("revenue: create rates index: %w", err)
	}

	_, err = s.db.Collection(journalCollection).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "operator_id", Value: 1},
			{Key: "game_id", Value: 1},
			{Key: "currency", Value: 1},
			{Key: "period_start", Value: 1},
		},
		Options: options.Index().SetUnique(true).SetName("idx_revenue_journal_period_unique"),
	})
	if err != nil {
		return fmt.Errorf("revenue: create journal unique index: %w", err)
	}

	_, err = s.db.Collection(journalCollection).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "operator_id", Value: 1},
			{Key: "period_start", Value: -1},
		},
		Options: options.Index().SetName("idx_revenue_journal_operator_period"),
	})
	if err != nil {
		return fmt.Errorf("revenue: create journal reporting index: %w", err)
	}
	return nil
}

func newID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("revenue: generate id: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func isUTCMidnight(t time.Time) bool {
	u := t.UTC()
	return u.Equal(u.Truncate(Period))
}

// SetRateInput : définition d'un taux contractuel pour un opérateur.
type SetRateInput struct {
	OperatorID       string
	Bps              int64
	CarryForwardLoss bool
	EffectiveFrom    time.Time // minuit UTC
	CreatedBy        string
}

// SetRate ajoute un taux à l'historique. Les taux existants ne sont jamais modifiés.
//
// Règles : minuit UTC ; pas avant aujourd'hui (UTC) ; strictement après le dernier
// taux existant ; refusé si une période clôturée se termine après EffectiveFrom.
// Un contrôle concurrent entre la vérification et l'insertion reste possible mais
// inoffensif : une clôture ne porte que sur des jours terminés, alors que
// EffectiveFrom >= aujourd'hui.
func (s *Store) SetRate(ctx context.Context, in SetRateInput, now time.Time) (*Rate, error) {
	in.OperatorID = strings.TrimSpace(in.OperatorID)
	if in.OperatorID == "" {
		return nil, fmt.Errorf("%w: operator ID is required", ErrInvalidInput)
	}
	if in.Bps < 0 || in.Bps > BpsDenominator {
		return nil, fmt.Errorf("%w: rate must be within 0..%d bps", ErrInvalidInput, BpsDenominator)
	}
	if !isUTCMidnight(in.EffectiveFrom) {
		return nil, fmt.Errorf("%w: effectiveFrom must be a UTC midnight", ErrInvalidInput)
	}
	now = now.UTC()
	effective := in.EffectiveFrom.UTC()
	if effective.Before(now.Truncate(Period)) {
		return nil, fmt.Errorf("%w: effectiveFrom is in the past", ErrRetroactive)
	}

	latest, err := s.latestRate(ctx, in.OperatorID)
	if err != nil && !errors.Is(err, ErrNoRate) {
		return nil, err
	}
	if latest != nil && !effective.After(latest.EffectiveFrom) {
		return nil, ErrRateOrder
	}

	closed, err := s.db.Collection(journalCollection).CountDocuments(ctx, bson.D{
		{Key: "operator_id", Value: in.OperatorID},
		{Key: "period_end", Value: bson.D{{Key: "$gt", Value: effective}}},
	}, options.Count().SetLimit(1))
	if err != nil {
		return nil, fmt.Errorf("revenue: check closed periods: %w", err)
	}
	if closed > 0 {
		return nil, ErrRetroactive
	}

	id, err := newID()
	if err != nil {
		return nil, err
	}
	rate := &Rate{
		ID:               id,
		OperatorID:       in.OperatorID,
		Bps:              in.Bps,
		CarryForwardLoss: in.CarryForwardLoss,
		EffectiveFrom:    effective,
		CreatedBy:        strings.TrimSpace(in.CreatedBy),
		CreatedAt:        now,
	}
	if _, err := s.db.Collection(ratesCollection).InsertOne(ctx, rate); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return nil, ErrRateOrder
		}
		return nil, fmt.Errorf("revenue: insert rate: %w", err)
	}
	return rate, nil
}

func (s *Store) latestRate(ctx context.Context, operatorID string) (*Rate, error) {
	var r Rate
	err := s.db.Collection(ratesCollection).
		FindOne(ctx, bson.D{{Key: "operator_id", Value: operatorID}},
			options.FindOne().SetSort(bson.D{{Key: "effective_from", Value: -1}})).
		Decode(&r)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNoRate
	}
	if err != nil {
		return nil, fmt.Errorf("revenue: find latest rate: %w", err)
	}
	return &r, nil
}

// RateAt retourne le taux en vigueur à l'instant donné, ou ErrNoRate.
func (s *Store) RateAt(ctx context.Context, operatorID string, at time.Time) (*Rate, error) {
	operatorID = strings.TrimSpace(operatorID)
	if operatorID == "" {
		return nil, fmt.Errorf("%w: operator ID is required", ErrInvalidInput)
	}
	var r Rate
	err := s.db.Collection(ratesCollection).
		FindOne(ctx, bson.D{
			{Key: "operator_id", Value: operatorID},
			{Key: "effective_from", Value: bson.D{{Key: "$lte", Value: at.UTC()}}},
		}, options.FindOne().SetSort(bson.D{{Key: "effective_from", Value: -1}})).
		Decode(&r)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNoRate
	}
	if err != nil {
		return nil, fmt.Errorf("revenue: find rate: %w", err)
	}
	return &r, nil
}

// ClosePeriodInput : clôture d'un jour UTC pour un opérateur et un jeu.
// Totals provient d'une source de réconciliation (phase suivante) ; ce package
// ne lit aucune collection financière.
type ClosePeriodInput struct {
	OperatorID  string
	GameID      string
	Currency    string
	PeriodStart time.Time // minuit UTC
	Totals      Totals
	CreatedBy   string
}

// ClosePeriod calcule et journalise une période terminée. Idempotent : rejouée
// avec les mêmes totaux, renvoie l'entrée existante ; avec d'autres totaux,
// ErrJournalConflict. Sans taux contractuel : ErrNoRate (aucun revenu inventé).
func (s *Store) ClosePeriod(ctx context.Context, in ClosePeriodInput, now time.Time) (*JournalEntry, error) {
	in.OperatorID = strings.TrimSpace(in.OperatorID)
	in.GameID = strings.TrimSpace(in.GameID)
	in.Currency = strings.ToUpper(strings.TrimSpace(in.Currency))
	if in.OperatorID == "" || in.GameID == "" {
		return nil, fmt.Errorf("%w: operator ID and game ID are required", ErrInvalidInput)
	}
	if !currencyPattern.MatchString(in.Currency) {
		return nil, fmt.Errorf("%w: currency must be a 3-letter code", ErrInvalidInput)
	}
	if !isUTCMidnight(in.PeriodStart) {
		return nil, fmt.Errorf("%w: periodStart must be a UTC midnight", ErrInvalidInput)
	}
	start := in.PeriodStart.UTC()
	end := start.Add(Period)
	now = now.UTC()
	if end.After(now) {
		return nil, ErrPeriodNotEnded
	}

	existing, err := s.findEntry(ctx, in.OperatorID, in.GameID, in.Currency, start)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if existing.Stakes == in.Totals.Stakes && existing.Payouts == in.Totals.Payouts {
			return existing, nil
		}
		return nil, ErrJournalConflict
	}

	rate, err := s.RateAt(ctx, in.OperatorID, start)
	if err != nil {
		return nil, err
	}

	var carryIn int64
	if rate.CarryForwardLoss {
		prior, err := s.priorEntry(ctx, in.OperatorID, in.GameID, in.Currency, start)
		if err != nil {
			return nil, err
		}
		if prior != nil {
			if !prior.PeriodEnd.Equal(start) {
				return nil, ErrPeriodGap
			}
			carryIn = prior.CarryOut
		}
	}

	res, err := Compute(Params{
		Totals:       in.Totals,
		RateBps:      rate.Bps,
		CarryEnabled: rate.CarryForwardLoss,
		CarryIn:      carryIn,
	})
	if err != nil {
		return nil, err
	}

	id, err := newID()
	if err != nil {
		return nil, err
	}
	entry := &JournalEntry{
		ID:               id,
		OperatorID:       in.OperatorID,
		GameID:           in.GameID,
		Currency:         in.Currency,
		PeriodStart:      start,
		PeriodEnd:        end,
		Stakes:           in.Totals.Stakes,
		Payouts:          in.Totals.Payouts,
		GGR:              res.GGR,
		RateID:           rate.ID,
		RateBps:          rate.Bps,
		CarryForwardLoss: rate.CarryForwardLoss,
		CarryIn:          carryIn,
		CarryOut:         res.CarryOut,
		Commission:       res.Commission,
		CreatedBy:        strings.TrimSpace(in.CreatedBy),
		CreatedAt:        now,
	}
	if _, err := s.db.Collection(journalCollection).InsertOne(ctx, entry); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			// Clôture concurrente : relire et appliquer la règle d'idempotence.
			again, ferr := s.findEntry(ctx, in.OperatorID, in.GameID, in.Currency, start)
			if ferr != nil {
				return nil, ferr
			}
			if again != nil && again.Stakes == in.Totals.Stakes && again.Payouts == in.Totals.Payouts {
				return again, nil
			}
			return nil, ErrJournalConflict
		}
		return nil, fmt.Errorf("revenue: insert journal entry: %w", err)
	}
	return entry, nil
}

func (s *Store) findEntry(ctx context.Context, operatorID, gameID, currency string, start time.Time) (*JournalEntry, error) {
	var e JournalEntry
	err := s.db.Collection(journalCollection).FindOne(ctx, bson.D{
		{Key: "operator_id", Value: operatorID},
		{Key: "game_id", Value: gameID},
		{Key: "currency", Value: currency},
		{Key: "period_start", Value: start},
	}).Decode(&e)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("revenue: find journal entry: %w", err)
	}
	return &e, nil
}

func (s *Store) priorEntry(ctx context.Context, operatorID, gameID, currency string, start time.Time) (*JournalEntry, error) {
	var e JournalEntry
	err := s.db.Collection(journalCollection).FindOne(ctx, bson.D{
		{Key: "operator_id", Value: operatorID},
		{Key: "game_id", Value: gameID},
		{Key: "currency", Value: currency},
		{Key: "period_end", Value: bson.D{{Key: "$lte", Value: start}}},
	}, options.FindOne().SetSort(bson.D{{Key: "period_end", Value: -1}})).Decode(&e)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("revenue: find prior journal entry: %w", err)
	}
	return &e, nil
}
