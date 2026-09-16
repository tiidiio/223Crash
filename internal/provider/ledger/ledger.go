// Package ledger implémente le Ledger et l'Idempotency Store du 223GAMING Provider Core.
//
// Ce composant NE remplace PAS le wallet de l'opérateur et NE détient AUCUN fonds joueur.
// Il enregistre, de façon durable et idempotente, les mouvements financiers que le Provider
// initie vers le wallet externe de l'opérateur (DEBIT/CREDIT/ROLLBACK), pour permettre :
//   - l'idempotency (un retry avec la même IdempotencyKey renvoie l'entrée existante au lieu
//     d'en créer une seconde) ;
//   - l'audit (chaque mouvement reste tracé, y compris après confirmation — contrairement au
//     comportement actuel de internal/wallet/queue.go qui supprime le document après succès) ;
//   - la réconciliation future avec le wallet de l'opérateur ;
//   - le Provider Recovery (détection des opérations restées PENDING après un crash process).
//
// Le Ledger est totalement indépendant de tout Game Product : GameID est une valeur opaque,
// jamais interprétée ici. Aucune règle de jeu (multiplicateur, crash point, etc.) n'est connue
// de ce package.
package ledger

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/tiidiio/223Gaming/223Crash/internal/provider"
)

// Types d'identité canoniques du Provider Core — définis une seule fois dans
// internal/provider et réutilisés ici par alias (type X = provider.X), pas par redéfinition,
// pour que ledger.GameID et provider.GameID restent rigoureusement le même type. Voir
// internal/provider/types.go pour la documentation de chaque type.
type (
	GameID         = provider.GameID
	OperatorID     = provider.OperatorID
	PlayerID       = provider.PlayerID
	ExternalRef    = provider.ExternalRef
	IdempotencyKey = provider.IdempotencyKey
	TransactionID  = provider.TransactionID
)

// EntryType est le sens du mouvement financier vis-à-vis du wallet opérateur.
type EntryType string

const (
	EntryDebit    EntryType = "DEBIT"
	EntryCredit   EntryType = "CREDIT"
	EntryRollback EntryType = "ROLLBACK"
)

// EntryStatus est l'état d'une entrée de Ledger dans son cycle de vie.
type EntryStatus string

const (
	// StatusPending : l'entrée existe, l'appel au wallet opérateur n'a pas encore été confirmé,
	// OU son résultat est resté indéterminé (timeout, 5xx — voir wallet.Outcome). Une entrée
	// PENDING n'est pas une dette perdue : c'est une transaction dont l'issue opérationnelle
	// n'a pas encore été déterminée — le futur Recovery/Reconciliation (Tranche 5) la traitera.
	StatusPending EntryStatus = "PENDING"
	// StatusConfirmed : le wallet opérateur a confirmé l'opération sans ambiguïté.
	StatusConfirmed EntryStatus = "CONFIRMED"
	// StatusFailed : l'opération a échoué de façon certaine (rejet métier explicite, pas une
	// simple absence de réponse) — jamais utilisé sur un résultat indéterminé.
	StatusFailed EntryStatus = "FAILED"
)

var (
	ErrIdempotencyKeyRequired      = errors.New("ledger: idempotency key requise")
	ErrGameIDRequired              = errors.New("ledger: game id requis")
	ErrOperatorIDRequired          = errors.New("ledger: operator id requis")
	ErrPlayerIDRequired            = errors.New("ledger: player id requis")
	ErrInvalidAmount               = errors.New("ledger: montant invalide (doit être > 0)")
	ErrInvalidEntryType            = errors.New("ledger: type d'entrée invalide")
	ErrParentTransactionIDRequired = errors.New("ledger: parent transaction id requis pour un ROLLBACK")
	ErrEntryNotFound               = errors.New("ledger: entrée introuvable")
	ErrStateConflict               = errors.New("ledger: transition d'état en conflit avec l'état actuel")
)

