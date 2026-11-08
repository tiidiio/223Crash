// Package betstore persiste durablement les paris de 223CRASH dans MongoDB.
//
// La mémoire du moteur (game.BetManager) reste la source des calculs temps réel, mais n'est plus
// l'unique source de vérité : chaque pari accepté a un document durable, clé = TransactionID du
// DEBIT (globalement unique, contrairement à l'ID moteur "bet-N" qui repart de 0 à chaque
// processus). Les informations financières détaillées restent dans le Ledger ; ce document ne
// garde que les références (TransactionID) et l'état métier du pari.
//
// Toutes les transitions sont conditionnelles à l'état courant :
//
//	PENDING -> CASHED_OUT | LOST | RECONCILING
//
// CASHED_OUT, LOST et RECONCILING n'ont aucune transition sortante automatique. RECONCILING
// signifie "issue du pari inconnue" : ni crédit ni remboursement automatiques, intervention de
// réconciliation requise.
package betstore

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type State string

const (
	StatePending     State = "PENDING"
	StateCashedOut   State = "CASHED_OUT"
	StateLost        State = "LOST"
	StateReconciling State = "RECONCILING"
)

var (
	ErrNotFound      = errors.New("betstore: pari introuvable")
	ErrStateConflict = errors.New("betstore: transition en conflit avec l'état actuel")
	ErrInvalid       = errors.New("betstore: paramètre invalide")
)

// Bet est le document durable d'un pari. Les montants sont en unités mineures (int64).
type Bet struct {
	DebitTransactionID    string `bson:"debitTransactionId"`
	CreditTransactionID   string `bson:"creditTransactionId,omitempty"`
	RollbackTransactionID string `bson:"rollbackTransactionId,omitempty"`

	ClientBetID string `bson:"clientBetId"`
	EngineBetID string `bson:"engineBetId,omitempty"` // local au processus, informatif uniquement
	OperatorID  string `bson:"operatorId"`
	PlayerID    string `bson:"playerId"`
	GameID      string `bson:"gameId"`
	RoundID     string `bson:"roundId"`
	Panel       uint8  `bson:"panel"`

	AmountMinor   int64   `bson:"amountMinor"`
	CashoutTarget float64 `bson:"cashoutTarget"`

	State             State   `bson:"state"`
	CashoutMultiplier float64 `bson:"cashoutMultiplier,omitempty"`
	PayoutMinor       int64   `bson:"payoutMinor,omitempty"`
	ReconcileReason   string  `bson:"reconcileReason,omitempty"`

	InstanceID string    `bson:"instanceId"`
	CreatedAt  time.Time `bson:"createdAt"`
	UpdatedAt  time.Time `bson:"updatedAt"`
	CashoutAt  time.Time `bson:"cashoutAt,omitempty"`
	Version    int64     `bson:"version"`
}

func (b Bet) validate() error {
	switch {
	case b.DebitTransactionID == "":
		return fmt.Errorf("%w: debitTransactionId requis", ErrInvalid)
	case b.ClientBetID == "":
		return fmt.Errorf("%w: clientBetId requis", ErrInvalid)
	case b.OperatorID == "":
		return fmt.Errorf("%w: operatorId requis", ErrInvalid)
	case b.PlayerID == "":
		return fmt.Errorf("%w: playerId requis", ErrInvalid)
	case b.GameID == "":
		return fmt.Errorf("%w: gameId requis", ErrInvalid)
	case b.RoundID == "":
		return fmt.Errorf("%w: roundId requis", ErrInvalid)
	case b.InstanceID == "":
		return fmt.Errorf("%w: instanceId requis", ErrInvalid)
	case b.Panel < 1 || b.Panel > 4:
		return fmt.Errorf("%w: panel hors 1..4", ErrInvalid)
	case b.AmountMinor <= 0:
		return fmt.Errorf("%w: montant doit être > 0", ErrInvalid)
	case b.CashoutTarget < 1:
		return fmt.Errorf("%w: cashoutTarget doit être >= 1", ErrInvalid)
	}
	return nil
}

func checkRef(operatorID, debitTxn string) error {
	if operatorID == "" || debitTxn == "" {
		return fmt.Errorf("%w: operatorId et debitTransactionId requis", ErrInvalid)
	}
	return nil
}

func checkCashout(multiplier float64, payout int64) error {
	if multiplier < 1 || payout <= 0 {
		return fmt.Errorf("%w: multiplicateur >= 1 et gain > 0 requis", ErrInvalid)
	}
	return nil
}

func conflict(txn string, cur State, to State) error {
	return fmt.Errorf("betstore: %s vers %s (actuel=%s): %w", txn, to, cur, ErrStateConflict)
}

// Store est l'accès durable aux paris. Contrat commun à toutes les implémentations :
//   - Insert est idempotent sur debitTransactionId : un second appel renvoie le document existant
//     avec created=false, sans rien modifier ; l'état initial est toujours PENDING ;
//   - toute lecture ou transition est limitée à operatorId (ErrNotFound pour un autre opérateur) ;
//   - une transition n'a lieu que depuis PENDING ; la rejouer à l'identique renvoie le document
//     sans erreur, la rejouer avec d'autres valeurs ou depuis un autre état renvoie
//     ErrStateConflict. Un état terminal n'est jamais réécrit.
type Store interface {
	Insert(ctx context.Context, b Bet) (*Bet, bool, error)
	Get(ctx context.Context, operatorID, debitTxn string) (*Bet, error)
	MarkCashedOut(ctx context.Context, operatorID, debitTxn string, multiplier float64, payoutMinor int64, at time.Time) (*Bet, error)
	MarkLost(ctx context.Context, operatorID, debitTxn string, at time.Time) (*Bet, error)
	MarkReconciling(ctx context.Context, operatorID, debitTxn, reason string) (*Bet, error)
	// SetCreditRef enregistre le TransactionID du CREDIT, une seule fois, sur un pari CASHED_OUT.
	SetCreditRef(ctx context.Context, operatorID, debitTxn, creditTxn string) (*Bet, error)
	// ReconcileOrphans passe en RECONCILING les paris PENDING d'une autre instance (processus
	// précédent) non modifiés depuis olderThan. Renvoie le nombre de paris basculés.
	ReconcileOrphans(ctx context.Context, instanceID string, olderThan time.Time, reason string) (int64, error)
	ListByState(ctx context.Context, state State, limit int) ([]Bet, error)
}
