package v1

type BetRequest struct {
	CorrelationID  string `json:"correlation_id"`
	IdempotencyKey string `json:"idempotency_key"`
	SessionID      string `json:"session_id"`
	PlayerID       string `json:"player_id"`
	OperatorID     string `json:"operator_id"`
	RoundID        string `json:"round_id"`
	Amount         int64  `json:"amount"`
	Currency       string `json:"currency"`
}

type BetResponse struct {
	CorrelationID string `json:"correlation_id"`
	TransactionID string `json:"transaction_id"`
	Status        string `json:"status"`
	NewBalance    int64  `json:"new_balance"`
	ErrorCode     string `json:"error_code,omitempty"`
	ErrorMessage  string `json:"error_message,omitempty"`
}

type CashoutRequest struct {
	CorrelationID  string  `json:"correlation_id"`
	IdempotencyKey string  `json:"idempotency_key"`
	SessionID      string  `json:"session_id"`
	PlayerID       string  `json:"player_id"`
	OperatorID     string  `json:"operator_id"`
	RoundID        string  `json:"round_id"`
	Multiplier     float64 `json:"multiplier"`
	BetID          string  `json:"bet_id"`
}

type CashoutResponse struct {
	CorrelationID string `json:"correlation_id"`
	TransactionID string `json:"transaction_id"`
	Status        string `json:"status"`
	PayoutAmount  int64  `json:"payout_amount"`
	NewBalance    int64  `json:"new_balance"`
	ErrorCode     string `json:"error_code,omitempty"`
	ErrorMessage  string `json:"error_message,omitempty"`
}