// NewEntry regroupe les paramètres nécessaires à l'ouverture d'une entrée de Ledger.
//
// ParentTransactionID n'est utilisé que pour Type == EntryRollback : il référence le
// TransactionID de l'EntryDebit d'origine. Un ROLLBACK ouvre toujours sa PROPRE entrée avec
// son propre TransactionID — on ne réécrit jamais le sens d'une transaction existante, la
// chaîne comptable DEBIT → ROLLBACK reste auditable via ce lien explicite.
type NewEntry struct {
	GameID              GameID
	OperatorID          OperatorID
	PlayerID            PlayerID
	ExternalRef         ExternalRef
	IdempotencyKey      IdempotencyKey
	Type                EntryType
	AmountMinor         int64 // unité mineure (centimes), jamais float64
	ParentTransactionID TransactionID
}

func (e NewEntry) validate() error {
	if e.GameID == "" {
		return ErrGameIDRequired
	}
	if e.OperatorID == "" {
		return ErrOperatorIDRequired
	}
	if e.PlayerID == "" {
		return ErrPlayerIDRequired
	}
	if e.IdempotencyKey == "" {
		return ErrIdempotencyKeyRequired
	}
	if e.AmountMinor <= 0 {
		return ErrInvalidAmount
	}
	switch e.Type {
	case EntryDebit, EntryCredit, EntryRollback:
	default:
		return ErrInvalidEntryType
	}
	if e.Type == EntryRollback && e.ParentTransactionID == "" {
		return ErrParentTransactionIDRequired
	}
	return nil
}

// Entry est une entrée durable du Ledger — à la fois le document Mongo et le type retourné
// à l'appelant (pas de DTO séparé : ce package reste volontairement simple, pas de couche de
// mapping superflue tant qu'aucun besoin réel ne la justifie).
type Entry struct {
	TransactionID             TransactionID  `bson:"transactionId"`
	IdempotencyKey            IdempotencyKey `bson:"idempotencyKey"`
	GameID                    GameID         `bson:"gameId"`
	OperatorID                OperatorID     `bson:"operatorId"`
	PlayerID                  PlayerID       `bson:"playerId"`
	ExternalRef               ExternalRef    `bson:"externalRef,omitempty"`
	ParentTransactionID       TransactionID  `bson:"parentTransactionId,omitempty"`
	Type                      EntryType      `bson:"type"`
	AmountMinor               int64          `bson:"amountMinor"`
	Status                    EntryStatus    `bson:"status"`
	OperatorBalanceAfterMinor *int64         `bson:"operatorBalanceAfterMinor,omitempty"`
	FailureReason             string         `bson:"failureReason,omitempty"`
	CreatedAt                 time.Time      `bson:"createdAt"`
	UpdatedAt                 time.Time      `bson:"updatedAt"`
	ConfirmedAt               time.Time      `bson:"confirmedAt,omitempty"`
	FailedAt                  time.Time      `bson:"failedAt,omitempty"`
}

// Ledger est le point d'accès unique au Ledger + Idempotency Store du Provider Core.
type Ledger struct {
	col *mongo.Collection
}

const collectionName = "provider_ledger_entries"

// New connecte le Ledger à la base Mongo déjà ouverte (réutilise une connexion existante —
// ne pas ouvrir une nouvelle connexion mongo.Connect par composant, contrairement au pattern
// actuel de internal/mongobridge et internal/wallet/queue.go, qui ouvrent chacun la leur) et
// garantit les index nécessaires, dont l'index unique sur idempotencyKey qui fait office de
// protection anti-doublon au niveau base de données, pas seulement applicatif.
func New(ctx context.Context, db *mongo.Database) (*Ledger, error) {
	col := db.Collection(collectionName)

	_, err := col.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "idempotencyKey", Value: 1}},
		Options: options.Index().SetUnique(true),
	})
	if err != nil {
		return nil, fmt.Errorf("ledger: création index idempotencyKey: %w", err)
	}

	_, err = col.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "transactionId", Value: 1}},
		Options: options.Index().SetUnique(true),
	})
	if err != nil {
		return nil, fmt.Errorf("ledger: création index transactionId: %w", err)
	}

	_, err = col.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "status", Value: 1}, {Key: "createdAt", Value: 1}},
	})
	if err != nil {
		return nil, fmt.Errorf("ledger: création index status/createdAt: %w", err)
	}

	_, err = col.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "parentTransactionId", Value: 1}},
	})
	if err != nil {
		return nil, fmt.Errorf("ledger: création index parentTransactionId: %w", err)
	}

	return &Ledger{col: col}, nil
}

