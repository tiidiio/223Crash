package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tiidiio/223Crash/internal/adminauth"
	"github.com/tiidiio/223Crash/internal/adminstore"
)

type fakeOperatorRepository struct {
	operators []adminstore.Operator
	created   *adminstore.Operator

	listErr   error
	createErr error

	listLimit int64
	createdBy string
	input     adminstore.CreateOperatorInput
}

func (f *fakeOperatorRepository) ListOperators(
	ctx context.Context,
	limit int64,
) ([]adminstore.Operator, error) {
	f.listLimit = limit
	return f.operators, f.listErr
}

func (f *fakeOperatorRepository) CreateOperator(
	ctx context.Context,
	input adminstore.CreateOperatorInput,
	createdBy string,
) (*adminstore.Operator, error) {
	f.input = input
	f.createdBy = createdBy

	if f.createErr != nil {
		return nil, f.createErr
	}

	return f.created, nil
}

func operatorTestRequest(
	method string,
	body string,
) (*httptest.ResponseRecorder, *http.Request) {
	req := httptest.NewRequest(method, "/api/admin/operators", strings.NewReader(body))
	rec := httptest.NewRecorder()
	return rec, req
}

func operatorTestAdminRequest(
	method string,
	body string,
) (*httptest.ResponseRecorder, *http.Request) {
	rec, req := operatorTestRequest(method, body)

	authenticated := &adminauth.AuthenticatedAdmin{
		Admin: &adminstore.Admin{
			ID:    "admin-test-001",
			Email: "admin@example.test",
			Role:  "founder",
		},
	}

	req = req.WithContext(contextWithAdmin(req, authenticated))
	return rec, req
}

