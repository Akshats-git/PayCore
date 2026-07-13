package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Akshats-git/PayCore/internal/charges"
)

// fakeChargeService is an in-memory ChargeService so the HTTP handlers can be
// tested with no database.
type fakeChargeService struct {
	createFn func(ctx context.Context, key string, rawBody []byte, req charges.CreateRequest) (int, []byte, error)
	getFn    func(ctx context.Context, id int64) (charges.Charge, error)
}

func (f fakeChargeService) CreateIdempotent(ctx context.Context, key string, rawBody []byte, req charges.CreateRequest) (int, []byte, error) {
	return f.createFn(ctx, key, rawBody, req)
}

func (f fakeChargeService) Get(ctx context.Context, id int64) (charges.Charge, error) {
	return f.getFn(ctx, id)
}

func chargeRouter(svc ChargeService) http.Handler {
	return NewRouter(Deps{Logger: discardLogger(), Ready: alwaysReady, Charges: svc})
}

// postCharge sends a POST /v1/charges, setting the Idempotency-Key header unless
// key is empty.
func postCharge(t *testing.T, svc ChargeService, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/charges", strings.NewReader(body))
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	chargeRouter(svc).ServeHTTP(rec, req)
	return rec
}

// validBody is a well-formed charge request used where the request itself isn't
// the thing under test.
const validBody = `{"from_account":1,"to_account":2,"amount":50000,"currency":"INR"}`

func TestCreateChargeSuccess(t *testing.T) {
	stored := []byte(`{"id":42,"from_account":1,"to_account":2,"amount":50000,"currency":"INR","status":"succeeded"}`)
	svc := fakeChargeService{createFn: func(_ context.Context, key string, _ []byte, _ charges.CreateRequest) (int, []byte, error) {
		if key != "abc123" {
			t.Errorf("handler passed key %q, want abc123", key)
		}
		return http.StatusCreated, stored, nil
	}}

	rec := postCharge(t, svc, "abc123", validBody)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != string(stored) {
		t.Fatalf("body = %s, want the exact stored bytes %s", rec.Body.String(), stored)
	}
}

func TestCreateChargeMissingIdempotencyKey(t *testing.T) {
	panicSvc := fakeChargeService{createFn: func(context.Context, string, []byte, charges.CreateRequest) (int, []byte, error) {
		panic("service should not be called without an idempotency key")
	}}
	rec := postCharge(t, panicSvc, "", validBody)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestCreateChargeValidationErrors(t *testing.T) {
	panicSvc := fakeChargeService{createFn: func(context.Context, string, []byte, charges.CreateRequest) (int, []byte, error) {
		panic("service should not be called for an invalid request")
	}}
	cases := map[string]string{
		"bad json":            `{not json`,
		"non-positive amount": `{"from_account":1,"to_account":2,"amount":0,"currency":"INR"}`,
		"same account":        `{"from_account":1,"to_account":1,"amount":100,"currency":"INR"}`,
		"missing currency":    `{"from_account":1,"to_account":2,"amount":100,"currency":""}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			rec := postCharge(t, panicSvc, "key", body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
		})
	}
}

func TestCreateChargeErrorMapping(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode int
	}{
		{"account not found", charges.ErrAccountNotFound, http.StatusUnprocessableEntity},
		{"key conflict", charges.ErrKeyConflict, http.StatusUnprocessableEntity},
		{"key in progress", charges.ErrKeyInProgress, http.StatusConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := fakeChargeService{createFn: func(context.Context, string, []byte, charges.CreateRequest) (int, []byte, error) {
				return 0, nil, tc.err
			}}
			rec := postCharge(t, svc, "key", validBody)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantCode)
			}
		})
	}
}

func TestGetChargeSuccess(t *testing.T) {
	svc := fakeChargeService{getFn: func(_ context.Context, id int64) (charges.Charge, error) {
		return charges.Charge{ID: id, FromAccount: 1, ToAccount: 2, Amount: 100, Currency: "INR", Status: "succeeded"}, nil
	}}

	req := httptest.NewRequest(http.MethodGet, "/v1/charges/7", nil)
	rec := httptest.NewRecorder()
	chargeRouter(svc).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"id":7`) {
		t.Fatalf("body %s does not contain id 7", rec.Body.String())
	}
}

func TestGetChargeNotFound(t *testing.T) {
	svc := fakeChargeService{getFn: func(context.Context, int64) (charges.Charge, error) {
		return charges.Charge{}, charges.ErrChargeNotFound
	}}

	req := httptest.NewRequest(http.MethodGet, "/v1/charges/999", nil)
	rec := httptest.NewRecorder()
	chargeRouter(svc).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestGetChargeInvalidID(t *testing.T) {
	svc := fakeChargeService{getFn: func(context.Context, int64) (charges.Charge, error) {
		panic("service should not be called for an invalid id")
	}}

	req := httptest.NewRequest(http.MethodGet, "/v1/charges/abc", nil)
	rec := httptest.NewRecorder()
	chargeRouter(svc).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
