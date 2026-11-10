package productadapter

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	v1 "github.com/tiidiio/223Gaming/contracts/product/v1"
)

var (
	ErrSessionNotFound = errors.New("session not found or expired")
	ErrInvalidMode     = errors.New("invalid or unauthorized session mode")
	ErrInsufficientBal = errors.New("insufficient virtual balance")
)

type Session struct {
	SessionID  string
	PlayerID   string
	OperatorID string
	ProductID  string
	Currency   string
	Mode       string
	Balance    int64
	ExpiresAt  time.Time
	IsValid    bool
}

type SessionManager struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

func NewSessionManager() *SessionManager {
	return &SessionManager{
		sessions: make(map[string]*Session),
	}
}

// CreateSession détermine de manière autoritaire le mode à partir du jeton serveur,
// interdisant au client de forcer un mode integration via un jeton demo.
func (sm *SessionManager) CreateSession(req *v1.SessionHandshakeRequest) (*Session, error) {
	token := strings.TrimSpace(req.Token)
	if token == "" {
		return nil, errors.New("empty authentication token")
	}

	// Détermination autoritaire du mode côté serveur
	mode := v1.ModeDemo
	operatorID := "operator_demo_sandbox"
	playerID := fmt.Sprintf("player_demo_%d", time.Now().UnixNano()%10000)
	initialBalance := int64(1000000) // 1,000,000 XOF virtuel pour la démo

	if strings.HasPrefix(token, "operator_token_") || strings.Contains(token, "integration") {
		mode = v1.ModeIntegration
		operatorID = "operator_production_01"
		playerID = "player_production_01"
		initialBalance = 500000 // Solde initial indicatif avant appel wallet
	}

	// Si le client tente explicitement d'envoyer "integration" mais fournit un jeton démo, on rejette ou force en démo.
	// La règle de sécurité exige qu'un jeton démo ne puisse jamais obtenir une session integration.
	if strings.HasPrefix(token, "demo_") || token == "" {
		mode = v1.ModeDemo
	}

	sessionID := fmt.Sprintf("sess_%s_%d", req.ProductID, time.Now().UnixNano())
	now := time.Now()

	sess := &Session{
		SessionID:  sessionID,
		PlayerID:   playerID,
		OperatorID: operatorID,
		ProductID:  req.ProductID,
		Currency:   "XOF",
		Mode:       mode,
		Balance:    initialBalance,
		ExpiresAt:  now.Add(4 * time.Hour),
		IsValid:    true,
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.sessions[sessionID] = sess

	return sess, nil
}

func (sm *SessionManager) GetSession(sessionID string) (*Session, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	sess, exists := sm.sessions[sessionID]
	if !exists || !sess.IsValid || time.Now().After(sess.ExpiresAt) {
		return nil, ErrSessionNotFound
	}
	return sess, nil
}

func (sm *SessionManager) DebitDemoBalance(sessionID string, amount int64) (int64, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sess, exists := sm.sessions[sessionID]
	if !exists || !sess.IsValid || time.Now().After(sess.ExpiresAt) {
		return 0, ErrSessionNotFound
	}

	if sess.Mode != v1.ModeDemo {
		return 0, ErrInvalidMode
	}

	if sess.Balance < amount {
		return 0, ErrInsufficientBal
	}

	sess.Balance -= amount
	return sess.Balance, nil
}

func (sm *SessionManager) CreditDemoBalance(sessionID string, amount int64) (int64, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sess, exists := sm.sessions[sessionID]
	if !exists || !sess.IsValid || time.Now().After(sess.ExpiresAt) {
		return 0, ErrSessionNotFound
	}

	if sess.Mode != v1.ModeDemo {
		return 0, ErrInvalidMode
	}

	sess.Balance += amount
	return sess.Balance, nil
}
