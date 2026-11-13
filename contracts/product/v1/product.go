package v1

import "time"

type ProductRoundState string

const (
	ProductStateIdle    ProductRoundState = "idle"
	ProductStateBetting ProductRoundState = "betting"
	ProductStateFlying  ProductRoundState = "flying"
	ProductStateCrashed ProductRoundState = "crashed"
)

type ProductRound struct {
	ID             string            `json:"roundId"`
	State          ProductRoundState `json:"state"`
	StartedAt      time.Time         `json:"startedAt"`
	CrashPoint     float64           `json:"crashPoint,omitempty"`
	ServerSeedHash string            `json:"serverSeedHash"`
	Nonce          int64             `json:"nonce"`
}

type CrashPoint struct {
	RoundID string  `json:"roundId"`
	Value   float64 `json:"value"`
	Hash    string  `json:"hash"`
	Valid   bool    `json:"valid"`
}

type ProductEvent struct {
	Type    string      `json:"type"`
	RoundID string      `json:"roundId"`
	Data    interface{} `json:"data"`
}
