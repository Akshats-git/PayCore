package storage

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestHashRequest is a plain unit test (no database): same body hashes the same,
// different body hashes differently, and the output is a 64-char SHA-256 hex.
func TestHashRequest(t *testing.T) {
	a := HashRequest([]byte(`{"amount":100}`))
	b := HashRequest([]byte(`{"amount":100}`))
	c := HashRequest([]byte(`{"amount":200}`))

	if a != b {
		t.Fatal("identical bodies must hash identically")
	}
	if a == c {
		t.Fatal("different bodies must hash differently")
	}
	if len(a) != 64 {
		t.Fatalf("expected 64 hex chars, got %d", len(a))
	}
}

// TestIdempotencyClaimIsAtomic is the store-level preview of the headline test:
// fire many concurrent Claims for the SAME key and assert exactly one wins. This
// proves the `INSERT ... ON CONFLICT` test-and-set is genuinely atomic before we
// wire it behind an HTTP endpoint.
func TestIdempotencyClaimIsAtomic(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	repo := NewIdempotencyRepo(pool)

	const key = "concurrent-key"
	const n = 50

	var (
		wg      sync.WaitGroup
		winners atomic.Int64
	)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			won, _, err := repo.Claim(ctx, pool, key, "hash", time.Hour)
			if err != nil {
				t.Errorf("claim: %v", err)
				return
			}
			if won {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()

	if got := winners.Load(); got != 1 {
		t.Fatalf("exactly one goroutine should win the claim, got %d", got)
	}
}

// TestIdempotencyClaimReplayAndComplete walks the store lifecycle: first claim
// wins, Complete stores the response, and a second claim loses and returns the
// completed record to replay.
func TestIdempotencyClaimReplayAndComplete(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	repo := NewIdempotencyRepo(pool)

	const key = "lifecycle-key"

	won, _, err := repo.Claim(ctx, pool, key, "hashA", time.Hour)
	if err != nil || !won {
		t.Fatalf("first claim should win: won=%v err=%v", won, err)
	}

	body := []byte(`{"id":1,"status":"succeeded"}`)
	if err := repo.Complete(ctx, pool, key, 201, body, nil); err != nil {
		t.Fatalf("complete: %v", err)
	}

	won2, existing, err := repo.Claim(ctx, pool, key, "hashA", time.Hour)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if won2 {
		t.Fatal("second claim must not win an existing key")
	}
	if existing.Status != IdempotencyCompleted {
		t.Fatalf("status = %q, want %q", existing.Status, IdempotencyCompleted)
	}
	if existing.ResponseCode != 201 {
		t.Fatalf("response code = %d, want 201", existing.ResponseCode)
	}
	if string(existing.ResponseBody) != string(body) {
		t.Fatalf("response body = %s, want %s", existing.ResponseBody, body)
	}
	if existing.RequestHash != "hashA" {
		t.Fatalf("request hash = %q, want %q", existing.RequestHash, "hashA")
	}
}
