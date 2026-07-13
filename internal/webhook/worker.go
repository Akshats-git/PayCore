package webhook

import (
	"context"
	"log/slog"
	"math/rand"
	"time"

	"github.com/Akshats-git/PayCore/internal/storage"
)

// Delivery tuning defaults.
const (
	defaultInterval    = 1 * time.Second
	defaultBatchSize   = 20
	defaultMaxAttempts = 5
	defaultBaseBackoff = 1 * time.Second
	defaultMaxBackoff  = 5 * time.Minute
	// leaseDuration is how long a claimed event is hidden from other workers
	// while this one delivers it; if the worker crashes, the event becomes due
	// again after the lease and is retried.
	leaseDuration = 30 * time.Second
)

// Worker polls the outbox and delivers pending events, retrying failures with
// exponential backoff + jitter and dead-lettering events that never succeed.
type Worker struct {
	outbox      *storage.OutboxRepo
	sender      Sender
	logger      *slog.Logger
	interval    time.Duration
	batchSize   int
	maxAttempts int
	baseBackoff time.Duration
	maxBackoff  time.Duration
	rng         *rand.Rand
	now         func() time.Time
}

// NewWorker returns a delivery worker with sensible defaults.
func NewWorker(outbox *storage.OutboxRepo, sender Sender, logger *slog.Logger) *Worker {
	return &Worker{
		outbox:      outbox,
		sender:      sender,
		logger:      logger,
		interval:    defaultInterval,
		batchSize:   defaultBatchSize,
		maxAttempts: defaultMaxAttempts,
		baseBackoff: defaultBaseBackoff,
		maxBackoff:  defaultMaxBackoff,
		rng:         rand.New(rand.NewSource(time.Now().UnixNano())),
		now:         time.Now,
	}
}

// Run polls the outbox on a fixed interval until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.processBatch(ctx); err != nil {
				w.logger.Error("webhook batch failed", "err", err)
			}
		}
	}
}

// processBatch claims a batch of due events and delivers each.
func (w *Worker) processBatch(ctx context.Context) error {
	events, err := w.outbox.ClaimDue(ctx, w.batchSize, leaseDuration)
	if err != nil {
		return err
	}
	for _, e := range events {
		w.deliver(ctx, e)
	}
	return nil
}

// deliver attempts one delivery and records the outcome: delivered, rescheduled
// with backoff, or dead-lettered once it has failed too many times.
func (w *Worker) deliver(ctx context.Context, e storage.OutboxEvent) {
	if err := w.sender.Send(ctx, e.Payload); err == nil {
		if err := w.outbox.MarkDelivered(ctx, e.ID); err != nil {
			w.logger.Error("mark delivered failed", "id", e.ID, "err", err)
		}
		return
	}

	attempts := e.Attempts + 1
	if attempts >= w.maxAttempts {
		if err := w.outbox.MarkDead(ctx, e.ID, attempts); err != nil {
			w.logger.Error("mark dead failed", "id", e.ID, "err", err)
			return
		}
		w.logger.Warn("webhook event dead-lettered", "id", e.ID, "attempts", attempts)
		return
	}

	next := w.now().Add(nextBackoff(attempts, w.baseBackoff, w.maxBackoff, w.rng))
	if err := w.outbox.Reschedule(ctx, e.ID, attempts, next); err != nil {
		w.logger.Error("reschedule failed", "id", e.ID, "err", err)
	}
}

// nextBackoff returns an exponentially increasing delay for the given attempt,
// capped at maxDelay, with "equal jitter" — at least half the delay, up to the
// full delay — so many events that failed at the same moment don't all retry in
// lockstep and pile onto a recovering receiver.
func nextBackoff(attempt int, base, maxDelay time.Duration, rng *rand.Rand) time.Duration {
	shift := attempt - 1
	if shift > 16 { // guard against shifting into overflow
		shift = 16
	}
	delay := base << uint(shift)
	if delay <= 0 || delay > maxDelay {
		delay = maxDelay
	}
	half := delay / 2
	return half + time.Duration(rng.Int63n(int64(half)+1))
}
