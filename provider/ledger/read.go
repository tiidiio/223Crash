package ledger

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const (
	// DefaultListLimit est la taille de page par défaut des lectures administratives.
	DefaultListLimit = 50
	// MaxListLimit borne strictement la taille d'une page.
	MaxListLimit = 200

	maxFilterValueLen = 128
)

var (
	ErrInvalidFilter     = errors.New("ledger: filtre invalide")
	ErrInvalidCursor     = errors.New("ledger: curseur invalide")
	ErrReaderUnavailable = errors.New("ledger: lecteur non initialisé")
)

// Reader est un accès EN LECTURE SEULE à la collection canonique du Ledger.
//
// Ce n'est pas un second ledger : il lit la même collection provider_ledger_entries, ne
// modifie aucune entrée et ne touche à aucun index. Les écritures (Begin/Confirm/Fail)
// restent exclusivement dans Ledger.
type Reader struct {
	col *mongo.Collection
}

// NewReader réutilise la base MongoDB existante (aucune nouvelle connexion).
func NewReader(db *mongo.Database) (*Reader, error) {
	if db == nil {
		return nil, errors.New("ledger: base MongoDB requise")
	}
	return &Reader{col: db.Collection(collectionName)}, nil
}

// ListQuery décrit une lecture filtrée et paginée. Les valeurs vides signifient "pas de filtre".
// From est inclusif, To est exclusif (UTC).
type ListQuery struct {
	OperatorID OperatorID
	PlayerID   PlayerID
	GameID     GameID
	Type       EntryType
	Status     EntryStatus
	From       time.Time
	To         time.Time
	Limit      int
	Cursor     string
}

// ListPage est une page de résultats, du plus récent au plus ancien.
type ListPage struct {
	Entries    []Entry
	NextCursor string
	HasMore    bool
	Limit      int
}

// StatBucket regroupe les entrées par (type, statut).
type StatBucket struct {
	Type            EntryType
	Status          EntryStatus
	Count           int64
	OldestCreatedAt time.Time
}

// Stats est une synthèse de comptage. Aucun montant n'est agrégé : Entry ne porte pas de
// devise, une somme inter-opérateurs serait donc trompeuse.
type Stats struct {
	GeneratedAt  time.Time
	TotalEntries int64
	Buckets      []StatBucket
}

type cursorPayload struct {
	T  int64  `json:"t"`
	ID string `json:"id"`
}

func encodeCursor(e Entry) string {
	raw, _ := json.Marshal(cursorPayload{
		T:  e.CreatedAt.UnixMilli(),
		ID: string(e.TransactionID),
	})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCursor(token string) (cursorPayload, error) {
	var payload cursorPayload

	if len(token) > 512 {
		return payload, ErrInvalidCursor
	}

	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return payload, ErrInvalidCursor
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return payload, ErrInvalidCursor
	}
	if payload.ID == "" || !validFilterValue(payload.ID) || payload.T < 0 {
		return payload, ErrInvalidCursor
	}
	return payload, nil
}

