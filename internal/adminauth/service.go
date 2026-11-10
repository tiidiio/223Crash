package adminauth

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/tiidiio/223Gaming/internal/adminstore"
)

const SessionDuration = 12 * time.Hour

var (
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrInvalidSession     = errors.New("invalid or expired administrative session")
)

type Service struct {
	store *adminstore.Store
	now   func() time.Time
}

type LoginResult struct {
	Token     string
	CSRFToken string
	ExpiresAt time.Time
	Admin     *adminstore.Admin
}

type AuthenticatedAdmin struct {
	Admin     *adminstore.Admin
	Session   *adminstore.AdminSession
	CSRFToken string
}

func NewService(store *adminstore.Store) (*Service, error) {
	if store == nil {
		return nil, errors.New("administrative persistence is unavailable")
	}

	return &Service{store: store, now: time.Now}, nil
}

func (s *Service) Login(
	ctx context.Context,
	email string,
	password string,
	ipAddress string,
	userAgent string,
) (*LoginResult, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("authentication service unavailable")
	}

	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || password == "" || len(email) > 254 {
		return nil, ErrInvalidCredentials
	}

	admin, err := s.store.FindAdminByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, adminstore.ErrNotFound) {
			return nil, ErrInvalidCredentials
		}
		return nil, errors.New("authentication persistence unavailable")
	}

	if admin.Status != "active" {
		return nil, ErrInvalidCredentials
	}

	valid, err := VerifyPassword(password, admin.PasswordHash)
	if err != nil || !valid {
		return nil, ErrInvalidCredentials
	}

	now := s.now().UTC()

	token, err := GenerateToken()
	if err != nil {
		return nil, errors.New("cannot generate administrative session")
	}

	csrfToken, err := GenerateCSRFToken()
	if err != nil {
		return nil, errors.New("cannot generate CSRF token")
	}

	sessionID, err := GenerateToken()
	if err != nil {
		return nil, errors.New("cannot generate session identifier")
	}

	expiresAt := now.Add(SessionDuration)
	tokenHash := HashToken(token)

	session := &adminstore.AdminSession{
		ID:         sessionID,
		AdminID:    admin.ID,
		Role:       admin.Role,
		OperatorID: admin.OperatorID,
		TokenHash:  tokenHash,
		CSRFToken:  csrfToken,
		CreatedAt:  now,
		ExpiresAt:  expiresAt,
		LastSeenAt: now,
	}

	if err := s.store.InsertSession(ctx, session); err != nil {
		return nil, errors.New("cannot establish administrative session")
	}

	if err := s.store.UpdateAdminLastLogin(ctx, admin.ID, now); err != nil {
		_ = s.store.DeleteSession(ctx, tokenHash)
		return nil, errors.New("cannot finalize administrative login")
	}

	if err := s.writeAudit(ctx, admin, "login_success", ipAddress, userAgent); err != nil {
		_ = s.store.DeleteSession(ctx, tokenHash)
		return nil, errors.New("cannot record administrative login")
	}

	admin.PasswordHash = ""

	return &LoginResult{
		Token:     token,
		CSRFToken: csrfToken,
		ExpiresAt: expiresAt,
		Admin:     admin,
	}, nil
}

func (s *Service) AuthenticateSession(
	ctx context.Context,
	token string,
) (*AuthenticatedAdmin, error) {
	if s == nil || s.store == nil || len(token) < 32 || len(token) > 128 {
		return nil, ErrInvalidSession
	}

	now := s.now().UTC()
	tokenHash := HashToken(token)

	session, err := s.store.FindSession(ctx, tokenHash, now)
	if err != nil {
		return nil, ErrInvalidSession
	}

	admin, err := s.store.FindAdminByID(ctx, session.AdminID)
	if err != nil {
		_ = s.store.DeleteSession(ctx, tokenHash)
		return nil, ErrInvalidSession
	}

	if admin.Status != "active" {
		_ = s.store.DeleteSession(ctx, tokenHash)
		return nil, ErrInvalidSession
	}

	if err := s.store.UpdateSessionLastSeen(ctx, tokenHash, now); err != nil {
		return nil, errors.New("cannot validate administrative session")
	}

	session.Role = admin.Role
	session.OperatorID = admin.OperatorID
	admin.PasswordHash = ""

	return &AuthenticatedAdmin{
		Admin:     admin,
		Session:   session,
		CSRFToken: session.CSRFToken,
	}, nil
}

func (s *Service) Logout(
	ctx context.Context,
	token string,
	ipAddress string,
	userAgent string,
) error {
	if s == nil || s.store == nil || len(token) < 32 || len(token) > 128 {
		return ErrInvalidSession
	}

	tokenHash := HashToken(token)

	session, err := s.store.FindSession(ctx, tokenHash, s.now().UTC())
	if err != nil {
		return ErrInvalidSession
	}

	admin, err := s.store.FindAdminByID(ctx, session.AdminID)
	if err != nil {
		return ErrInvalidSession
	}

	if err := s.store.DeleteSession(ctx, tokenHash); err != nil {
		return errors.New("cannot revoke administrative session")
	}

	if err := s.writeAudit(ctx, admin, "logout", ipAddress, userAgent); err != nil {
		return errors.New("session revoked but logout audit could not be recorded")
	}

	return nil
}

func (s *Service) RevokeAllSessions(
	ctx context.Context,
	adminID string,
) error {
	if s == nil || s.store == nil || adminID == "" {
		return errors.New("invalid session revocation request")
	}

	return s.store.DeleteSessionsByAdminID(ctx, adminID)
}

func (s *Service) writeAudit(
	ctx context.Context,
	admin *adminstore.Admin,
	event string,
	ipAddress string,
	userAgent string,
) error {
	if admin == nil {
		return errors.New("missing administrator audit identity")
	}

	id, err := GenerateToken()
	if err != nil {
		return errors.New("cannot generate audit identifier")
	}

	if len(ipAddress) > 64 {
		ipAddress = ipAddress[:64]
	}
	if len(userAgent) > 512 {
		userAgent = userAgent[:512]
	}

	audit := &adminstore.AdminAudit{
		ID:        id,
		AdminID:   admin.ID,
		Email:     admin.Email,
		Event:     event,
		IPAddress: ipAddress,
		UserAgent: userAgent,
		CreatedAt: s.now().UTC(),
	}

	if err := s.store.InsertAudit(ctx, audit); err != nil {
		return errors.New("cannot persist administrative audit event")
	}

	return nil
}
