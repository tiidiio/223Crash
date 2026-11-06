package mongobridge

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

type BroadcastMessage struct {
	Type        string `bson:"type"`
	Data        bson.M `bson:"data"`
	EmittedAtMs int64  `bson:"emittedAtMs"`
}

type PlayerEvent struct {
	PlayerID    string `bson:"playerId"`
	Type        string `bson:"type"`
	Data        bson.M `bson:"data"`
	EmittedAtMs int64  `bson:"emittedAtMs"`
}

type Publisher struct {
	store *Store
}

func NewPublisher(store *Store) *Publisher {
	return &Publisher{store: store}
}

func nowMs() int64 {
	return time.Now().UnixMilli()
}

func (p *Publisher) PublishTick(ctx context.Context, roundID string, multiplier float64, elapsedMs int64) error {
	_, err := p.store.Broadcast.InsertOne(ctx, BroadcastMessage{
		Type: "TICK",
		Data: bson.M{
			"roundId":    roundID,
			"multiplier": multiplier,
			"elapsedMs":  elapsedMs,
		},
		EmittedAtMs: nowMs(),
	})
	return err
}

// PublishCrash reveals serverSeed, clientSeed and nonce for public provably-fair
// verification (GET /verify/{roundId} recalcule CrashPoint(serverSeed, clientSeed, nonce)
// et compare au crashPoint publié) — only call this once the round has actually crashed.
// clientSeed et nonce ne sont pas secrets ; seul serverSeed devait rester caché jusqu'ici.
func (p *Publisher) PublishCrash(ctx context.Context, roundID string, crashPoint float64, serverSeed, clientSeed string, nonce int64) error {
	_, err := p.store.Broadcast.InsertOne(ctx, BroadcastMessage{
		Type: "CRASH",
		Data: bson.M{
			"roundId":    roundID,
			"crashPoint": crashPoint,
			"serverSeed": serverSeed,
			"clientSeed": clientSeed,
			"nonce":      nonce,
		},
		EmittedAtMs: nowMs(),
	})
	return err
}

func (p *Publisher) PublishRoundStart(ctx context.Context, roundID, serverSeedHash string, startsAt time.Time) error {
	_, err := p.store.Broadcast.InsertOne(ctx, BroadcastMessage{
		Type: "ROUND_START",
		Data: bson.M{
			"roundId":        roundID,
			"serverSeedHash": serverSeedHash,
			"startsAt":       startsAt,
		},
		EmittedAtMs: nowMs(),
	})
	return err
}

// PublishPlayerEvent is dropped silently by the gateway if playerID has no
// open socket — caller doesn't need to check for delivery.
func (p *Publisher) PublishPlayerEvent(ctx context.Context, playerID, eventType string, data bson.M) error {
	_, err := p.store.Events.InsertOne(ctx, PlayerEvent{
		PlayerID:    playerID,
		Type:        eventType,
		Data:        data,
		EmittedAtMs: nowMs(),
	})
	return err
}
