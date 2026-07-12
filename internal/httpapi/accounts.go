package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/Akshats-git/PayCore/internal/accounts"
	"github.com/Akshats-git/PayCore/internal/ledger"
)

// AccountService is the slice of account behavior the HTTP layer needs.
// *accounts.Service satisfies it.
type AccountService interface {
	Create(ctx context.Context, name string, accountType ledger.AccountType, currency string) (accounts.Account, error)
	Get(ctx context.Context, id int64) (accounts.Account, error)
}

type createAccountRequest struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Currency string `json:"currency"`
}

type accountResponse struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Currency string `json:"currency"`
	Balance  int64  `json:"balance"` // minor units, derived from ledger entries
}

func toAccountResponse(a accounts.Account) accountResponse {
	return accountResponse{
		ID:       a.ID,
		Name:     a.Name,
		Type:     string(a.Type),
		Currency: a.Currency,
		Balance:  int64(a.Balance),
	}
}

func handleCreateAccount(logger *slog.Logger, svc AccountService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createAccountRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if req.Name == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}
		accountType := ledger.AccountType(req.Type)
		if accountType != ledger.Asset && accountType != ledger.Liability {
			writeError(w, http.StatusBadRequest, `type must be "asset" or "liability"`)
			return
		}
		if req.Currency == "" {
			writeError(w, http.StatusBadRequest, "currency is required")
			return
		}

		acc, err := svc.Create(r.Context(), req.Name, accountType, req.Currency)
		if err != nil {
			logger.Error("create account failed", "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		writeJSON(w, http.StatusCreated, toAccountResponse(acc))
	}
}

func handleGetAccount(logger *slog.Logger, svc AccountService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid account id")
			return
		}
		acc, err := svc.Get(r.Context(), id)
		if err != nil {
			if errors.Is(err, accounts.ErrNotFound) {
				writeError(w, http.StatusNotFound, "account not found")
				return
			}
			logger.Error("get account failed", "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		writeJSON(w, http.StatusOK, toAccountResponse(acc))
	}
}
