package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func newProviderConsoleTestMux(t *testing.T) *http.ServeMux {
	t.Helper()

	mux := http.NewServeMux()
	registerProviderConsole(mux)

	return mux
}

func TestProviderConsoleRedirect(t *testing.T) {
	t.Setenv("PROVIDER_CONSOLE_DIR", t.TempDir())

	mux := newProviderConsoleTestMux(t)

	req := httptest.NewRequest(http.MethodGet, "/provider?source=test", nil)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusPermanentRedirect {
		t.Fatalf(
			"statut incorrect: obtenu %d, attendu %d",
			rec.Code,
			http.StatusPermanentRedirect,
		)
	}

	location := rec.Header().Get("Location")
	if location != "/provider/?source=test" {
		t.Fatalf("destination incorrecte: %q", location)
	}
}

func TestProviderConsoleRejectsUnsupportedMethods(t *testing.T) {
	t.Setenv("PROVIDER_CONSOLE_DIR", t.TempDir())

	mux := newProviderConsoleTestMux(t)

	tests := []struct {
		name   string
		path   string
		method string
	}{
		{
			name:   "POST sur /provider",
			path:   "/provider",
			method: http.MethodPost,
		},
		{
			name:   "PUT sur /provider/",
			path:   "/provider/",
			method: http.MethodPut,
		},
		{
			name:   "DELETE sur un fichier",
			path:   "/provider/index.html",
			method: http.MethodDelete,
		},
		{
			name:   "PATCH sur /provider",
			path:   "/provider",
			method: http.MethodPatch,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			rec := httptest.NewRecorder()

			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf(
					"statut incorrect: obtenu %d, attendu %d",
					rec.Code,
					http.StatusMethodNotAllowed,
				)
			}

			if got := rec.Header().Get("Allow"); got != providerConsoleAllowedMethods {
				t.Fatalf("en-tête Allow incorrect: %q", got)
			}
		})
	}
}

func TestProviderConsoleServesOnlyAllowedFiles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PROVIDER_CONSOLE_DIR", dir)

	files := map[string]string{
		"index.html": "<html>provider</html>",
		"style.css":  "body { margin: 0; }",
		"script.js":  "console.log('provider');",
	}

	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatalf("création du fichier %s: %v", name, err)
		}
	}

	mux := newProviderConsoleTestMux(t)

	for name, expectedBody := range files {
		t.Run(name, func(t *testing.T) {
			path := "/provider/"
			if name != "index.html" {
				path += name
			}

			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()

			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf(
					"statut incorrect: obtenu %d, attendu %d",
					rec.Code,
					http.StatusOK,
				)
			}

			if rec.Body.String() != expectedBody {
				t.Fatalf(
					"contenu incorrect: obtenu %q, attendu %q",
					rec.Body.String(),
					expectedBody,
				)
			}

			if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
				t.Fatalf("Cache-Control incorrect: %q", got)
			}

			if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Fatalf("X-Content-Type-Options incorrect: %q", got)
			}
		})
	}
}

func TestProviderConsoleRejectsUnknownFiles(t *testing.T) {
	t.Setenv("PROVIDER_CONSOLE_DIR", t.TempDir())

	mux := newProviderConsoleTestMux(t)

	paths := []string{
		"/provider/secret.txt",
		"/provider/config.json",
		"/provider/unknown.js",
		"/provider/subdir/index.html",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()

			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusNotFound {
				t.Fatalf(
					"statut incorrect pour %s: obtenu %d, attendu %d",
					path,
					rec.Code,
					http.StatusNotFound,
				)
			}
		})
	}
}

func TestProviderConsoleHeadRequest(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PROVIDER_CONSOLE_DIR", dir)

	content := "<html>provider</html>"
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	mux := newProviderConsoleTestMux(t)

	req := httptest.NewRequest(http.MethodHead, "/provider/", nil)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf(
			"statut incorrect: obtenu %d, attendu %d",
			rec.Code,
			http.StatusOK,
		)
	}

	if rec.Body.Len() != 0 {
		t.Fatalf("HEAD ne doit pas retourner de corps, obtenu %q", rec.Body.String())
	}
}