func newTransactionID() (TransactionID, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("ledger: génération transaction id: %w", err)
	}
	return TransactionID("txn_" + hex.EncodeToString(b)), nil
}

// Begin ouvre une nouvelle entrée PENDING, ou renvoie l'entrée existante si IdempotencyKey a
// déjà été utilisée — c'est le mécanisme d'idempotency : un retry (réseau, applicatif) avec la
// même clé ne crée jamais de second mouvement financier, il retrouve toujours le premier.
//
// Begin doit être appelé et confirmé en base AVANT tout appel réseau vers le wallet opérateur —
// c'est ce qui comble le trou de Provider Recovery identifié dans l'audit (DEBIT synchrone sans
// trace préalable dans l'ancien cmd/server/gateway.go.handleBet).
//
// Le bool retourné (created) est le signal de possession de l'opération, garanti atomique par
// l'index unique MongoDB sur idempotencyKey (InsertOne échoue avec une erreur de clé dupliquée
// pour tout appelant concurrent perdant la course — jamais une simple vérification applicative
// "if exists { return }" qui laisserait une fenêtre de course) :
//   - created == true  : cet appel a réellement inséré l'entrée. L'appelant est seul responsable
//     d'exécuter l'opération financière associée (appel wallet) et de résoudre l'entrée via
//     Confirm/Fail.
//   - created == false : une entrée existait déjà pour cette IdempotencyKey (créée par cet appel
//     ou par un autre, dans ce process ou un autre). L'appelant NE DOIT JAMAIS relancer
//     l'opération financière : il doit lire Entry.Status et agir en conséquence (résultat déjà
//     connu si CONFIRMED/FAILED, attendre la résolution si PENDING).
func (l *Ledger) Begin(ctx context.Context, e NewEntry) (*Entry, bool, error) {
	if err := e.validate(); err != nil {
		return nil, false, err
	}

	txnID, err := newTransactionID()
	if err != nil {
		return nil, false, err
	}

	now := time.Now()
	entry := Entry{
		TransactionID:       txnID,
		IdempotencyKey:      e.IdempotencyKey,
		GameID:              e.GameID,
		OperatorID:          e.OperatorID,
		PlayerID:            e.PlayerID,
		ExternalRef:         e.ExternalRef,
		ParentTransactionID: e.ParentTransactionID,
		Type:                e.Type,
		AmountMinor:         e.AmountMinor,
		Status:              StatusPending,
		CreatedAt:           now,
		UpdatedAt:           now,
	}

	_, err = l.col.InsertOne(ctx, entry)
	if err == nil {
		return &entry, true, nil
	}
	if mongo.IsDuplicateKeyError(err) {
		existing, getErr := l.GetByIdempotencyKey(ctx, e.IdempotencyKey)
		if getErr != nil {
			return nil, false, fmt.Errorf("ledger: begin: conflit idempotency mais relecture échouée: %w", getErr)
		}
		return existing, false, nil
	}
	return nil, false, fmt.Errorf("ledger: begin: insertion: %w", err)
}

// Confirm marque une entrée PENDING comme CONFIRMED, avec le solde opérateur post-opération
// si le wallet le renvoie (optionnel — certains opérateurs ne le fournissent pas systématique).
// Idempotent : appeler Confirm deux fois avec le même TransactionID renvoie la même entrée sans
// erreur. Appeler Confirm sur une entrée déjà FAILED renvoie ErrStateConflict — c'est un signal
// réel d'incohérence (le wallet a confirmé après qu'on l'ait déjà considérée en échec) que le
// futur Provider Recovery/Reconciliation devra traiter explicitement, jamais silencieusement.
func (l *Ledger) Confirm(ctx context.Context, txnID TransactionID, operatorBalanceAfterMinor *int64) (*Entry, error) {
	return l.transition(ctx, txnID, StatusConfirmed, operatorBalanceAfterMinor, "")
}

