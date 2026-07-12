package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Akshats-git/PayCore/internal/accounts"
	"github.com/Akshats-git/PayCore/internal/ledger"
)

type fakeAccountService struct {
	createFn func(ctx context.Context, name string, t ledger.AccountType, currency string) (accounts.Account, error)
	getFn    func(ctx context.Context, id int64) (accounts.Account, error)
}

func (f fakeAccountService) Create(ctx context.Context, name string, t ledger.AccountType, currency string) (accounts.Account, error) {
	return f.createFn(ctx, name, t, currency)
}

func (f fakeAccountService) Get(ctx context.Context, id int64) (accounts.Account, error) {
	return f.getFn(ctx, id)
}

func accountRouter(svc AccountService) http.Handler {
	return NewRouter(Deps{Logger: discardLogger(), Ready: alwaysReady, Accounts: svc})
}

func TestCreateAccountSuccess(t *testing.T) {
	svc := fakeAccountService{createFn: func(_ context.Context, name string, typ ledger.AccountType, currency string) (accounts.Account, error) {
		return accounts.Account{ID: 1, Name: name, Type: typ, Currency: currency}, nil
	}}
	req := httptest.NewRequest(http.MethodPost, "/v1/accounts", strings.NewReader(`{"name":"alice","type":"liability","currency":"INR"}`))
	rec := httptest.NewRecorder()
	accountRouter(svc).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
}

func TestCreateAccountValidation(t *testing.T) {
	panicSvc := fakeAccountService{createFn: func(context.Context, string, ledger.AccountType, string) (accounts.Account, error) {
		panic("service should not be called for an invalid request")
	}}
	cases := map[string]string{
		"bad json":         `{`,
		"missing name":     `{"name":"","type":"liability","currency":"INR"}`,
		"bad type":         `{"name":"x","type":"wallet","currency":"INR"}`,
		"missing currency": `{"name":"x","type":"asset","currency":""}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/accounts", strings.NewReader(body))
			rec := httptest.NewRecorder()
			accountRouter(panicSvc).ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
		})
	}
}

func TestGetAccountSuccess(t *testing.T) {
	svc := fakeAccountService{getFn: func(_ context.Context, id int64) (accounts.Account, error) {
		return accounts.Account{ID: id, Name: "bob", Type: ledger.Liability, Currency: "INR", Balance: 50000}, nil
	}}
	req := httptest.NewRequest(http.MethodGet, "/v1/accounts/2", nil)
	rec := httptest.NewRecorder()
	accountRouter(svc).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp accountResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ID != 2 || resp.Balance != 50000 {
		t.Fatalf("response = %+v, want id=2 balance=50000", resp)
	}
}

func TestGetAccountNotFound(t *testing.T) {
	svc := fakeAccountService{getFn: func(context.Context, int64) (accounts.Account, error) {
		return accounts.Account{}, accounts.ErrNotFound
	}}
	req := httptest.NewRequest(http.MethodGet, "/v1/accounts/999", nil)
	rec := httptest.NewRecorder()
	accountRouter(svc).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestGetAccountInvalidID(t *testing.T) {
	svc := fakeAccountService{getFn: func(context.Context, int64) (accounts.Account, error) {
		panic("service should not be called for an invalid id")
	}}
	req := httptest.NewRequest(http.MethodGet, "/v1/accounts/xyz", nil)
	rec := httptest.NewRecorder()
	accountRouter(svc).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
