package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/tiidiio/223Crash/internal/adminauth"
)

const adminCookieName = "223gaming_admin_session"

// Le login enchaîne argon2id + plusieurs écritures MongoDB distantes :
// il dispose d'un budget plus large que les requêtes de lecture.
const loginTimeout = 20 * time.Second

type adminHTTP struct {
	auth         *adminauth.Service
	cookieSecure bool
	limiter      *loginLimiter
}

type loginLimiter struct {
	mu      sync.Mutex
	entries map[string]loginAttempt
}

type loginAttempt struct {
	count   int
	resetAt time.Time
}

type adminContextKey struct{}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func newAdminHTTP(auth *adminauth.Service) *adminHTTP {
	return &adminHTTP{
		auth:         auth,
		cookieSecure: strings.EqualFold(os.Getenv("ADMIN_COOKIE_SECURE"), "true"),
		limiter: &loginLimiter{
			entries: make(map[string]loginAttempt),
		},
	}
}

func (h *adminHTTP) login(w http.ResponseWriter, r *http.Request) {
	if h.auth == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": "authentication_unavailable",
		})
		return
	}

	if !validOrigin(r) {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error": "invalid_origin",
		})
		return
	}

	if os.Getenv("ADMIN_ENV") == "production" && !h.cookieSecure {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": "secure_cookie_required",
		})
		return
	}

	ip := clientIP(r)

	if !h.limiter.allow(ip, time.Now()) {
		w.Header().Set("Retry-After", "900")
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error": "too_many_login_attempts",
		})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	defer r.Body.Close()

	var input loginRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "invalid_request",
		})
		return
	}

	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "invalid_request",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), loginTimeout)
	defer cancel()

	started := time.Now()

	result, err := h.auth.Login(
		ctx,
		input.Email,
		input.Password,
		ip,
		r.UserAgent(),
	)
	if err != nil {
		if errors.Is(err, adminauth.ErrInvalidCredentials) {
			h.limiter.recordFailure(ip, time.Now())
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"error":   "invalid_credentials",
				"message": "Identifiants invalides.",
			})
			return
		}

		log.Printf(
			"admin login failed: %v (ctx=%v, elapsed=%dms)",
			err,
			ctx.Err(),
			time.Since(started).Milliseconds(),
		)

		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": "authentication_failed",
		})
		return
	}

	log.Printf("admin login ok (elapsed=%dms)", time.Since(started).Milliseconds())

	h.limiter.reset(ip)

	http.SetCookie(w, &http.Cookie{
		Name:     adminCookieName,
		Value:    result.Token,
		Path:     "/api/admin",
		Expires:  result.ExpiresAt,
		MaxAge:   int(time.Until(result.ExpiresAt).Seconds()),
		HttpOnly: true,
		Secure:   h.cookieSecure,
		SameSite: http.SameSiteStrictMode,
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"csrfToken":     result.CSRFToken,
		"expiresAt":     result.ExpiresAt,
		"admin":         result.Admin,
	})
}

func (h *adminHTTP) me(w http.ResponseWriter, r *http.Request) {
	authenticated, err := h.authenticateRequest(r)
	if err != nil {
		clearAdminCookie(w, h.cookieSecure)
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"authenticated": false,
			"error":         "unauthenticated",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"csrfToken":     authenticated.CSRFToken,
		"admin":         authenticated.Admin,
	})
}

func (h *adminHTTP) logout(w http.ResponseWriter, r *http.Request) {
	authenticated, ok := r.Context().Value(adminContextKey{}).(*adminauth.AuthenticatedAdmin)
	if !ok || authenticated == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"error": "unauthenticated",
		})
		return
	}

	cookie, err := r.Cookie(adminCookieName)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"error": "invalid_session",
		})
		return
	}

	ctx, cancel := requestContext(r)
	defer cancel()

	err = h.auth.Logout(
		ctx,
		cookie.Value,
		clientIP(r),
		r.UserAgent(),
	)

	clearAdminCookie(w, h.cookieSecure)

	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error":   "logout_audit_failed",
			"message": "La session a été révoquée, mais l'audit doit être vérifié.",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": false,
	})
}

func (h *adminHTTP) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.auth == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"error": "authentication_unavailable",
			})
			return
		}

		authenticated, err := h.authenticateRequest(r)
		if err != nil {
			clearAdminCookie(w, h.cookieSecure)
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"error": "unauthenticated",
			})
			return
		}

		if r.Method != http.MethodGet &&
			r.Method != http.MethodHead &&
			r.Method != http.MethodOptions {

			if !validOrigin(r) {
				writeJSON(w, http.StatusForbidden, map[string]any{
					"error": "invalid_origin",
				})
				return
			}

			supplied := r.Header.Get("X-CSRF-Token")
			if !adminauth.VerifyCSRFToken(authenticated.CSRFToken, supplied) {
				writeJSON(w, http.StatusForbidden, map[string]any{
					"error": "csrf_validation_failed",
				})
				return
			}
		}

		ctx := contextWithAdmin(r, authenticated)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (h *adminHTTP) authenticateRequest(
	r *http.Request,
) (*adminauth.AuthenticatedAdmin, error) {
	if h.auth == nil {
		return nil, adminauth.ErrInvalidSession
	}

	cookie, err := r.Cookie(adminCookieName)
	if err != nil {
		return nil, adminauth.ErrInvalidSession
	}

	ctx, cancel := requestContext(r)
	defer cancel()

	return h.auth.AuthenticateSession(ctx, cookie.Value)
}

func (l *loginLimiter) allow(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	for key, attempt := range l.entries {
		if !now.Before(attempt.resetAt) {
			delete(l.entries, key)
		}
	}

	attempt, exists := l.entries[ip]
	if !exists {
		return len(l.entries) < 10000
	}

	return attempt.count < 5
}

func (l *loginLimiter) recordFailure(ip string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.entries == nil {
		l.entries = make(map[string]loginAttempt)
	}

	for key, attempt := range l.entries {
		if !now.Before(attempt.resetAt) {
			delete(l.entries, key)
		}
	}

	attempt, exists := l.entries[ip]
	if !exists {
		if len(l.entries) >= 10000 {
			return
		}
		attempt = loginAttempt{resetAt: now.Add(15 * time.Minute)}
	}

	if attempt.count < 5 {
		attempt.count++
	}
	l.entries[ip] = attempt
}

func (l *loginLimiter) reset(ip string) {
	l.mu.Lock()
	delete(l.entries, ip)
	l.mu.Unlock()
}

func validOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}

	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}

	if parsed.User != nil ||
		parsed.Host == "" ||
		parsed.Path != "" ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return false
	}

	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}

	return strings.EqualFold(parsed.Host, r.Host)
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}

	return "unknown"
}

func requestContext(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 5*time.Second)
}

func contextWithAdmin(
	r *http.Request,
	admin *adminauth.AuthenticatedAdmin,
) context.Context {
	return context.WithValue(r.Context(), adminContextKey{}, admin)
}

func clearAdminCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     adminCookieName,
		Value:    "",
		Path:     "/api/admin",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
}
