package httpapi

import (
	"github.com/tiidiio/223Crash/internal/productadapter"
	"net/http"
)

func AttachHandshake(mux *http.ServeMux) {
	adapter := productadapter.NewDefaultProductAdapter()
	h := newProductSessionHandler(adapter)
	mux.HandleFunc("POST /api/v1/session/handshake", h.handleHandshake)
}
