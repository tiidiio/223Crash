package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	v1 "github.com/tiidiio/223Crash/contracts/product/v1"
	"github.com/tiidiio/223Crash/internal/productadapter"
)

type productSessionHandler struct {
	adapter *productadapter.DefaultProductAdapter
}

func newProductSessionHandler(adapter *productadapter.DefaultProductAdapter) *productSessionHandler {
	return &productSessionHandler{
		adapter: adapter,
	}
}

func (h *productSessionHandler) handleHandshake(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{
			"error": "method_not_allowed",
		})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	defer r.Body.Close()

	var req v1.SessionHandshakeRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":     "invalid_request_body",
			"error_msg": "Le format JSON de la requête est invalide ou le champ client_time est malformé.",
		})
		return
	}

	if strings.TrimSpace(req.Token) == "" || strings.TrimSpace(req.ProductID) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":     "invalid_parameters",
			"error_msg": "Les champs token et product_id sont obligatoires.",
		})
		return
	}

	ctx, cancel := requestContext(r)
	defer cancel()

	resp, err := h.adapter.ValidateSession(ctx, &req)
	if err != nil || resp == nil {
		errMsg := "session validation failed"
		if err != nil {
			errMsg = err.Error()
		}
		writeJSON(w, http.StatusBadRequest, v1.SessionHandshakeResponse{
			IsValid:  false,
			ErrorMsg: errMsg,
		})
		return
	}

	writeJSON(w, http.StatusOK, resp)
}