// Fail marque une entrée PENDING comme FAILED avec une raison. Idempotent au même titre que
// Confirm. Ne supprime jamais l'entrée — contrairement au comportement actuel de
// internal/wallet/queue.go, qui détruit le document après traitement.
//
// N'appeler Fail QUE sur un rejet certain (voir wallet.OutcomeRejected). Un résultat
// indéterminé (timeout, 5xx) doit laisser l'entrée PENDING — jamais Fail.
func (l *Ledger) Fail(ctx context.Context, txnID TransactionID, reason string) (*Entry, error) {
	return l.transition(ctx, txnID, StatusFailed, nil, reason)
}

func (l *Ledger) transition(ctx context.Context, txnID TransactionID, to EntryStatus, operatorBalanceAfterMinor *int64, reason string) (*Entry, error) {
	now := time.Now()
	set := bson.M{
		"status":    to,
		"updatedAt": now,
	}
	switch to {
	case StatusConfirmed:
		set["confirmedAt"] = now
		if operatorBalanceAfterMinor != nil {
			set["operatorBalanceAfterMinor"] = *operatorBalanceAfterMinor
		}
	case StatusFailed:
		set["failedAt"] = now
		set["failureReason"] = reason
	}

	var entry Entry
	err := l.col.FindOneAndUpdate(
		ctx,
		bson.M{"transactionId": txnID, "status": StatusPending},
		bson.M{"$set": set},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&entry)

	if err == nil {
		return &entry, nil
	}
	if !errors.Is(err, mongo.ErrNoDocuments) {
		return nil, fmt.Errorf("ledger: transition %s vers %s: %w", txnID, to, err)
	}

	existing, getErr := l.GetByTransactionID(ctx, txnID)
	if getErr != nil {
		return nil, fmt.Errorf("ledger: transition %s vers %s: %w", txnID, to, ErrEntryNotFound)
	}
	if existing.Status == to {
		return existing, nil
	}
	return nil, fmt.Errorf("ledger: transition %s vers %s (actuel=%s): %w", txnID, to, existing.Status, ErrStateConflict)
}

// GetByIdempotencyKey retrouve une entrée par sa clé d'idempotency.
func (l *Ledger) GetByIdempotencyKey(ctx context.Context, key IdempotencyKey) (*Entry, error) {
	var entry Entry
	err := l.col.FindOne(ctx, bson.M{"idempotencyKey": key}).Decode(&entry)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrEntryNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("ledger: get by idempotency key: %w", err)
	}
	return &entry, nil
}

// GetByTransactionID retrouve une entrée par son TransactionID interne.
func (l *Ledger) GetByTransactionID(ctx context.Context, txnID TransactionID) (*Entry, error) {
	var entry Entry
	err := l.col.FindOne(ctx, bson.M{"transactionId": txnID}).Decode(&entry)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrEntryNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("ledger: get by transaction id: %w", err)
	}
	return &entry, nil
}

// ListPendingOlderThan retourne les entrées PENDING créées avant `before` — c'est le point
// d'entrée que le futur Provider Recovery (tranche ultérieure) utilisera au boot pour détecter
// les opérations restées en suspens après un crash process. Ajoutée dès cette tranche car c'est
// une simple lecture, pas une logique de recovery en soi (qui reste à construire séparément).
func (l *Ledger) ListPendingOlderThan(ctx context.Context, before time.Time) ([]Entry, error) {
	cur, err := l.col.Find(ctx, bson.M{
		"status":    StatusPending,
		"createdAt": bson.M{"$lt": before},
	})
	if err != nil {
		return nil, fmt.Errorf("ledger: list pending: %w", err)
	}
	defer cur.Close(ctx)

	var entries []Entry
	if err := cur.All(ctx, &entries); err != nil {
		return nil, fmt.Errorf("ledger: list pending: decode: %w", err)
	}
	return entries, nil
}
