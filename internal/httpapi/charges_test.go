package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Akshats-git/PayCore/internal/charges"
	"github.com/Akshats-git/PayCore/internal/ledger"
)

// fakeChargeService is an in-memory ChargeService so the HTTP handlers can be
// tested with no database.
type fakeChargeService struct {
	createFn func(ctx context.Context, from, to int64, amount ledger.Money, currency string) (charges.Charge, error)
	getFn    func(ctx context.Context, id int64) (charges.Charge, error)
}

func (f fakeChargeService) Create(ctx context.Context, from, to int64, amount ledger.Money, currency string) (charges.Charge, error) {
	return f.createFn(ctx, from, to, amount, currency)
}

func (f fakeChargeService) Get(ctx context.Context, id int64) (charges.Charge, error) {
	return f.getFn(ctx, id)
}

func chargeRouter(svc ChargeService) http.Handler {
	return NewRouter(Deps{Logger: discardLogger(), Ready: alwaysReady, Charges: svc})
}

func postCharge(t *testing.T, svc ChargeService, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/charges", strings.NewReader(body))
	rec := httptest.NewRecorder()
	chargeRouter(svc).ServeHTTP(rec, req)
	return rec
}

func TestCreateChargeSuccess(t *testing.T) {
	svc := fakeChargeService{createFn: func(_ context.Context, from, to int64, amount ledger.Money, currency string) (charges.Charge, error) {
		return charges.Charge{ID: 42, FromAccount: from, ToAccount: to, Amount: amount, Currency: currency, Status: "succeeded", CreatedAt: time.Now()}, nil
	}}

	rec := postCharge(t, svc, `{"from_account":1,"to_account":2,"amount":50000,"currency":"INR"}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var resp chargeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ID != 42 || resp.Amount != 50000 || resp.FromAccount != 1 || resp.ToAccount != 2 {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestCreateChargeValidationErrors(t *testing.T) {
	// Each of these should be rejected with 400 *before* the service is called,
	// so a service that panics if invoked proves the request never reached it.
	panicSvc := fakeChargeService{createFn: func(context.Context, int64, int64, ledger.Money, string) (charges.Charge, error) {
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
			rec := postCharge(t, panicSvc, body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
		})
	}
}

func TestCreateChargeAccountNotFound(t *testing.T) {
	svc := fakeChargeService{createFn: func(context.Context, int64, int64, ledger.Money, string) (charges.Charge, error) {
		return charges.Charge{}, charges.ErrAccountNotFound
	}}

	rec := postCharge(t, svc, `{"from_account":1,"to_account":2,"amount":100,"currency":"INR"}`)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
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
	var resp chargeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ID != 7 {
		t.Fatalf("id = %d, want 7", resp.ID)
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
