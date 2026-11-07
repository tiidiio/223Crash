package admin

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tiidiio/223Gaming/223Crash/internal/security"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

const (
	sessionCookieName = "223_admin_session"
	csrfCookieName    = "223_admin_csrf"
	csrfHeaderName    = "X-CSRF-Token"
)

var operatorCodePattern = regexp.MustCompile(`^[A-Z0-9_]{3,20}$`)

type Handler struct {
	store *Store
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type createOperatorRequest struct {
	Name        string `json:"name"`
	Code        string `json:"code"`
	Currency    string `json:"currency"`
	CallbackURL string `json:"callbackUrl"`
}

func NewHandler(store *Store) http.HandlerFunc {
	h := &Handler{store: store}
	return h.ServeHTTP
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/admin/login":
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		if !sameOrigin(r) {
			writeError(w, http.StatusForbidden, "origine de requête refusée")
			return
		}
		h.login(w, r)

	case "/api/admin/session":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		h.session(w, r)

	case "/api/admin/logout":
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		if !sameOrigin(r) || !validCSRF(r) {
			writeError(w, http.StatusForbidden, "protection CSRF : requête refusée")
			return
		}
		h.logout(w, r)

	case "/api/admin/operators":
		switch r.Method {
		case http.MethodGet:
			h.requireAdmin(http.HandlerFunc(h.listOperators)).ServeHTTP(w, r)
		case http.MethodPost:
			h.requireAdmin(http.HandlerFunc(h.createOperator)).ServeHTTP(w, r)
		default:
			methodNotAllowed(w)
		}

	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		log.Printf("admin login: store non initialisé")
		writeError(w, http.StatusInternalServerError, "service administrateur indisponible")
		return
	}

	var input loginRequest

	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "requête JSON invalide")
		return
	}

	if strings.TrimSpace(input.Email) == "" || input.Password == "" {
		writeError(w, http.StatusBadRequest, "email et mot de passe requis")
		return
	}

	token, err := h.store.Authenticate(
		r.Context(),
		input.Email,
		input.Password,
	)

	if err != nil {
		if errors.Is(err, ErrInvalidCredentials) {
			writeError(w, http.StatusUnauthorized, "identifiants invalides")
			return
		}

		log.Printf("admin login: erreur interne d'authentification: %v", err)
		writeError(w, http.StatusInternalServerError, "authentification temporairement indisponible")
		return
	}

	csrfToken, err := newCSRFToken()

	if err != nil {
		_ = h.store.DeleteSession(r.Context(), token)
		log.Printf("admin login: génération CSRF impossible: %v", err)
		writeError(w, http.StatusInternalServerError, "initialisation de session impossible")
		return
	}

	secure := secureCookies()

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(sessionDuration.Seconds()),
		Expires:  time.Now().UTC().Add(sessionDuration),
	})

	setCSRFCookie(w, csrfToken, secure)

	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"csrfToken":     csrfToken,
		"admin": map[string]string{
			"email": strings.TrimSpace(strings.ToLower(input.Email)),
		},
	})
}

func (h *Handler) session(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		log.Printf("admin session: store non initialisé")
		writeError(w, http.StatusInternalServerError, "service administrateur indisponible")
		return
	}

	cookie, err := r.Cookie(sessionCookieName)

	if err != nil {
		clearSessionCookies(w)
		writeError(w, http.StatusUnauthorized, "session administrateur absente")
		return
	}

	email, err := h.store.ValidateSession(r.Context(), cookie.Value)

	if err != nil {
		if !handleSessionValidationError(w, err) {
			return
		}

		clearSessionCookies(w)
		writeError(w, http.StatusUnauthorized, "session administrateur invalide ou expirée")
		return
	}

	csrfCookie, err := r.Cookie(csrfCookieName)
	csrfToken := ""

	if err == nil {
		csrfToken = csrfCookie.Value
	}

	if !validCSRFTokenFormat(csrfToken) {
		csrfToken, err = newCSRFToken()

		if err != nil {
			log.Printf("admin session: génération CSRF impossible: %v", err)
			writeError(w, http.StatusInternalServerError, "initialisation CSRF impossible")
			return
		}

		setCSRFCookie(w, csrfToken, secureCookies())
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"csrfToken":     csrfToken,
		"admin": map[string]string{
			"email": email,
		},
	})
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		log.Printf("admin logout: store non initialisé")
		writeError(w, http.StatusInternalServerError, "service administrateur indisponible")
		return
	}

	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		if err := h.store.DeleteSession(r.Context(), cookie.Value); err != nil {
			log.Printf("admin logout: suppression session: %v", err)
			writeError(w, http.StatusInternalServerError, "déconnexion impossible")
			return
		}
	}

	clearSessionCookies(w)
	w.WriteHeader(http.StatusNoContent)
}

