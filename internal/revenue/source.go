package revenue

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// ledgerCollection est la collection du Ledger du Provider Core
// (internal/provider/ledger). Constante dupliquée volontairement : revenue ne dépend pas
// du package ledger, uniquement du schéma persisté.
const ledgerCollection = "provider_ledger_entries"

// ErrPendingEntries : la période contient des entrées PENDING (DEBIT, CREDIT ou ROLLBACK).
// Clôturer maintenant figerait des totaux faux : on attend la résolution (Recovery).
var ErrPendingEntries = errors.New("revenue: ledger has PENDING entries in the period")

func bdoc(kv ...any) bson.D {
	out := make(bson.D, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		out = append(out, bson.E{Key: kv[i].(string), Value: kv[i+1]})
	}
	return out
}

type debitAgg struct {
	Stakes    int64 `bson:"stakes"`
	Rollbacks int64 `bson:"rollbacks"`
	Pending   int64 `bson:"pending"`
}

type creditAgg struct {
	Status string `bson:"_id"`
	Sum    int64  `bson:"sum"`
	Count  int64  `bson:"count"`
}

// LedgerTotals agrège mises et gains d'un jour UTC pour un opérateur et un jeu.
//
//	Stakes  = somme des DEBIT CONFIRMED créés dans la période
//	          - somme des ROLLBACK CONFIRMED rattachés (parentTransactionId) à ces DEBIT
//	Payouts = somme des CREDIT CONFIRMED créés dans la période (date de règlement)
//
// Les entrées FAILED sont ignorées. Toute entrée PENDING concernée → ErrPendingEntries.
func (s *Store) LedgerTotals(ctx context.Context, operatorID, gameID string, periodStart time.Time) (Totals, error) {
	operatorID = strings.TrimSpace(operatorID)
	gameID = strings.TrimSpace(gameID)
	if operatorID == "" || gameID == "" {
		return Totals{}, fmt.Errorf("%w: operator ID and game ID are required", ErrInvalidInput)
	}
	if !isUTCMidnight(periodStart) {
		return Totals{}, fmt.Errorf("%w: periodStart must be a UTC midnight", ErrInvalidInput)
	}
	start := periodStart.UTC()
	end := start.Add(Period)
	col := s.db.Collection(ledgerCollection)

	inPeriod := bdoc("$gte", start, "$lt", end)
	isConfirmed := bdoc("$eq", bson.A{"$status", "CONFIRMED"})
	isPending := bdoc("$eq", bson.A{"$status", "PENDING"})

	debitPipeline := mongo.Pipeline{
		bdoc("$match", bdoc("operatorId", operatorID, "gameId", gameID, "type", "DEBIT", "createdAt", inPeriod)),
		bdoc("$lookup", bdoc(
			"from", ledgerCollection,
			"localField", "transactionId",
			"foreignField", "parentTransactionId",
			"as", "rollbacks",
		)),
		bdoc("$project", bdoc(
			"status", 1,
			"amountMinor", 1,
			"rbConfirmed", bdoc("$reduce", bdoc(
				"input", "$rollbacks",
				"initialValue", int64(0),
				"in", bdoc("$cond", bson.A{
					bdoc("$eq", bson.A{"$$this.status", "CONFIRMED"}),
					bdoc("$add", bson.A{"$$value", "$$this.amountMinor"}),
					"$$value",
				}),
			)),
			"rbPending", bdoc("$size", bdoc("$filter", bdoc(
				"input", "$rollbacks",
				"as", "r",
				"cond", bdoc("$eq", bson.A{"$$r.status", "PENDING"}),
			))),
		)),
		bdoc("$group", bdoc(
			"_id", nil,
			"stakes", bdoc("$sum", bdoc("$cond", bson.A{isConfirmed, "$amountMinor", int64(0)})),
			"rollbacks", bdoc("$sum", bdoc("$cond", bson.A{isConfirmed, "$rbConfirmed", int64(0)})),
			"pending", bdoc("$sum", bdoc("$add", bson.A{
				bdoc("$cond", bson.A{isPending, int64(1), int64(0)}),
				"$rbPending",
			})),
		)),
	}

	dc, err := col.Aggregate(ctx, debitPipeline)
	if err != nil {
		return Totals{}, fmt.Errorf("revenue: aggregate debits: %w", err)
	}
	var debits []debitAgg
	if err := dc.All(ctx, &debits); err != nil {
		return Totals{}, fmt.Errorf("revenue: decode debits: %w", err)
	}
	var d debitAgg
	if len(debits) > 0 {
		d = debits[0]
	}

	creditPipeline := mongo.Pipeline{
		bdoc("$match", bdoc("operatorId", operatorID, "gameId", gameID, "type", "CREDIT", "createdAt", inPeriod)),
		bdoc("$group", bdoc(
			"_id", "$status",
			"sum", bdoc("$sum", "$amountMinor"),
			"count", bdoc("$sum", int64(1)),
		)),
	}
	cc, err := col.Aggregate(ctx, creditPipeline)
	if err != nil {
		return Totals{}, fmt.Errorf("revenue: aggregate credits: %w", err)
	}
	var credits []creditAgg
	if err := cc.All(ctx, &credits); err != nil {
		return Totals{}, fmt.Errorf("revenue: decode credits: %w", err)
	}

	var payouts, pendingCredits int64
	for _, c := range credits {
		switch c.Status {
		case "CONFIRMED":
			payouts = c.Sum
		case "PENDING":
			pendingCredits = c.Count
		}
	}

	if d.Pending+pendingCredits > 0 {
		return Totals{}, ErrPendingEntries
	}
	net := d.Stakes - d.Rollbacks
	if net < 0 || payouts < 0 {
		return Totals{}, fmt.Errorf("%w: inconsistent ledger totals (rollbacks exceed stakes)", ErrInvalidInput)
	}
	return Totals{Stakes: net, Payouts: payouts}, nil
}

// ClosePeriodFromLedger agrège le ledger puis clôture la période (journal append-only).
// currency est la devise de l'opérateur (provider_operators.currency) : le ledger ne la
// porte pas, l'appelant la fournit.
func (s *Store) ClosePeriodFromLedger(ctx context.Context, operatorID, gameID, currency string, periodStart time.Time, createdBy string, now time.Time) (*JournalEntry, error) {
	totals, err := s.LedgerTotals(ctx, operatorID, gameID, periodStart)
	if err != nil {
		return nil, err
	}
	return s.ClosePeriod(ctx, ClosePeriodInput{
		OperatorID:  operatorID,
		GameID:      gameID,
		Currency:    currency,
		PeriodStart: periodStart,
		Totals:      totals,
		CreatedBy:   createdBy,
	}, now)
}
