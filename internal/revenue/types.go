// Package revenue implémente le suivi des revenus du fournisseur (GGR et
// commission) de 223GAMING. Il est volontairement isolé : aucune dépendance
// vers les packages de jeu, aucun mélange avec les fonds joueurs.
//
// Règles :
//   - montants et taux en entiers (plus petite unité monétaire, points de base) ;
//   - aucune commission sans taux contractuel explicite ;
//   - journal append-only, jamais de mise à jour ni de suppression ;
//   - un changement de taux n'est jamais rétroactif.
package revenue

import (
	"errors"
	"time"
)

const (
	// BpsDenominator : 10000 points de base = 100 %.
	BpsDenominator int64 = 10000

	// Period : une période de clôture est exactement un jour UTC.
	Period = 24 * time.Hour
)

var (
	ErrInvalidInput    = errors.New("revenue: invalid input")
	ErrNoRate          = errors.New("revenue: no contractual rate for operator")
	ErrRateOrder       = errors.New("revenue: rate must take effect after the latest existing rate")
	ErrRetroactive     = errors.New("revenue: rate change would affect a closed period")
	ErrPeriodNotEnded  = errors.New("revenue: period has not ended yet")
	ErrPeriodGap       = errors.New("revenue: gap between the previous closed period and this one")
	ErrJournalConflict = errors.New("revenue: period already closed with different totals")
	ErrOverflow        = errors.New("revenue: arithmetic overflow")
)

// Totals : agrégats d'une période, en plus petite unité monétaire.
// Stakes = somme des mises débitées ; Payouts = somme des gains crédités.
type Totals struct {
	Stakes  int64 `bson:"stakes" json:"stakes"`
	Payouts int64 `bson:"payouts" json:"payouts"`
}

// Rate : une ligne de l'historique des taux d'un opérateur.
type Rate struct {
	ID               string    `bson:"_id" json:"id"`
	OperatorID       string    `bson:"operator_id" json:"operatorId"`
	Bps              int64     `bson:"bps" json:"bps"`
	CarryForwardLoss bool      `bson:"carry_forward_loss" json:"carryForwardLoss"`
	EffectiveFrom    time.Time `bson:"effective_from" json:"effectiveFrom"`
	CreatedBy        string    `bson:"created_by,omitempty" json:"createdBy,omitempty"`
	CreatedAt        time.Time `bson:"created_at" json:"createdAt"`
}

// JournalEntry : ligne immuable du journal de revenus (une par opérateur,
// jeu, devise et jour UTC clôturé).
type JournalEntry struct {
	ID               string    `bson:"_id" json:"id"`
	OperatorID       string    `bson:"operator_id" json:"operatorId"`
	GameID           string    `bson:"game_id" json:"gameId"`
	Currency         string    `bson:"currency" json:"currency"`
	PeriodStart      time.Time `bson:"period_start" json:"periodStart"`
	PeriodEnd        time.Time `bson:"period_end" json:"periodEnd"`
	Stakes           int64     `bson:"stakes" json:"stakes"`
	Payouts          int64     `bson:"payouts" json:"payouts"`
	GGR              int64     `bson:"ggr" json:"ggr"`
	RateID           string    `bson:"rate_id" json:"rateId"`
	RateBps          int64     `bson:"rate_bps" json:"rateBps"`
	CarryForwardLoss bool      `bson:"carry_forward_loss" json:"carryForwardLoss"`
	CarryIn          int64     `bson:"carry_in" json:"carryIn"`
	CarryOut         int64     `bson:"carry_out" json:"carryOut"`
	Commission       int64     `bson:"commission" json:"commission"`
	CreatedBy        string    `bson:"created_by,omitempty" json:"createdBy,omitempty"`
	CreatedAt        time.Time `bson:"created_at" json:"createdAt"`
}
