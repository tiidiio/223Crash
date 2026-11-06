// Package ledger implémente le Ledger et l'Idempotency Store du 223GAMING Provider Core.
//
// Ce composant NE remplace PAS le wallet de l'opérateur et NE détient AUCUN fonds joueur.
// Il enregistre, de façon durable et idempotente, les mouvements financiers que le Provider
// initie vers le wallet externe de l'opérateur (DEBIT/CREDIT/ROLLBACK), pour permettre :
//   - l'idempotency (un retry avec la même IdempotencyKey renvoie l'entrée existante au lieu
//     d'en créer une seconde) ;
//   - l'audit (chaque mouvement reste tracé, y compris après confirmation — contrairement au
//     comportement historique de internal/wallet/queue.go qui supprime le document après succès) ;
//   - la réconciliation future avec le wallet de l'opérateur ;
//   - le Provider Recovery (détection des opérations restées PENDING après un crash process).
//
// Le Ledger est totalement indépendant de tout Game Product : GameID est une valeur opaque,
// jamais interprétée ici. Aucune règle de jeu (multiplicateur, crash point, etc.) n'est connue
// de ce package.
//
// Organisation : ce fichier contient la logique métier (validation, états, idempotency) et ne
// dépend du driver MongoDB que par le type *mongo.Collection conservé dans Ledger. Tout accès
// base passe par l'interface `store` ; l'implémentation MongoDB vit dans mongo.go. Les tests
// unitaires (ledger_unit_test.go) valident la logique via un faux store ; les propriétés propres
// à MongoDB (index unique, atomicité de FindOneAndUpdate, curseurs) restent couvertes par les
// tests d'intégration (MONGO_TEST_URI).
package ledger

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"

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

// Erreurs de contrat entre Ledger et store — non exportées : un store traduit ses erreurs
// natives (clé dupliquée, document absent) vers ces sentinelles, le Ledger les traduit vers
// l'API publique. Toute autre erreur du store est une erreur d'infrastructure, propagée telle
// quelle (enveloppée).
var (
	errNotFound  = errors.New("ledger: store: document absent")
	errDuplicate = errors.New("ledger: store: clé dupliquée")
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
	fields []string
	unique bool
}

// indexDefs : l'index unique sur idempotencyKey fait office de protection anti-doublon au
// niveau base de données, pas seulement applicatif (voir Begin).
var indexDefs = []indexDef{
	{label: "idempotencyKey", fields: []string{"idempotencyKey"}, unique: true},
	{label: "transactionId", fields: []string{"transactionId"}, unique: true},
	{label: "status/createdAt", fields: []string{"status", "createdAt"}},
	{label: "parentTransactionId", fields: []string{"parentTransactionId"}},
}

// store est l'accès minimal au stockage dont le Ledger a besoin. Contrat :
//   - insert renvoie errDuplicate si la clé d'idempotence existe déjà ;
//   - by*/resolvePending renvoient errNotFound si aucun document ne correspond ;
//   - resolvePending n'applique la transition QUE si l'entrée est encore PENDING (atomique) ;
//   - toute autre erreur est une erreur d'infrastructure.
type store interface {
	insert(ctx context.Context, e Entry) error
	byIdempotencyKey(ctx context.Context, key IdempotencyKey) (*Entry, error)
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

// Begin ouvre une nouvelle entrée PENDING, ou renvoie l'entrée existante si IdempotencyKey a
// déjà été utilisée — c'est le mécanisme d'idempotency : un retry (réseau, applicatif) avec la
// même clé ne crée jamais de second mouvement financier, il retrouve toujours le premier.
//
// Begin doit être appelé et confirmé en base AVANT tout appel réseau vers le wallet opérateur —
// c'est ce qui comble le trou de Provider Recovery identifié dans l'audit (DEBIT synchrone sans
// trace préalable dans l'ancien cmd/server/gateway.go.handleBet).
//
// Le bool retourné (created) est le signal de possession de l'opération, garanti atomique par
// l'index unique MongoDB sur idempotencyKey (l'insertion échoue avec une erreur de clé dupliquée
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

	err = l.st.insert(ctx, entry)
	if err == nil {
		return &entry, true, nil
	}
	if errors.Is(err, errDuplicate) {
		existing, getErr := l.GetByIdempotencyKey(ctx, e.IdempotencyKey)
		if getErr != nil {
			return nil, false, fmt.Errorf("ledger: begin: conflit idempotency mais relecture échouée: %w", getErr)
		}
		return existing, false, nil
	}
	return nil, false, fmt.Errorf("ledger: begin: insertion: %w", err)
}

// Confirm marque une entrée PENDING comme CONFIRMED, avec le solde opérateur post-opération
// si le wallet le renvoie (optionnel — certains opérateurs ne le fournissent pas systématique,
// et un solde illisible doit être passé à nil, jamais à 0 : voir wallet.BalanceOrNil).
// Idempotent : appeler Confirm deux fois avec le même TransactionID renvoie la même entrée sans
// erreur. Appeler Confirm sur une entrée déjà FAILED renvoie ErrStateConflict — c'est un signal
// réel d'incohérence (le wallet a confirmé après qu'on l'ait déjà considérée en échec) que le
// futur Provider Recovery/Reconciliation devra traiter explicitement, jamais silencieusement.
func (l *Ledger) Confirm(ctx context.Context, txnID TransactionID, operatorBalanceAfterMinor *int64) (*Entry, error) {
	return l.transition(ctx, txnID, transitionSpec{to: StatusConfirmed, at: time.Now(), balance: operatorBalanceAfterMinor})
}

// Fail marque une entrée PENDING comme FAILED avec une raison. Idempotent au même titre que
// Confirm. Ne supprime jamais l'entrée.
//
// N'appeler Fail QUE sur un rejet certain (voir wallet.OutcomeRejected). Un résultat
// indéterminé (timeout, 5xx) doit laisser l'entrée PENDING — jamais Fail.
func (l *Ledger) Fail(ctx context.Context, txnID TransactionID, reason string) (*Entry, error) {
	return l.transition(ctx, txnID, transitionSpec{to: StatusFailed, at: time.Now(), reason: reason})
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
		// pendant la relecture reste une erreur d'infrastructure : la masquer en ErrEntryNotFound
		// ferait traiter une panne transitoire comme une incohérence définitive par l'appelant.
		return nil, fmt.Errorf("ledger: transition %s vers %s: %w", txnID, t.to, getErr)
	}
	if existing.Status == t.to {
		return existing, nil
	}
	return nil, fmt.Errorf("ledger: transition %s vers %s (actuel=%s): %w", txnID, t.to, existing.Status, ErrStateConflict)
}

// GetByIdempotencyKey retrouve une entrée par sa clé d'idempotency.
func (l *Ledger) GetByIdempotencyKey(ctx context.Context, key IdempotencyKey) (*Entry, error) {
	entry, err := l.st.byIdempotencyKey(ctx, key)
	if errors.Is(err, errNotFound) {
		return nil, ErrEntryNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("ledger: get by idempotency key: %w", err)
	}
	return entry, nil
}

// GetByTransactionID retrouve une entrée par son TransactionID interne.
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

// ListPendingOlderThan retourne les entrées PENDING créées avant `before` — c'est le point
// d'entrée que le Provider Recovery utilise pour détecter les opérations restées en suspens
// après un crash process.
func (l *Ledger) ListPendingOlderThan(ctx context.Context, before time.Time) ([]Entry, error) {
	entries, err := l.st.pendingBefore(ctx, before)
	if err != nil {
		return nil, fmt.Errorf("ledger: list pending: %w", err)
	}
	return entries, nil
}
