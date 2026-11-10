package v1

import "encoding/json"

type EventType string

const (
	EventRoundStarted   EventType = "ROUND_STARTED"
	EventMultiplierTick EventType = "MULTIPLIER_TICK"
	EventRoundCrashed   EventType = "ROUND_CRASHED"
	EventBetPlaced      EventType = "BET_PLACED"
	EventPlayerCashout  EventType = "PLAYER_CASHOUT"
	EventRoundSettled   EventType = "ROUND_SETTLEMENT"
)

type GameEvent struct {
	EventID       string          `json:"event_id"`
	CorrelationID string          `json:"correlation_id"`
	Type          EventType       `json:"type"`
	Timestamp     int64           `json:"timestamp"`
	ProductID     string          `json:"product_id"`
	RoundID       string          `json:"round_id"`
	Payload       json.RawMessage `json:"payload"`
}

type RoundStartedPayload struct {
	RoundID        string `json:"round_id"`
	ServerSeedHash string `json:"server_seed_hash"`
	BetWindowMs    int64  `json:"bet_window_ms"`
}

type MultiplierTickPayload struct {
	RoundID    string  `json:"round_id"`
	Multiplier float64 `json:"multiplier"`
	ElapsedMs  int64   `json:"elapsed_ms"`
}

type RoundCrashedPayload struct {
	RoundID         string  `json:"round_id"`
	CrashMultiplier float64 `json:"crash_multiplier"`
	ServerSeed      string  `json:"server_seed"`
}

type SettlementPayload struct {
	RoundID      string  `json:"round_id"`
	PlayerID     string  `json:"player_id"`
	BetID        string  `json:"bet_id"`
	Status       string  `json:"status"`
	PayoutAmount int64   `json:"payout_amount"`
	Multiplier   float64 `json:"multiplier,omitempty"`
}
