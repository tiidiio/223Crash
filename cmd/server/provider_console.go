package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func registerProviderConsole(mux *http.ServeMux) {
	dir := os.Getenv("PROVIDER_CONSOLE_DIR")
	if dir == "" {
		dir = "../provider-console"
	}

	files := map[string]bool{
		"index.html": true,
		"style.css":  true,
		"script.js":  true,
	}

	mux.HandleFunc("/provider", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		http.Redirect(w, r, "/provider/", http.StatusPermanentRedirect)
	})

	mux.HandleFunc("/provider/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		name := strings.TrimPrefix(r.URL.Path, "/provider/")
		if name == "" {
			name = "index.html"
		}

		if !files[name] {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, filepath.Join(dir, name))
	})
}
