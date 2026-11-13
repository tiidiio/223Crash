package v1

import "context"

type PanelID int

const (
	PanelA PanelID = 0
	PanelB PanelID = 1
)

type RoundState int

const (
	StateBettingOpen RoundState = iota
	StateRunning
	StateCrashed
)

type BetState int

const (
	BetPending BetState = iota
	BetCashedOut
	BetLost
)

type RoundView struct {
	ID              string     `json:"id"`
	State           RoundState `json:"state"`
	BettingOpenedAt int64      `json:"bettingOpenedAt"` // ms unix
}

type BetView struct {
	ID            string   `json:"id"`
	UserID        string   `json:"userId"`
	Panel         PanelID  `json:"panel"`
	Amount        int64    `json:"amount"`
	Payout        int64    `json:"payout"`
	State         BetState `json:"state"`
	RoundID       string   `json:"roundId"`
	CashoutTarget float64  `json:"cashoutTarget"`
}

type EngineEventType string

const (
	EventBettingOpen  EngineEventType = "BETTING_OPEN"
	EventRoundRunning EngineEventType = "ROUND_RUNNING"
	EventTick         EngineEventType = "TICK"
	EventCrash        EngineEventType = "CRASH"
)

type EngineEvent struct {
	Type           EngineEventType `json:"type"`
	RoundID        string          `json:"roundId"`
	ServerSeedHash string          `json:"serverSeedHash,omitempty"`
	ServerSeed     string          `json:"serverSeed,omitempty"`
	ClientSeed     string          `json:"clientSeed,omitempty"`
	Multiplier     float64         `json:"multiplier,omitempty"`
	CrashPoint     float64         `json:"crashPoint,omitempty"`
	Nonce          int64           `json:"nonce,omitempty"`
	TimestampMs    int64           `json:"timestampMs"`
	LostBets       []BetView       `json:"lostBets,omitempty"`
	ElapsedMs      int64           `json:"elapsedMs,omitempty"`
}

type GameEngine interface {
	CurrentRound() (*RoundView, error)
	CurrentFairness() (hash string, clientSeed string, nonce int64)
	PlaceBet(userID string, panel PanelID, amountMinor int64, cashoutTarget float64) (*BetView, error)
	GetPanelBet(userID string, panel PanelID) (BetView, bool, error)
	Cashout(betID string) (BetView, error)
	UserBets(userID string) []BetView
	Run(ctx context.Context) error
	Events() <-chan EngineEvent
}
