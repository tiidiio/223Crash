package httpapi

import (
	"net/http"
	"github.com/tiidiio/223Gaming/internal/productadapter"
)

func AttachHandshake(mux *http.ServeMux) {
	adapter := productadapter.NewDefaultProductAdapter()
	h := newProductSessionHandler(adapter)
	mux.HandleFunc("POST /api/v1/session/handshake", h.handleHandshake)
}