func TestAdminOperatorsListHandler(t *testing.T) {
	now := time.Now().UTC()

	repo := &fakeOperatorRepository{
		operators: []adminstore.Operator{
			{
				ID:         "record-001",
				OperatorID: "operator-001",
				Name:       "Operator One",
				Code:       "OPERATOR_01",
				Currency:   "XOF",
				Status:     "active",
				CreatedAt:  now,
				UpdatedAt:  now,
			},
		},
	}

	rec, req := operatorTestRequest(http.MethodGet, "")
	adminOperatorsListHandler(rec, req, repo)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload struct {
		Operators []adminstore.Operator `json:"operators"`
		Count     int                   `json:"count"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if payload.Count != 1 || len(payload.Operators) != 1 {
		t.Fatalf("unexpected list response: %+v", payload)
	}

	if payload.Operators[0].OperatorID != "operator-001" {
		t.Errorf("operatorId = %q, want operator-001",
			payload.Operators[0].OperatorID)
	}

	if repo.listLimit != adminstore.DefaultOperatorListLimit {
		t.Errorf("limit = %d, want %d",
			repo.listLimit, adminstore.DefaultOperatorListLimit)
	}

	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("expected Cache-Control: no-store")
	}
}

func TestAdminOperatorsListHandlerCustomLimit(t *testing.T) {
	repo := &fakeOperatorRepository{}

	rec, req := operatorTestRequest(http.MethodGet, "")
	req.URL.RawQuery = "limit=25"

	adminOperatorsListHandler(rec, req, repo)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	if repo.listLimit != 25 {
		t.Errorf("limit = %d, want 25", repo.listLimit)
	}
}

func TestAdminOperatorsListHandlerInvalidLimit(t *testing.T) {
	repo := &fakeOperatorRepository{}

	rec, req := operatorTestRequest(http.MethodGet, "")
	req.URL.RawQuery = "limit=invalid"

	adminOperatorsListHandler(rec, req, repo)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestAdminOperatorsListHandlerStorageUnavailable(t *testing.T) {
	rec, req := operatorTestRequest(http.MethodGet, "")

	adminOperatorsListHandler(rec, req, nil)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestAdminOperatorsListHandlerDatabaseError(t *testing.T) {
	repo := &fakeOperatorRepository{
		listErr: errors.New("database unavailable"),
	}

	rec, req := operatorTestRequest(http.MethodGet, "")
	adminOperatorsListHandler(rec, req, repo)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}

	if strings.Contains(rec.Body.String(), "database unavailable") {
		t.Fatal("internal database error leaked into response")
	}
}

func TestAdminOperatorsCreateHandlerSuccess(t *testing.T) {
	now := time.Now().UTC()

	created := &adminstore.Operator{
		ID:         "record-001",
		OperatorID: "operator-001",
		Name:       "Operator One",
		Code:       "OPERATOR_01",
		Currency:   "XOF",
		Status:     "active",
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	repo := &fakeOperatorRepository{
		created: created,
	}

	body := `{
		"name": "  Operator One  ",
		"code": "operator_01",
		"currency": "xof",
		"callbackUrl": "https://operator.example/wallet"
	}`

	rec, req := operatorTestAdminRequest(http.MethodPost, body)
	adminOperatorsCreateHandler(rec, req, repo)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s",
			rec.Code, rec.Body.String())
	}

	if repo.createdBy != "admin-test-001" {
		t.Errorf("createdBy = %q, want admin-test-001", repo.createdBy)
	}

	if repo.input.Name != "Operator One" {
		t.Errorf("name = %q, want normalized name", repo.input.Name)
	}

	if repo.input.Code != "OPERATOR_01" {
		t.Errorf("code = %q, want OPERATOR_01", repo.input.Code)
	}

	if repo.input.Currency != "XOF" {
		t.Errorf("currency = %q, want XOF", repo.input.Currency)
	}

	var payload struct {
		Operator adminstore.Operator `json:"operator"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if payload.Operator.OperatorID != "operator-001" {
		t.Errorf("operatorId = %q, want operator-001",
			payload.Operator.OperatorID)
	}
}

func TestAdminOperatorsCreateHandlerRejectsUnknownField(t *testing.T) {
	repo := &fakeOperatorRepository{}

	body := `{
		"name": "Operator One",
		"code": "OPERATOR_01",
		"currency": "XOF",
		"unexpected": true
	}`

	rec, req := operatorTestAdminRequest(http.MethodPost, body)
	adminOperatorsCreateHandler(rec, req, repo)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestAdminOperatorsCreateHandlerRejectsTrailingJSON(t *testing.T) {
	repo := &fakeOperatorRepository{}

	body := `{"name":"Operator One","code":"OPERATOR_01","currency":"XOF"} {}`

	rec, req := operatorTestAdminRequest(http.MethodPost, body)
	adminOperatorsCreateHandler(rec, req, repo)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestAdminOperatorsCreateHandlerRejectsInvalidOperator(t *testing.T) {
	repo := &fakeOperatorRepository{}

	body := `{
		"name": "A",
		"code": "OPERATOR_01",
		"currency": "XOF"
	}`

	rec, req := operatorTestAdminRequest(http.MethodPost, body)
	adminOperatorsCreateHandler(rec, req, repo)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestAdminOperatorsCreateHandlerRejectsHTTPCallback(t *testing.T) {
	repo := &fakeOperatorRepository{}

	body := `{
		"name": "Operator One",
		"code": "OPERATOR_01",
		"currency": "XOF",
		"callbackUrl": "http://operator.example/wallet"
	}`

	rec, req := operatorTestAdminRequest(http.MethodPost, body)
	adminOperatorsCreateHandler(rec, req, repo)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestAdminOperatorsCreateHandlerConflict(t *testing.T) {
	repo := &fakeOperatorRepository{
		createErr: adminstore.ErrOperatorConflict,
	}

	body := `{
		"name": "Operator One",
		"code": "OPERATOR_01",
		"currency": "XOF"
	}`

	rec, req := operatorTestAdminRequest(http.MethodPost, body)
	adminOperatorsCreateHandler(rec, req, repo)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestAdminOperatorsCreateHandlerRequiresAdmin(t *testing.T) {
	repo := &fakeOperatorRepository{}

	body := `{
		"name": "Operator One",
		"code": "OPERATOR_01",
		"currency": "XOF"
	}`

	rec, req := operatorTestRequest(http.MethodPost, body)
	adminOperatorsCreateHandler(rec, req, repo)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestAdminOperatorsCreateHandlerStorageUnavailable(t *testing.T) {
	body := `{
		"name": "Operator One",
		"code": "OPERATOR_01",
		"currency": "XOF"
	}`

	rec, req := operatorTestAdminRequest(http.MethodPost, body)
	adminOperatorsCreateHandler(rec, req, nil)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}
