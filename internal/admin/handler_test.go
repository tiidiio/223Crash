package admin

import (
	"github.com/tiidiio/223Crash/internal/security"

	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSameOrigin(t *testing.T) {
	t.Setenv("ADMIN_PUBLIC_ORIGIN", "")
	t.Setenv("APP_ENV", "development")

	tests := []struct {
		name       string
		requestURL string
		origin     string
		https      bool
		want       bool
	}{
		{
			name:       "HTTP même origine",
			requestURL: "http://provider.example/api/admin/login",
			origin:     "http://provider.example",
			want:       true,
		},
		{
			name:       "HTTPS même origine",
			requestURL: "https://provider.example/api/admin/login",
			origin:     "https://provider.example",
			https:      true,
			want:       true,
		},
		{
			name:       "hôte différent",
			requestURL: "http://provider.example/api/admin/login",
			origin:     "http://attacker.example",
			want:       false,
		},
		{
			name:       "schéma différent",
			requestURL: "https://provider.example/api/admin/login",
			origin:     "http://provider.example",
			https:      true,
			want:       false,
		},
		{
			name:       "origine absente",
			requestURL: "http://provider.example/api/admin/login",
			origin:     "",
			want:       false,
		},
		{
			name:       "origine null",
			requestURL: "http://provider.example/api/admin/login",
			origin:     "null",
			want:       false,
		},
		{
			name:       "origine avec chemin",
			requestURL: "http://provider.example/api/admin/login",
			origin:     "http://provider.example/evil",
			want:       false,
		},
		{
			name:       "origine avec identifiants",
			requestURL: "http://provider.example/api/admin/login",
			origin:     "http://user:password@provider.example",
			want:       false,
		},
		{
			name:       "origine avec query",
			requestURL: "http://provider.example/api/admin/login",
			origin:     "http://provider.example?x=1",
			want:       false,
		},
		{
			name:       "origine avec fragment",
			requestURL: "http://provider.example/api/admin/login",
			origin:     "http://provider.example#fragment",
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", tt.requestURL, nil)
			req.Header.Set("Origin", tt.origin)

			if tt.https {
				req.TLS = &tls.ConnectionState{}
			}

			if got := sameOrigin(req); got != tt.want {
				t.Fatalf("sameOrigin() = %v, attendu %v", got, tt.want)
			}
		})
	}
}

func TestSameOriginWithConfiguredPublicOrigin(t *testing.T) {
	t.Setenv("ADMIN_PUBLIC_ORIGIN", "https://console.example.com")
	t.Setenv("APP_ENV", "production")

	req := httptest.NewRequest(
		"POST",
		"http://console.example.com/api/admin/login",
		nil,
	)
	req.Host = "console.example.com"
	req.Header.Set("Origin", "https://console.example.com")

	if !sameOrigin(req) {
		t.Fatal("l'origine publique configurée devrait être acceptée")
	}

	req.Header.Set("Origin", "http://console.example.com")

	if sameOrigin(req) {
		t.Fatal("une origine HTTP ne doit pas correspondre à une origine HTTPS")
	}

	req.Header.Set("Origin", "https://attacker.example")

	if sameOrigin(req) {
		t.Fatal("une origine externe ne doit pas être acceptée")
	}
}

func TestParseOrigin(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{"HTTP valide", "http://provider.example", false},
		{"HTTPS valide", "https://provider.example", false},
		{"schéma interdit", "javascript://provider.example", true},
		{"sans hôte", "https://", true},
		{"chemin interdit", "https://provider.example/path", true},
		{"query interdite", "https://provider.example?x=1", true},
		{"fragment interdit", "https://provider.example#section", true},
		{"identifiants interdits", "https://user:pass@provider.example", true},
		{"origine opaque", "https:provider.example", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := security.ParseOrigin(tt.value)

			if (err != nil) != tt.wantErr {
				t.Fatalf(
					"security.ParseOrigin(%q), erreur = %v, wantErr = %v",
					tt.value,
					err,
					tt.wantErr,
				)
			}
		})
	}
}

func TestSameOriginDefaultPorts(t *testing.T) {
	tests := []struct {
		name   string
		config string
		origin string
		want   bool
	}{
		{
			name:   "HTTPS port 443 implicite",
			config: "https://console.example.com:443",
			origin: "https://console.example.com",
			want:   true,
		},
		{
			name:   "HTTP port 80 implicite",
			config: "http://console.example.com:80",
			origin: "http://console.example.com",
			want:   true,
		},
		{
			name:   "port HTTPS différent",
			config: "https://console.example.com:8443",
			origin: "https://console.example.com",
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ADMIN_PUBLIC_ORIGIN", tt.config)

			req := httptest.NewRequest(
				"POST",
				"http://console.example.com/api/admin/login",
				nil,
			)
			req.Host = "console.example.com"

			if tt.config == "https://console.example.com:8443" {
				req.Host += ":8443"
			}

			req.Header.Set("Origin", tt.origin)

			if got := sameOrigin(req); got != tt.want {
				t.Fatalf("sameOrigin() = %v, attendu %v", got, tt.want)
			}
		})
	}
}

func TestParseOriginRejectsInvalidPorts(t *testing.T) {
	tests := []string{
		"https://provider.example:99999",
		"https://provider.example:65536",
		"https://provider.example:abc",
		"https://provider.example:",
	}

	for _, value := range tests {
		t.Run(value, func(t *testing.T) {
			if _, err := security.ParseOrigin(value); err == nil {
				t.Fatalf("security.ParseOrigin(%q) aurait dû rejeter ce port", value)
			}
		})
	}
}

func TestHandleSessionValidationError(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantResult bool
		wantStatus int
	}{
		{
			name:       "session invalide",
			err:        ErrInvalidSession,
			wantResult: true,
			wantStatus: http.StatusOK,
		},
		{
			name:       "session expirée",
			err:        errors.Join(ErrInvalidSession, errors.New("expired")),
			wantResult: true,
			wantStatus: http.StatusOK,
		},
		{
			name:       "erreur MongoDB",
			err:        errors.New("connection refused"),
			wantResult: false,
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()

			got := handleSessionValidationError(recorder, tt.err)

			if got != tt.wantResult {
				t.Fatalf(
					"handleSessionValidationError() = %v, attendu %v",
					got,
					tt.wantResult,
				)
			}

			if recorder.Code != tt.wantStatus {
				t.Fatalf(
					"statut HTTP = %d, attendu %d",
					recorder.Code,
					tt.wantStatus,
				)
			}
		})
	}
}

func TestValidCSRFTokenFormat(t *testing.T) {
	valid, err := newCSRFToken()
	if err != nil {
		t.Fatalf("newCSRFToken(): %v", err)
	}

	if !validCSRFTokenFormat(valid) {
		t.Fatal("un token CSRF valide a été rejeté")
	}

	invalidTokens := []string{
		"",
		"invalid",
		"123456",
		"!",
	}

	for _, token := range invalidTokens {
		if validCSRFTokenFormat(token) {
			t.Errorf("token CSRF invalide accepté: %q", token)
		}
	}
}
