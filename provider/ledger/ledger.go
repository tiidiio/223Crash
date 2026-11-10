// Package ledger implémente le Ledger et l'Idempotency Store du 223GAMING Provider Core.
//
// Ce package est l'UNIQUE autorité financière canonique : aucun Game Product ne possède sa
// propre implémentation.
//
// Ce composant NE remplace PAS le wallet de l'opérateur et NE détient AUCUN fonds joueur.
// Il enregistre, de façon durable et idempotente, les mouvements financiers que le Provider
// initie vers le wallet externe de l'opérateur (DEBIT/CREDIT/ROLLBACK), pour permettre :
//   - l'idempotency (un retry avec la même IdempotencyKey, pour le même opérateur, renvoie
//     l'entrée existante au lieu d'en créer une seconde) ;
//   - l'audit (chaque mouvement reste tracé, y compris après confirmation) ;
//   - la réconciliation avec le wallet de l'opérateur ;
//   - le Provider Recovery (détection des opérations restées PENDING après un crash process).
//
// L'idempotence est strictement scoped par opérateur :
//
//	UNIQUE(operatorId, idempotencyKey)
//
// GameID reste une valeur opaque, jamais interprétée ici. Aucune règle de jeu n'est connue
// de ce package.
//
// Organisation : ce fichier contient la logique métier (validation, états, idempotency). Tout
// accès base passe par l'interface `store` ; l'implémentation MongoDB vit dans mongo.go. Les
// tests unitaires valident la logique via un faux store ; les propriétés propres à MongoDB
// (index unique, atomicité de FindOneAndUpdate, curseurs) sont couvertes par les tests
// d'intégration (MONGO_TEST_URI).
package ledger

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/tiidiio/223Gaming/provider"
)

// Types d'identité canoniques du Provider Core — définis une seule fois dans
// internal/provider et réutilisés ici par alias (type X = provider.X), pas par redéfinition.
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
	// OU son résultat est resté indéterminé (timeout, 5xx). Une entrée PENDING n'est pas une
	// dette perdue : son issue opérationnelle n'a pas encore été déterminée — le
	// Recovery/Reconciliation la traite.
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

// Erreurs de contrat entre Ledger et store — non exportées : un store traduit ses erreurs
// natives (clé dupliquée, document absent) vers ces sentinelles, le Ledger les traduit vers
// l'API publique. Toute autre erreur du store est une erreur d'infrastructure.
var (
	errNotFound  = errors.New("ledger: store: document absent")
	errDuplicate = errors.New("ledger: store: conflit idempotence opérateur/clé")
)

// NewEntry regroupe les paramètres nécessaires à l'ouverture d'une entrée de Ledger.
//
// ParentTransactionID n'est utilisé que pour Type == EntryRollback : il référence le
// TransactionID de l'EntryDebit d'origine. Un ROLLBACK ouvre toujours sa PROPRE entrée avec
// son propre TransactionID — on ne réécrit jamais le sens d'une transaction existante.
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
// à l'appelant.
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

// transitionSpec décrit une transition PENDING → CONFIRMED|FAILED à appliquer atomiquement.
type transitionSpec struct {
	to      EntryStatus
	at      time.Time
	balance *int64 // CONFIRMED uniquement, optionnel
	reason  string // FAILED uniquement
}

// indexDef décrit un index du Ledger indépendamment du driver (champs triés ascendants).
type indexDef struct {
	label  string
	name   string
	fields []string
	unique bool
}

// idempotencyIndexName est le nom explicite de l'index composé d'idempotence. Il sert aussi
// de fallback de classification des erreurs DuplicateKey (voir mongo.go).
const idempotencyIndexName = "operatorId_1_idempotencyKey_1"

// indexDefs : l'unicité de l'idempotence est limitée à un opérateur et garantie au niveau
// base de données, pas seulement applicatif (voir Begin). transactionId reste globalement
// unique.
var indexDefs = []indexDef{
	{
		label:  "operatorId/idempotencyKey",
		name:   idempotencyIndexName,
		fields: []string{"operatorId", "idempotencyKey"},
		unique: true,
	},
	{
		label:  "transactionId",
		name:   "transactionId_1",
		fields: []string{"transactionId"},
		unique: true,
	},
	{
		label:  "status/createdAt",
		name:   "status_1_createdAt_1",
		fields: []string{"status", "createdAt"},
	},
	{
		label:  "parentTransactionId",
		name:   "parentTransactionId_1",
		fields: []string{"parentTransactionId"},
	},
}

