package productadapter

import (
	"context"
	"fmt"
	"time"

	v1 "github.com/tiidiio/223Crash/contracts/product/v1"
	"github.com/tiidiio/223Crash/internal/gamecatalog"
)

// DefaultProductAdapter implémente v1.ProductAdapter avec support des modes Demo et Integration.
type DefaultProductAdapter struct {
	sessions *SessionManager
}

func NewDefaultProductAdapter() *DefaultProductAdapter {
	return &DefaultProductAdapter{
		sessions: NewSessionManager(),
	}
}

// GetSessionManager permet aux tests d'accéder au gestionnaire de sessions.
func (a *DefaultProductAdapter) GetSessionManager() *SessionManager {
	return a.sessions
}

func (a *DefaultProductAdapter) ValidateSession(ctx context.Context, req *v1.SessionHandshakeRequest) (*v1.SessionHandshakeResponse, error) {
	if req.ProductID != "223crash" {
		return &v1.SessionHandshakeResponse{
			IsValid:  false,
			ErrorMsg: "unknown product id",
		}, nil
	}

	games := gamecatalog.List()
	found := false
	for _, g := range games {
		if g.ID == req.ProductID {
			found = true
			break
		}
	}

	if !found {
		return &v1.SessionHandshakeResponse{
			IsValid:  false,
			ErrorMsg: "product not found in catalog",
		}, nil
	}

	// Création sécurisée et autoritaire de la session via le SessionManager
	sess, err := a.sessions.CreateSession(req)
	if err != nil {
		return &v1.SessionHandshakeResponse{
			IsValid:  false,
			ErrorMsg: err.Error(),
		}, nil
	}

	return &v1.SessionHandshakeResponse{
		SessionID:  sess.SessionID,
		PlayerID:   sess.PlayerID,
		OperatorID: sess.OperatorID,
		Currency:   sess.Currency,
		Balance:    sess.Balance,
		Mode:       sess.Mode,
		ExpiresAt:  sess.ExpiresAt,
		IsValid:    true,
	}, nil
}

func (a *DefaultProductAdapter) ProcessBet(ctx context.Context, req *v1.BetRequest) (*v1.BetResponse, error) {
	if req.Amount <= 0 {
		return &v1.BetResponse{
			CorrelationID: req.CorrelationID,
			Status:        "REJECTED",
			ErrorCode:     v1.ErrCodeInvalidBetAmount,
			ErrorMessage:  "bet amount must be greater than zero",
		}, nil
	}

	if req.IdempotencyKey == "" {
		return &v1.BetResponse{
			CorrelationID: req.CorrelationID,
			Status:        "REJECTED",
			ErrorCode:     v1.ErrCodeIdempotencyConflict,
			ErrorMessage:  "missing idempotency key",
		}, nil
	}

	// Récupération de la session pour déterminer son mode réel (autorité serveur)
	sess, err := a.sessions.GetSession(req.SessionID)
	if err != nil {
		return &v1.BetResponse{
			CorrelationID: req.CorrelationID,
			Status:        "REJECTED",
			ErrorCode:     v1.ErrCodeSessionExpired,
			ErrorMessage:  "session not found, expired, or invalid",
		}, nil
	}

	// Branchement selon le mode
	if sess.Mode == v1.ModeDemo {
		newBal, debitErr := a.sessions.DebitDemoBalance(req.SessionID, req.Amount)
		if debitErr != nil {
			return &v1.BetResponse{
				CorrelationID: req.CorrelationID,
				Status:        "REJECTED",
				ErrorCode:     v1.ErrCodeInsufficientFunds,
				ErrorMessage:  debitErr.Error(),
			}, nil
		}

		txID := fmt.Sprintf("tx_demo_debit_%d", time.Now().UnixNano())
		return &v1.BetResponse{
			CorrelationID: req.CorrelationID,
			TransactionID: txID,
			Status:        "SUCCESS",
			NewBalance:    newBal,
		}, nil
	}

	// Mode INTEGRATION : Dépendance Wallet Gateway externe requise
	// Ne jamais simuler un succès financier si le Wallet Gateway réel n'est pas connecté.
	return &v1.BetResponse{
		CorrelationID: req.CorrelationID,
		Status:        "REJECTED",
		ErrorCode:     v1.ErrCodeInternalError,
		ErrorMessage:  "integration mode error: operator wallet gateway is not connected or configured",
	}, nil
}

func (a *DefaultProductAdapter) ProcessCashout(ctx context.Context, req *v1.CashoutRequest) (*v1.CashoutResponse, error) {
	if req.Multiplier <= 1.0 {
		return &v1.CashoutResponse{
			CorrelationID: req.CorrelationID,
			Status:        "REJECTED",
			ErrorCode:     v1.ErrCodeInternalError,
			ErrorMessage:  "cashout multiplier must be strictly greater than 1.0",
		}, nil
	}

	// Récupération de la session
	sess, err := a.sessions.GetSession(req.SessionID)
	if err != nil {
		return &v1.CashoutResponse{
			CorrelationID: req.CorrelationID,
			Status:        "REJECTED",
			ErrorCode:     v1.ErrCodeInternalError,
			ErrorMessage:  "session not found or expired for cashout",
		}, nil
	}

	estimatedOriginalBet := int64(10000)
	payoutAmount := int64(float64(estimatedOriginalBet) * req.Multiplier)

	if sess.Mode == v1.ModeDemo {
		newBal, creditErr := a.sessions.CreditDemoBalance(req.SessionID, payoutAmount)
		if creditErr != nil {
			return &v1.CashoutResponse{
				CorrelationID: req.CorrelationID,
				Status:        "REJECTED",
				ErrorCode:     v1.ErrCodeInternalError,
				ErrorMessage:  creditErr.Error(),
			}, nil
		}

		txID := fmt.Sprintf("tx_demo_credit_%d", time.Now().UnixNano())
		return &v1.CashoutResponse{
			CorrelationID: req.CorrelationID,
			TransactionID: txID,
			Status:        "SUCCESS",
			PayoutAmount:  payoutAmount,
			NewBalance:    newBal,
		}, nil
	}

	// Mode INTEGRATION : Wallet Gateway requis
	return &v1.CashoutResponse{
		CorrelationID: req.CorrelationID,
		Status:        "REJECTED",
		ErrorCode:     v1.ErrCodeInternalError,
		ErrorMessage:  "integration mode error: operator wallet gateway is not connected or configured",
	}, nil
}
