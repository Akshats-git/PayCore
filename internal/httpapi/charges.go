package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/Akshats-git/PayCore/internal/charges"
	"github.com/Akshats-git/PayCore/internal/ledger"
)

// maxChargeBodyBytes caps how much of a request body we'll read, so a giant body
// can't exhaust memory.
const maxChargeBodyBytes = 64 << 10 // 64 KiB

// ChargeService is the slice of charge behavior the HTTP layer needs.
// *charges.Service satisfies it. CreateIdempotent returns the status code and
// response body to send (identical on the first call and every retry).
type ChargeService interface {
	CreateIdempotent(ctx context.Context, key string, rawBody []byte, req charges.CreateRequest) (int, []byte, error)
	Get(ctx context.Context, id int64) (charges.Charge, error)
}

type createChargeRequest struct {
	FromAccount int64  `json:"from_account"`
	ToAccount   int64  `json:"to_account"`
	Amount      int64  `json:"amount"` // minor units (paise/cents)
	Currency    string `json:"currency"`
}

func handleCreateCharge(logger *slog.Logger, svc ChargeService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Every charge must carry an idempotency key so retries are always safe.
		key := r.Header.Get("Idempotency-Key")
		if key == "" {
			writeError(w, http.StatusBadRequest, "Idempotency-Key header is required")
			return
		}

		// Read the raw bytes: we both hash them (for idempotency) and parse them.
		rawBody, err := io.ReadAll(io.LimitReader(r.Body, maxChargeBodyBytes))
		if err != nil {
			writeError(w, http.StatusBadRequest, "could not read request body")
			return
		}

		var req createChargeRequest
		if err := json.Unmarshal(rawBody, &req); err != nil {
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

		code, body, err := svc.CreateIdempotent(r.Context(), key, rawBody, charges.CreateRequest{
			FromAccount: req.FromAccount,
			ToAccount:   req.ToAccount,
			Amount:      ledger.Money(req.Amount),
			Currency:    req.Currency,
		})
		if err != nil {
			switch {
			case errors.Is(err, charges.ErrAccountNotFound):
				writeError(w, http.StatusUnprocessableEntity, "from_account or to_account does not exist")
			case errors.Is(err, charges.ErrKeyConflict):
				writeError(w, http.StatusUnprocessableEntity, "Idempotency-Key was already used with a different request")
			case errors.Is(err, charges.ErrKeyInProgress):
				writeError(w, http.StatusConflict, "a request with this Idempotency-Key is still in progress")
			default:
				logger.Error("create charge failed", "err", err)
				writeError(w, http.StatusInternalServerError, "internal error")
			}
			return
		}
		// Write the exact bytes the service produced (and stored for replay).
		writeRaw(w, code, body)
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
		writeJSON(w, http.StatusOK, c)
	}
}
