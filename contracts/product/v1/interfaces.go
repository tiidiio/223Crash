package v1

import "context"

type ProductAdapter interface {
	ValidateSession(ctx context.Context, req *SessionHandshakeRequest) (*SessionHandshakeResponse, error)
	ProcessBet(ctx context.Context, req *BetRequest) (*BetResponse, error)
	ProcessCashout(ctx context.Context, req *CashoutRequest) (*CashoutResponse, error)
}
