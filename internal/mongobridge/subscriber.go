package mongobridge

import (
	"context"
	"log"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type Command struct {
	Type        string   `bson:"type"`
	PlayerID    string   `bson:"playerId"`
	OperatorID  string   `bson:"operatorId"`
	Slot        int      `bson:"slot"`
	Amount      float64  `bson:"amount"`
	AutoCashout *float64 `bson:"autoCashout,omitempty"`
}

type CommandHandlers struct {
	OnJoin     func(ctx context.Context, cmd Command)
	OnPlaceBet func(ctx context.Context, cmd Command)
	OnCashout  func(ctx context.Context, cmd Command)
}

type Subscriber struct {
	store    *Store
	handlers CommandHandlers
}

func NewSubscriber(store *Store, handlers CommandHandlers) *Subscriber {
	return &Subscriber{store: store, handlers: handlers}
}

// Watch blocks, consuming crash_commands via change stream. On any stream
// error (network blip, Atlas failover, cursor timeout) it reconnects with
// exponential backoff (500ms → 30s cap), resetting the backoff each time a
// connection is actually established. Returns only when ctx is cancelled.
func (s *Subscriber) Watch(ctx context.Context) error {
	backoff := 500 * time.Millisecond
	const maxBackoff = 30 * time.Second

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		connected, err := s.watchOnce(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			log.Printf("mongobridge: subscriber stream error (reconnect in %s): %v", backoff, err)
		}

		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}

		if connected {
			backoff = 500 * time.Millisecond
		} else {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

func (s *Subscriber) watchOnce(ctx context.Context) (connected bool, err error) {
	pipeline := mongo.Pipeline{
		bson.D{{Key: "$match", Value: bson.D{{Key: "operationType", Value: "insert"}}}},
	}
	streamOpts := options.ChangeStream().SetFullDocument(options.UpdateLookup)

	stream, err := s.store.Commands.Watch(ctx, pipeline, streamOpts)
	if err != nil {
		return false, err
	}
	defer stream.Close(ctx)
	connected = true

	for stream.Next(ctx) {
		var event struct {
			FullDocument Command `bson:"fullDocument"`
		}
		if err := stream.Decode(&event); err != nil {
			log.Printf("mongobridge: decode command failed: %v", err)
			continue
		}
		s.dispatch(ctx, event.FullDocument)
	}

	return connected, stream.Err()
}

func (s *Subscriber) dispatch(ctx context.Context, cmd Command) {
	switch cmd.Type {
	case "JOIN":
		if s.handlers.OnJoin != nil {
			s.handlers.OnJoin(ctx, cmd)
		}
	case "PLACE_BET":
		if s.handlers.OnPlaceBet != nil {
			s.handlers.OnPlaceBet(ctx, cmd)
		}
	case "CASHOUT":
		if s.handlers.OnCashout != nil {
			s.handlers.OnCashout(ctx, cmd)
		}
	default:
		log.Printf("mongobridge: unknown command type %q", cmd.Type)
	}
}