// store est l'accès minimal au stockage dont le Ledger a besoin. Contrat :
//   - insert renvoie errDuplicate UNIQUEMENT pour une collision sur (operatorId,
//     idempotencyKey) ; toute autre collision (ex. transactionId) reste une erreur
//     d'infrastructure distincte ;
//   - byIdempotencyKey recherche dans ce même périmètre ;
//   - byTransactionID recherche par identifiant global ;
//   - by*/resolvePending renvoient errNotFound si aucun document ne correspond ;
//   - resolvePending n'applique la transition QUE si l'entrée est encore PENDING (atomique) ;
//   - toute autre erreur est une erreur d'infrastructure.
type store interface {
	insert(ctx context.Context, e Entry) error
	byIdempotencyKey(ctx context.Context, operatorID OperatorID, key IdempotencyKey) (*Entry, error)
	byTransactionID(ctx context.Context, id TransactionID) (*Entry, error)
	resolvePending(ctx context.Context, id TransactionID, t transitionSpec) (*Entry, error)
	pendingBefore(ctx context.Context, before time.Time) ([]Entry, error)
	ensureIndex(ctx context.Context, d indexDef) error
}

// Ledger est le point d'accès unique au Ledger + Idempotency Store du Provider Core.
//
// col est conservé pour compatibilité (tests d'intégration, outillage) ; la logique métier
// n'utilise que st.
type Ledger struct {
	col *mongo.Collection
	st  store
}

const collectionName = "provider_ledger_entries"

// ensureIndexes crée tous les index requis ; s'arrête à la première erreur.
func ensureIndexes(ctx context.Context, st store) error {
	for _, d := range indexDefs {
		if err := st.ensureIndex(ctx, d); err != nil {
			return fmt.Errorf("ledger: création index %s: %w", d.label, err)
		}
	}
	return nil
}

// randRead est le point d'injection de la source d'aléa (tests uniquement).
var randRead = rand.Read

func newTransactionID() (TransactionID, error) {
	b := make([]byte, 12)
	if _, err := randRead(b); err != nil {
		return "", fmt.Errorf("ledger: génération transaction id: %w", err)
	}
	return TransactionID("txn_" + hex.EncodeToString(b)), nil
}

// Begin ouvre une nouvelle entrée PENDING, ou renvoie l'entrée existante si
// (OperatorID, IdempotencyKey) a déjà été utilisé — un retry (réseau, applicatif) avec la même
// clé pour le même opérateur ne crée jamais de second mouvement financier, il retrouve
// toujours le premier. Deux opérateurs distincts peuvent utiliser la même IdempotencyKey sans
// collision.
//
// Begin doit être appelé et confirmé en base AVANT tout appel réseau vers le wallet opérateur.
//
// Le bool retourné (created) est le signal de possession de l'opération, garanti atomique par
// l'index unique MongoDB (operatorId, idempotencyKey) — jamais une simple vérification
// applicative "if exists { return }" qui laisserait une fenêtre de course :
//   - created == true  : cet appel a réellement inséré l'entrée. L'appelant est seul responsable
//     d'exécuter l'opération financière associée (appel wallet) et de résoudre l'entrée via
//     Confirm/Fail.
//   - created == false : une entrée existait déjà pour ce couple. L'appelant NE DOIT JAMAIS
//     relancer l'opération financière : il doit lire Entry.Status et agir en conséquence
//     (résultat déjà connu si CONFIRMED/FAILED, attendre la résolution si PENDING).
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

	err = l.st.insert(ctx, entry)
	if err == nil {
		return &entry, true, nil
	}

	if errors.Is(err, errDuplicate) {
		existing, getErr := l.GetByIdempotencyKey(ctx, e.OperatorID, e.IdempotencyKey)
		if getErr != nil {
			return nil, false, fmt.Errorf(
				"ledger: begin: conflit idempotence mais relecture échouée: %w",
				getErr,
			)
		}
		if existing.OperatorID != e.OperatorID || existing.IdempotencyKey != e.IdempotencyKey {
			return nil, false, fmt.Errorf(
				"ledger: begin: incohérence de portée après conflit d'idempotence",
			)
		}
		return existing, false, nil
	}

	return nil, false, fmt.Errorf("ledger: begin: insertion: %w", err)
}