// handleSessionValidationError retourne true lorsque l'erreur correspond
// à une session invalide. Les erreurs internes produisent une réponse 500.
func handleSessionValidationError(w http.ResponseWriter, err error) bool {
	if errors.Is(err, ErrInvalidSession) {
		return true
	}

	log.Printf("admin session validation: erreur interne: %v", err)
	writeError(w, http.StatusInternalServerError, "vérification de session temporairement indisponible")

	return false
}

func (h *Handler) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.store == nil {
			log.Printf("admin authorization: store non initialisé")
			writeError(w, http.StatusInternalServerError, "service administrateur indisponible")
			return
		}

		cookie, err := r.Cookie(sessionCookieName)

		if err != nil {
			writeError(w, http.StatusUnauthorized, "authentification administrateur requise")
			return
		}

		if _, err := h.store.ValidateSession(r.Context(), cookie.Value); err != nil {
			if !handleSessionValidationError(w, err) {
				return
			}

			clearSessionCookies(w)
			writeError(w, http.StatusUnauthorized, "session administrateur invalide ou expirée")
			return
		}

		if r.Method != http.MethodGet &&
			r.Method != http.MethodHead &&
			r.Method != http.MethodOptions {

			if !sameOrigin(r) || !validCSRF(r) {
				writeError(w, http.StatusForbidden, "protection CSRF : requête refusée")
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

func (h *Handler) listOperators(w http.ResponseWriter, r *http.Request) {
	operators, err := h.store.ListOperators(r.Context())

	if err != nil {
		log.Printf("admin operators list: %v", err)
		writeError(w, http.StatusInternalServerError, "lecture des opérateurs impossible")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"operators": operators,
	})
}

func (h *Handler) createOperator(w http.ResponseWriter, r *http.Request) {
	var input createOperatorRequest

	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "requête JSON invalide")
		return
	}

	input.Name = strings.TrimSpace(input.Name)
	input.Code = strings.TrimSpace(input.Code)
	input.Currency = strings.ToUpper(strings.TrimSpace(input.Currency))
	input.CallbackURL = strings.TrimSpace(input.CallbackURL)

	if len([]rune(input.Name)) < 3 || len([]rune(input.Name)) > 60 {
		writeError(w, http.StatusBadRequest, "name doit contenir entre 3 et 60 caractères")
		return
	}

	if !operatorCodePattern.MatchString(input.Code) {
		writeError(w, http.StatusBadRequest, "code invalide : format attendu A-Z, 0-9 ou _ (3 à 20 caractères)")
		return
	}

	switch input.Currency {
	case "XOF", "EUR", "USD":
	default:
		writeError(w, http.StatusBadRequest, "currency doit être XOF, EUR ou USD")
		return
	}

	// Validation SSRF : HTTPS obligatoire, résolution DNS et refus
	// des adresses IP privées, locales ou réservées.
	validationCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	if err := security.ValidatePublicHTTPSURL(validationCtx, input.CallbackURL); err != nil {
		writeError(w, http.StatusBadRequest, "callbackUrl doit être une URL HTTPS publique valide")
		return
	}

	operator := Operator{
		Name:        input.Name,
		Code:        input.Code,
		Currency:    input.Currency,
		CallbackURL: input.CallbackURL,
		Status:      "active",
		CreatedAt:   time.Now().UTC(),
	}

	if err := h.store.CreateOperator(r.Context(), operator); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			writeError(w, http.StatusConflict, "ce code opérateur existe déjà")
			return
		}

		log.Printf("admin operator create: %v", err)
		writeError(w, http.StatusInternalServerError, "création de l'opérateur impossible")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"operator": operator,
	})
}

