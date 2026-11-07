package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const providerConsoleAllowedMethods = "GET, HEAD"

func registerProviderConsole(mux *http.ServeMux) {
	dir := os.Getenv("PROVIDER_CONSOLE_DIR")
	if dir == "" {
		dir = "../provider-console"
	}

	files := map[string]struct{}{
		"index.html": {},
		"style.css":  {},
		"script.js":  {},
	}

	mux.HandleFunc("/provider", func(w http.ResponseWriter, r *http.Request) {
		if !providerConsoleMethodAllowed(r.Method) {
			providerConsoleMethodNotAllowed(w)
			return
		}

		target := "/provider/"
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}

		http.Redirect(w, r, target, http.StatusPermanentRedirect)
	})

	mux.HandleFunc("/provider/", func(w http.ResponseWriter, r *http.Request) {
		if !providerConsoleMethodAllowed(r.Method) {
			providerConsoleMethodNotAllowed(w)
			return
		}

		name := strings.TrimPrefix(r.URL.Path, "/provider/")
		if name == "" {
			name = "index.html"
		}

		if _, allowed := files[name]; !allowed {
			http.NotFound(w, r)
			return
		}

		filePath := filepath.Join(dir, name)
		file, err := os.Open(filePath)
		if err != nil {
			if os.IsNotExist(err) {
				http.NotFound(w, r)
				return
			}

			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		defer file.Close()

		info, err := file.Stat()
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		if !info.Mode().IsRegular() {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")

		http.ServeContent(w, r, name, info.ModTime(), file)
	})
}

func providerConsoleMethodAllowed(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

func providerConsoleMethodNotAllowed(w http.ResponseWriter) {
	w.Header().Set("Allow", providerConsoleAllowedMethods)
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}
