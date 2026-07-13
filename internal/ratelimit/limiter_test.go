package ratelimit

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// testRedis connects to the Redis named by PAYCORE_TEST_REDIS_URL, skipping the
// test if it isn't set.
func testRedis(t *testing.T) *redis.Client {
	t.Helper()
	url := os.Getenv("PAYCORE_TEST_REDIS_URL")
	if url == "" {
		t.Skip("PAYCORE_TEST_REDIS_URL not set; skipping Redis integration test")
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatalf("parse redis url: %v", err)
	}
	rdb := redis.NewClient(opts)
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("ping redis: %v", err)
	}
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

// uniqueKey keeps tests independent even against a shared Redis (buckets also
// carry a TTL, so they clean themselves up).
func uniqueKey(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

func TestLimiterTokenBucket(t *testing.T) {
	l := NewLimiter(testRedis(t), 3, 1) // burst 3, refill 1/sec
	clock := time.Now()
	l.now = func() time.Time { return clock } // freeze time; we advance it by hand
	ctx := context.Background()
	key := uniqueKey("bucket")

	// A burst of 3 is allowed.
	for i := 1; i <= 3; i++ {
		res, err := l.Allow(ctx, key)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		if !res.Allowed {
			t.Fatalf("request %d should be allowed (burst of 3)", i)
		}
	}

	// The 4th is denied, with a positive Retry-After.
	res, err := l.Allow(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if res.Allowed {
		t.Fatal("4th request should be denied")
	}
	if res.RetryAfter <= 0 {
		t.Fatalf("expected a positive RetryAfter, got %v", res.RetryAfter)
	}

	// Two seconds later, two tokens have refilled: two more allowed, then denied.
	clock = clock.Add(2 * time.Second)
	for i := 1; i <= 2; i++ {
		if res, _ := l.Allow(ctx, key); !res.Allowed {
			t.Fatalf("post-refill request %d should be allowed", i)
		}
	}
	if res, _ := l.Allow(ctx, key); res.Allowed {
		t.Fatal("should be denied again after the two refilled tokens are spent")
	}
}

// TestLimiterConcurrentIsAtomic fires many requests at one bucket simultaneously
// and asserts exactly `capacity` are allowed — proving the Lua check-decrement
// can't over-admit under concurrency.
func TestLimiterConcurrentIsAtomic(t *testing.T) {
	const capacity = 10
	l := NewLimiter(testRedis(t), capacity, 1)
	clock := time.Now()
	l.now = func() time.Time { return clock } // frozen: no refill during the burst
	ctx := context.Background()
	key := uniqueKey("concurrent")

	const n = 50
	var (
		allowed atomic.Int64
		wg      sync.WaitGroup
	)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			res, err := l.Allow(ctx, key)
			if err != nil {
				t.Errorf("allow: %v", err)
				return
			}
			if res.Allowed {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()

	if got := allowed.Load(); got != capacity {
		t.Fatalf("with capacity %d, exactly %d of %d concurrent requests should be allowed, got %d", capacity, capacity, n, got)
	}
}
