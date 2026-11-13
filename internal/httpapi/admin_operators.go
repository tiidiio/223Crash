package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/tiidiio/223Crash/internal/adminauth"
	"github.com/tiidiio/223Crash/internal/adminstore"
)

const maxOperatorRequestBody = 16 << 10

type operatorRepository interface {
	CreateOperator(
		ctx context.Context,
		input adminstore.CreateOperatorInput,
		createdBy string,
	) (*adminstore.Operator, error)

	ListOperators(
		ctx context.Context,
		limit int64,
	) ([]adminstore.Operator, error)
}

func adminOperatorsListHandler(
	w http.ResponseWriter,
	r *http.Request,
	repo operatorRepository,
) {
	if repo == nil {
		writeOperatorError(
			w,
			http.StatusServiceUnavailable,
			"storage_unavailable",
			"Le stockage des opérateurs est indisponible.",
		)
		return
	}

	limit := adminstore.DefaultOperatorListLimit

	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed <= 0 {
			writeOperatorError(
				w,
				http.StatusBadRequest,
				"invalid_limit",
				"La limite demandée est invalide.",
			)
			return
		}
		limit = parsed
	}

	ctx, cancel := requestContext(r)
	defer cancel()

	operators, err := repo.ListOperators(ctx, limit)
	if err != nil {
		writeOperatorError(
			w,
			http.StatusInternalServerError,
			"operator_list_failed",
			"Impossible de récupérer les opérateurs.",
		)
		return
	}

	if operators == nil {
		operators = make([]adminstore.Operator, 0)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"operators": operators,
		"count":     len(operators),
	})
}

func adminOperatorsCreateHandler(
	w http.ResponseWriter,
	r *http.Request,
	repo operatorRepository,
) {
	if repo == nil {
		writeOperatorError(
			w,
			http.StatusServiceUnavailable,
			"storage_unavailable",
			"Le stockage des opérateurs est indisponible.",
		)
		return
	}

	createdBy := authenticatedAdminID(r)
	if createdBy == "" {
		writeOperatorError(
			w,
			http.StatusUnauthorized,
			"admin_required",
			"Une session administrateur valide est nécessaire.",
		)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxOperatorRequestBody)
	defer r.Body.Close()

	var input adminstore.CreateOperatorInput

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&input); err != nil {
		writeOperatorError(
			w,
			http.StatusBadRequest,
			"invalid_request",
			"Le corps JSON est absent ou invalide.",
		)
		return
	}

	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeOperatorError(
			w,
			http.StatusBadRequest,
			"invalid_request",
			"Le corps doit contenir un seul objet JSON.",
		)
		return
	}

	normalized, err := adminstore.ValidateOperatorInput(input)
	if err != nil {
		writeOperatorError(
			w,
			http.StatusBadRequest,
			"invalid_operator",
			err.Error(),
		)
		return
	}

	ctx, cancel := requestContext(r)
	defer cancel()

	operator, err := repo.CreateOperator(ctx, normalized, createdBy)
	if err != nil {
		switch {
		case errors.Is(err, adminstore.ErrInvalidOperator):
			writeOperatorError(
				w,
				http.StatusBadRequest,
				"invalid_operator",
				err.Error(),
			)
		case errors.Is(err, adminstore.ErrOperatorConflict):
			writeOperatorError(
				w,
				http.StatusConflict,
				"operator_conflict",
				"Un opérateur utilise déjà ce code ou cet identifiant.",
			)
		default:
			writeOperatorError(
				w,
				http.StatusInternalServerError,
				"operator_creation_failed",
				"Impossible de créer l'opérateur.",
			)
		}
		return
	}

	if operator == nil {
		writeOperatorError(
			w,
			http.StatusInternalServerError,
			"operator_creation_failed",
			"Le stockage n'a retourné aucun opérateur créé.",
		)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"operator": operator,
	})
}

func authenticatedAdminID(r *http.Request) string {
	authenticated, ok := r.Context().
		Value(adminContextKey{}).(*adminauth.AuthenticatedAdmin)

	if !ok || authenticated == nil || authenticated.Admin == nil {
		return ""
	}

	return strings.TrimSpace(authenticated.Admin.ID)
}

func writeOperatorError(
	w http.ResponseWriter,
	status int,
	code string,
	message string,
) {
	writeJSON(w, status, map[string]string{
		"error":   code,
		"message": message,
	})
}

func validateOperatorRequestBodySize(err error) bool {
	var maxBytesErr *http.MaxBytesError
	return errors.As(err, &maxBytesErr)
}

func operatorInternalError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("operator API: %w", err)
}