// Confirm marque une entrée PENDING comme CONFIRMED, avec le solde opérateur post-opération
// si le wallet le renvoie (optionnel — un solde illisible doit être passé à nil, jamais à 0).
// Idempotent : appeler Confirm deux fois avec le même TransactionID renvoie la même entrée sans
// erreur. Appeler Confirm sur une entrée déjà FAILED renvoie ErrStateConflict — signal réel
// d'incohérence que le Recovery/Reconciliation doit traiter explicitement, jamais en silence.
func (l *Ledger) Confirm(ctx context.Context, txnID TransactionID, operatorBalanceAfterMinor *int64) (*Entry, error) {
	return l.transition(ctx, txnID, transitionSpec{
		to:      StatusConfirmed,
		at:      time.Now(),
		balance: operatorBalanceAfterMinor,
	})
}

// Fail marque une entrée PENDING comme FAILED avec une raison. Idempotent au même titre que
// Confirm. Ne supprime jamais l'entrée.
//
// N'appeler Fail QUE sur un rejet certain. Un résultat indéterminé (timeout, 5xx) doit laisser
// l'entrée PENDING — jamais Fail.
func (l *Ledger) Fail(ctx context.Context, txnID TransactionID, reason string) (*Entry, error) {
	return l.transition(ctx, txnID, transitionSpec{
		to:     StatusFailed,
		at:     time.Now(),
		reason: reason,
	})
}

func (l *Ledger) transition(ctx context.Context, txnID TransactionID, t transitionSpec) (*Entry, error) {
	entry, err := l.st.resolvePending(ctx, txnID, t)
	if err == nil {
		return entry, nil
	}
	if !errors.Is(err, errNotFound) {
		return nil, fmt.Errorf("ledger: transition %s vers %s: %w", txnID, t.to, err)
	}

	// Aucune entrée PENDING ne correspond : soit elle n'existe pas, soit elle est déjà résolue.
	existing, getErr := l.GetByTransactionID(ctx, txnID)
	if getErr != nil {
		// Seul ErrEntryNotFound signifie "entrée inexistante". Une erreur d'infrastructure
		// pendant la relecture reste une erreur d'infrastructure.
		return nil, fmt.Errorf("ledger: transition %s vers %s: %w", txnID, t.to, getErr)
	}
	if existing.Status == t.to {
		return existing, nil
	}
	return nil, fmt.Errorf(
		"ledger: transition %s vers %s (actuel=%s): %w",
		txnID, t.to, existing.Status, ErrStateConflict,
	)
}

// GetByIdempotencyKey recherche une entrée dans le périmètre exact de l'opérateur.
func (l *Ledger) GetByIdempotencyKey(
	ctx context.Context,
	operatorID OperatorID,
	key IdempotencyKey,
) (*Entry, error) {
	if operatorID == "" {
		return nil, ErrOperatorIDRequired
	}
	if key == "" {
		return nil, ErrIdempotencyKeyRequired
	}

	entry, err := l.st.byIdempotencyKey(ctx, operatorID, key)
	if errors.Is(err, errNotFound) {
		return nil, ErrEntryNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("ledger: get by operator/idempotency key: %w", err)
	}
	return entry, nil
}

// GetByTransactionID retrouve une entrée par son TransactionID (globalement unique).
func (l *Ledger) GetByTransactionID(ctx context.Context, txnID TransactionID) (*Entry, error) {
	entry, err := l.st.byTransactionID(ctx, txnID)
	if errors.Is(err, errNotFound) {
		return nil, ErrEntryNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("ledger: get by transaction id: %w", err)
	}
	return entry, nil
}

// ListPendingOlderThan retourne les entrées PENDING créées avant `before` — point d'entrée du
// Provider Recovery pour détecter les opérations restées en suspens après un crash process.
func (l *Ledger) ListPendingOlderThan(ctx context.Context, before time.Time) ([]Entry, error) {
	entries, err := l.st.pendingBefore(ctx, before)
	if err != nil {
		return nil, fmt.Errorf("ledger: list pending: %w", err)
	}
	return entries, nil
}