func validFilterValue(s string) bool {
	if len(s) > maxFilterValueLen {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func buildFilter(q ListQuery) (bson.D, error) {
	filter := bson.D{}

	eq := func(field, label, value string) error {
		if value == "" {
			return nil
		}
		if !validFilterValue(value) {
			return fmt.Errorf("%w: %s", ErrInvalidFilter, label)
		}
		filter = append(filter, bson.E{Key: field, Value: value})
		return nil
	}

	if err := eq("operatorId", "operatorId", string(q.OperatorID)); err != nil {
		return nil, err
	}
	if err := eq("playerId", "playerId", string(q.PlayerID)); err != nil {
		return nil, err
	}
	if err := eq("gameId", "gameId", string(q.GameID)); err != nil {
		return nil, err
	}

	switch q.Type {
	case "", EntryDebit, EntryCredit, EntryRollback:
	default:
		return nil, fmt.Errorf("%w: type", ErrInvalidFilter)
	}
	if q.Type != "" {
		filter = append(filter, bson.E{Key: "type", Value: string(q.Type)})
	}

	switch q.Status {
	case "", StatusPending, StatusConfirmed, StatusFailed:
	default:
		return nil, fmt.Errorf("%w: status", ErrInvalidFilter)
	}
	if q.Status != "" {
		filter = append(filter, bson.E{Key: "status", Value: string(q.Status)})
	}

	rng := bson.D{}
	if !q.From.IsZero() {
		rng = append(rng, bson.E{Key: "$gte", Value: q.From.UTC()})
	}
	if !q.To.IsZero() {
		rng = append(rng, bson.E{Key: "$lt", Value: q.To.UTC()})
	}
	if !q.From.IsZero() && !q.To.IsZero() && !q.To.After(q.From) {
		return nil, fmt.Errorf("%w: plage de dates", ErrInvalidFilter)
	}
	if len(rng) > 0 {
		filter = append(filter, bson.E{Key: "createdAt", Value: rng})
	}

	if q.Cursor != "" {
		c, err := decodeCursor(q.Cursor)
		if err != nil {
			return nil, err
		}
		at := time.UnixMilli(c.T).UTC()
		filter = append(filter, bson.E{Key: "$or", Value: bson.A{
			bson.D{{Key: "createdAt", Value: bson.D{{Key: "$lt", Value: at}}}},
			bson.D{
				{Key: "createdAt", Value: at},
				{Key: "transactionId", Value: bson.D{{Key: "$lt", Value: c.ID}}},
			},
		}})
	}

	return filter, nil
}

// List retourne une page d'entrées, triées par (createdAt desc, transactionId desc).
// transactionId est globalement unique : c'est le tie-breaker déterministe du curseur.
func (r *Reader) List(ctx context.Context, q ListQuery) (*ListPage, error) {
	if r == nil || r.col == nil {
		return nil, ErrReaderUnavailable
	}

	limit := q.Limit
	if limit == 0 {
		limit = DefaultListLimit
	}
	if limit < 0 || limit > MaxListLimit {
		return nil, fmt.Errorf("%w: limit", ErrInvalidFilter)
	}

	filter, err := buildFilter(q)
	if err != nil {
		return nil, err
	}

	opts := options.Find().
		SetSort(bson.D{
			{Key: "createdAt", Value: -1},
			{Key: "transactionId", Value: -1},
		}).
		SetLimit(int64(limit) + 1)

	cur, err := r.col.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("ledger: list: %w", err)
	}
	defer cur.Close(ctx)

	entries := make([]Entry, 0, limit+1)
	if err := cur.All(ctx, &entries); err != nil {
		return nil, fmt.Errorf("ledger: list decode: %w", err)
	}

	page := &ListPage{Limit: limit}
	if len(entries) > limit {
		entries = entries[:limit]
		page.HasMore = true
		page.NextCursor = encodeCursor(entries[len(entries)-1])
	}
	page.Entries = entries
	return page, nil
}

// GetByTransactionID lit une entrée par identifiant global.
func (r *Reader) GetByTransactionID(ctx context.Context, id TransactionID) (*Entry, error) {
	if r == nil || r.col == nil {
		return nil, ErrReaderUnavailable
	}
	if id == "" || !validFilterValue(string(id)) {
		return nil, fmt.Errorf("%w: transactionId", ErrInvalidFilter)
	}

	var entry Entry
	err := r.col.FindOne(ctx, bson.D{{Key: "transactionId", Value: string(id)}}).Decode(&entry)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrEntryNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("ledger: get: %w", err)
	}
	return &entry, nil
}

// Ping vérifie que la collection est réellement lisible (une lecture, pas un simple ping
// de connexion). Une collection vide est considérée comme saine.
func (r *Reader) Ping(ctx context.Context) error {
	if r == nil || r.col == nil {
		return ErrReaderUnavailable
	}
	err := r.col.FindOne(
		ctx,
		bson.D{},
		options.FindOne().SetProjection(bson.D{{Key: "_id", Value: 1}}),
	).Err()
	if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
		return fmt.Errorf("ledger: ping: %w", err)
	}
	return nil
}

// Stats compte les entrées par (type, statut). Balayage complet de la collection :
// à surveiller si le volume devient important.
func (r *Reader) Stats(ctx context.Context) (*Stats, error) {
	if r == nil || r.col == nil {
		return nil, ErrReaderUnavailable
	}

	pipeline := mongo.Pipeline{
		bson.D{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: bson.D{
				{Key: "type", Value: "$type"},
				{Key: "status", Value: "$status"},
			}},
			{Key: "count", Value: bson.D{{Key: "$sum", Value: 1}}},
			{Key: "oldest", Value: bson.D{{Key: "$min", Value: "$createdAt"}}},
		}}},
	}

	cur, err := r.col.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("ledger: stats: %w", err)
	}
	defer cur.Close(ctx)

	stats := &Stats{
		GeneratedAt: time.Now().UTC(),
		Buckets:     make([]StatBucket, 0, 9),
	}

	for cur.Next(ctx) {
		var row struct {
			ID struct {
				Type   EntryType   `bson:"type"`
				Status EntryStatus `bson:"status"`
			} `bson:"_id"`
			Count  int64     `bson:"count"`
			Oldest time.Time `bson:"oldest"`
		}
		if err := cur.Decode(&row); err != nil {
			return nil, fmt.Errorf("ledger: stats decode: %w", err)
		}
		stats.Buckets = append(stats.Buckets, StatBucket{
			Type:            row.ID.Type,
			Status:          row.ID.Status,
			Count:           row.Count,
			OldestCreatedAt: row.Oldest.UTC(),
		})
		stats.TotalEntries += row.Count
	}
	if err := cur.Err(); err != nil {
		return nil, fmt.Errorf("ledger: stats cursor: %w", err)
	}

	sort.Slice(stats.Buckets, func(i, j int) bool {
		a, b := stats.Buckets[i], stats.Buckets[j]
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		return a.Status < b.Status
	})

	return stats, nil
}
