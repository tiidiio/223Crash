package v1

const (
	ErrCodeUnauthorized        = "UNAUTHORIZED_SESSION"
	ErrCodeSessionExpired      = "SESSION_EXPIRED"
	ErrCodeInsufficientFunds   = "INSUFFICIENT_FUNDS"
	ErrCodeInvalidBetAmount    = "INVALID_BET_AMOUNT"
	ErrCodeRoundClosed         = "ROUND_CLOSED"
	ErrCodeIdempotencyConflict = "IDEMPOTENCY_CONFLICT"
	ErrCodeWalletTimeout       = "WALLET_TIMEOUT"
	ErrCodeInternalError       = "INTERNAL_ERROR"
)

type ContractError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}

func (e *ContractError) Error() string {
	return e.Code + ": " + e.Message
}
