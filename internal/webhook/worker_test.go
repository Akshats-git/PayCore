package webhook

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Akshats-git/PayCore/internal/storage"
	"github.com/Akshats-git/PayCore/migrations"
)

func testOutbox(t *testing.T) (*storage.OutboxRepo, *pgxpool.Pool) {
	t.Helper()
	url := os.Getenv("PAYCORE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PAYCORE_TEST_DATABASE_URL not set; skipping integration test")
	}
	if err := storage.RunMigrations(url, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := storage.NewPostgresPool(context.Background(), url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(context.Background(), `TRUNCATE outbox RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return storage.NewOutboxRepo(pool), pool
}

// controllableSender fails its first `failures` sends, then succeeds.
type controllableSender struct {
	failures int
	calls    int
}

func (s *controllableSender) Send(context.Context, []byte) error {
	s.calls++
	if s.calls <= s.failures {
		return errors.New("receiver unavailable")
	}
	return nil
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// newTestWorker uses a low maxAttempts so the dead-letter path is quick to reach.
func newTestWorker(outbox *storage.OutboxRepo, sender Sender) *Worker {
	w := NewWorker(outbox, sender, discardLogger())
	w.maxAttempts = 3
	return w
}

// forceDue makes all pending events immediately claimable again, so tests can
// drive retries deterministically without waiting out the real backoff.
func forceDue(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`UPDATE outbox SET next_attempt_at = now() - interval '1 hour' WHERE status = 'pending'`); err != nil {
		t.Fatalf("force due: %v", err)
	}
}

func status(t *testing.T, pool *pgxpool.Pool, id int64) (string, int) {
	t.Helper()
	var (
		s string
		a int
	)
	if err := pool.QueryRow(context.Background(),
		`SELECT status, attempts FROM outbox WHERE id = $1`, id).Scan(&s, &a); err != nil {
		t.Fatalf("read status: %v", err)
	}
	return s, a
}

func TestWorkerDeliversSuccess(t *testing.T) {
	outbox, pool := testOutbox(t)
	ctx := context.Background()
	id, _ := outbox.Enqueue(ctx, pool, "charge.succeeded", []byte(`{"x":1}`))

	w := newTestWorker(outbox, &controllableSender{failures: 0})
	if err := w.processBatch(ctx); err != nil {
		t.Fatal(err)
	}

	if s, _ := status(t, pool, id); s != storage.OutboxDelivered {
		t.Fatalf("status = %q, want delivered", s)
	}
}

func TestWorkerRetriesThenDelivers(t *testing.T) {
	outbox, pool := testOutbox(t)
	ctx := context.Background()
	id, _ := outbox.Enqueue(ctx, pool, "e", []byte(`{}`))

	// A flaky receiver: fails twice, then works.
	w := newTestWorker(outbox, &controllableSender{failures: 2})

	w.processBatch(ctx)
	if s, a := status(t, pool, id); s != storage.OutboxPending || a != 1 {
		t.Fatalf("after 1 failure: %s/%d, want pending/1", s, a)
	}
	forceDue(t, pool)

	w.processBatch(ctx)
	if s, a := status(t, pool, id); s != storage.OutboxPending || a != 2 {
		t.Fatalf("after 2 failures: %s/%d, want pending/2", s, a)
	}
	forceDue(t, pool)

	w.processBatch(ctx)
	if s, _ := status(t, pool, id); s != storage.OutboxDelivered {
		t.Fatalf("after recovery: %s, want delivered", s)
	}
}

func TestWorkerDeadLettersAfterMaxAttempts(t *testing.T) {
	outbox, pool := testOutbox(t)
	ctx := context.Background()
	id, _ := outbox.Enqueue(ctx, pool, "e", []byte(`{}`))

	// A permanently broken receiver. maxAttempts is 3.
	w := newTestWorker(outbox, &controllableSender{failures: 1000})
	for i := 0; i < 3; i++ {
		forceDue(t, pool)
		w.processBatch(ctx)
	}

	if s, a := status(t, pool, id); s != storage.OutboxDead || a != 3 {
		t.Fatalf("status = %s/%d, want dead/3", s, a)
	}
}

func TestWorkerReschedulesIntoFuture(t *testing.T) {
	outbox, pool := testOutbox(t)
	ctx := context.Background()
	id, _ := outbox.Enqueue(ctx, pool, "e", []byte(`{}`))

	w := newTestWorker(outbox, &controllableSender{failures: 1000})
	w.processBatch(ctx)

	var next time.Time
	if err := pool.QueryRow(ctx, `SELECT next_attempt_at FROM outbox WHERE id = $1`, id).Scan(&next); err != nil {
		t.Fatal(err)
	}
	if !next.After(time.Now()) {
		t.Fatalf("next_attempt_at = %v, want in the future (backoff delay)", next)
	}
}