func newCSRFToken() (string, error) {
	raw := make([]byte, 32)

	if _, err := rand.Read(raw); err != nil {
		return "", err
	}

	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func validCSRFTokenFormat(token string) bool {
	if token == "" || len(token) > 128 {
		return false
	}

	raw, err := base64.RawURLEncoding.DecodeString(token)

	return err == nil && len(raw) == 32
}

func validCSRF(r *http.Request) bool {
	cookie, err := r.Cookie(csrfCookieName)

	if err != nil || !validCSRFTokenFormat(cookie.Value) {
		return false
	}

	header := r.Header.Get(csrfHeaderName)

	if !validCSRFTokenFormat(header) {
		return false
	}

	return subtle.ConstantTimeCompare(
		[]byte(cookie.Value),
		[]byte(header),
	) == 1
}

func setCSRFCookie(w http.ResponseWriter, token string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    token,
		Path:     "/api/admin/",
		HttpOnly: false,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(sessionDuration.Seconds()),
		Expires:  time.Now().UTC().Add(sessionDuration),
	})
}

func clearSessionCookies(w http.ResponseWriter) {
	secure := secureCookies()

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
		Expires:  time.Unix(1, 0).UTC(),
	})

	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    "",
		Path:     "/api/admin/",
		HttpOnly: false,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
		Expires:  time.Unix(1, 0).UTC(),
	})
}

func secureCookies() bool {
	return strings.EqualFold(
		strings.TrimSpace(os.Getenv("APP_ENV")),
		"production",
	)
}

func sameOrigin(r *http.Request) bool {
	originHeader := strings.TrimSpace(r.Header.Get("Origin"))

	if originHeader == "" || originHeader == "null" || r.Host == "" {
		return false
	}

	origin, err := parseOrigin(originHeader)

	if err != nil {
		return false
	}

	configured := strings.TrimSpace(os.Getenv("ADMIN_PUBLIC_ORIGIN"))

	var expected *url.URL

	if configured != "" {
		expected, err = parseOrigin(configured)

		if err != nil {
			return false
		}

		requestHost, err := parseOrigin(expected.Scheme + "://" + r.Host)

		if err != nil || !sameOriginURL(requestHost, expected) {
			return false
		}
	} else {
		scheme := "http"

		if r.TLS != nil {
			scheme = "https"
		}

		expected, err = parseOrigin(scheme + "://" + r.Host)

		if err != nil {
			return false
		}
	}

	return sameOriginURL(origin, expected)
}

func sameOriginURL(a, b *url.URL) bool {
	if a == nil || b == nil {
		return false
	}

	return strings.EqualFold(a.Scheme, b.Scheme) &&
		strings.EqualFold(a.Hostname(), b.Hostname()) &&
		effectiveOriginPort(a) == effectiveOriginPort(b)
}

func effectiveOriginPort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}

	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}

	return "80"
}

func parseOrigin(value string) (*url.URL, error) {
	u, err := url.Parse(value)

	if err != nil {
		return nil, err
	}

	if u.Opaque != "" ||
		u.Host == "" ||
		u.User != nil ||
		u.Path != "" ||
		u.RawPath != "" ||
		u.RawQuery != "" ||
		u.ForceQuery ||
		u.Fragment != "" {
		return nil, errors.New("origine invalide")
	}

	if !strings.EqualFold(u.Scheme, "http") &&
		!strings.EqualFold(u.Scheme, "https") {
		return nil, errors.New("schéma interdit")
	}

	if u.Hostname() == "" {
		return nil, errors.New("hôte invalide")
	}

	host := u.Host
	var port string
	var explicitPort bool

	if strings.HasPrefix(host, "[") {
		end := strings.LastIndex(host, "]")

		if end < 0 {
			return nil, errors.New("adresse IPv6 invalide")
		}

		suffix := host[end+1:]

		if suffix != "" {
			if !strings.HasPrefix(suffix, ":") {
				return nil, errors.New("autorité invalide")
			}

			explicitPort = true
			port = suffix[1:]
		}
	} else {
		if strings.Count(host, ":") > 1 {
			return nil, errors.New("adresse IPv6 non encadrée")
		}

		if index := strings.LastIndex(host, ":"); index >= 0 {
			explicitPort = true
			port = host[index+1:]
		}
	}

	if explicitPort {
		if port == "" {
			return nil, errors.New("port vide")
		}

		n, err := strconv.Atoi(port)

		if err != nil || n < 1 || n > 65535 {
			return nil, errors.New("port invalide")
		}
	}

	return u, nil
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(destination); err != nil {
		return err
	}

	var extra any

	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("plusieurs valeurs JSON ou contenu supplémentaire")
	}

	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")

	w.WriteHeader(status)

	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{
		"error": message,
	})
}

func methodNotAllowed(w http.ResponseWriter) {
	w.Header().Set("Allow", "GET, POST")
	writeError(w, http.StatusMethodNotAllowed, "méthode non autorisée")
}
