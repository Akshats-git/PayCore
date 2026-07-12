package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/Akshats-git/PayCore/internal/ledger"
)

// AccountService is the slice of account behavior the HTTP layer needs.
// *storage.AccountRepo satisfies it. Defining the interface here, where it is
// consumed, keeps the handler decoupled from the concrete storage type and
// trivially fakeable in tests.
type AccountService interface {
	Create(ctx context.Context, name string, accountType ledger.AccountType, currency string) (ledger.Account, error)
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
}

func handleCreateAccount(logger *slog.Logger, accounts AccountService) http.HandlerFunc {
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

		acc, err := accounts.Create(r.Context(), req.Name, accountType, req.Currency)
		if err != nil {
			logger.Error("create account failed", "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		writeJSON(w, http.StatusCreated, accountResponse{
			ID:       acc.ID,
			Name:     acc.Name,
			Type:     string(acc.Type),
			Currency: acc.Currency,
		})
	}
}
