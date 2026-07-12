package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/Akshats-git/PayCore/internal/charges"
	"github.com/Akshats-git/PayCore/internal/ledger"
)

// ChargeService is the slice of charge behavior the HTTP layer needs.
// *charges.Service satisfies it.
type ChargeService interface {
	Create(ctx context.Context, fromAccount, toAccount int64, amount ledger.Money, currency string) (charges.Charge, error)
	Get(ctx context.Context, id int64) (charges.Charge, error)
}

type createChargeRequest struct {
	FromAccount int64  `json:"from_account"`
	ToAccount   int64  `json:"to_account"`
	Amount      int64  `json:"amount"` // minor units (paise/cents)
	Currency    string `json:"currency"`
}

type chargeResponse struct {
	ID          int64     `json:"id"`
	FromAccount int64     `json:"from_account"`
	ToAccount   int64     `json:"to_account"`
	Amount      int64     `json:"amount"`
	Currency    string    `json:"currency"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
}

func toChargeResponse(c charges.Charge) chargeResponse {
	return chargeResponse{
		ID:          c.ID,
		FromAccount: c.FromAccount,
		ToAccount:   c.ToAccount,
		Amount:      int64(c.Amount),
		Currency:    c.Currency,
		Status:      c.Status,
		CreatedAt:   c.CreatedAt,
	}
}

func handleCreateCharge(logger *slog.Logger, svc ChargeService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createChargeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if req.Amount <= 0 {
			writeError(w, http.StatusBadRequest, "amount must be a positive integer in minor units")
			return
		}
		if req.FromAccount == req.ToAccount {
			writeError(w, http.StatusBadRequest, "from_account and to_account must differ")
			return
		}
		if req.Currency == "" {
			writeError(w, http.StatusBadRequest, "currency is required")
			return
		}

		c, err := svc.Create(r.Context(), req.FromAccount, req.ToAccount, ledger.Money(req.Amount), req.Currency)
		if err != nil {
			switch {
			case errors.Is(err, charges.ErrAccountNotFound):
				writeError(w, http.StatusUnprocessableEntity, "from_account or to_account does not exist")
			default:
				logger.Error("create charge failed", "err", err)
				writeError(w, http.StatusInternalServerError, "internal error")
			}
			return
		}
		writeJSON(w, http.StatusCreated, toChargeResponse(c))
	}
}

func handleGetCharge(logger *slog.Logger, svc ChargeService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid charge id")
			return
		}
		c, err := svc.Get(r.Context(), id)
		if err != nil {
			if errors.Is(err, charges.ErrChargeNotFound) {
				writeError(w, http.StatusNotFound, "charge not found")
				return
			}
			logger.Error("get charge failed", "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		writeJSON(w, http.StatusOK, toChargeResponse(c))
	}
}
