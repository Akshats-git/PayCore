// Package ratelimit implements a token-bucket rate limiter backed by Redis.
//
// Each key (e.g. a client IP or API key) has a bucket of up to `capacity` tokens
// that refills at `rate` tokens per second. Every request costs one token; when
// the bucket is empty, requests are rejected. A bucket lets a client burst up to
// `capacity` and then settle to a steady `rate` — forgiving, not a hard wall.
package ratelimit

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// tokenBucketScript performs the whole check-refill-decrement in one shot.
// Redis runs a Lua script atomically (single-threaded), so two concurrent
// requests can never both read the same token count and both spend it — the
// same atomicity guarantee the idempotency claim gets from a unique index.
//
//	KEYS[1] = bucket key
//	ARGV    = capacity, rate (tokens/sec), now (ms), requested
//	returns = {allowed (0|1), remaining tokens (string)}
const tokenBucketScript = `
local capacity  = tonumber(ARGV[1])
local rate      = tonumber(ARGV[2])
local now_ms    = tonumber(ARGV[3])
local requested = tonumber(ARGV[4])

local state  = redis.call('HMGET', KEYS[1], 'tokens', 'ts')
local tokens = tonumber(state[1])
local ts     = tonumber(state[2])
if tokens == nil then
  tokens = capacity
  ts = now_ms
end

local elapsed = (now_ms - ts) / 1000.0
if elapsed < 0 then elapsed = 0 end
tokens = math.min(capacity, tokens + elapsed * rate)

local allowed = 0
if tokens >= requested then
  tokens = tokens - requested
  allowed = 1
end

redis.call('HSET', KEYS[1], 'tokens', tokens, 'ts', now_ms)
local ttl = 60
if rate > 0 then ttl = math.ceil(capacity / rate) + 1 end
redis.call('EXPIRE', KEYS[1], ttl)

return {allowed, tostring(tokens)}
`

// Result is the outcome of a single rate-limit check.
type Result struct {
	Allowed    bool
	Remaining  float64
	RetryAfter time.Duration // when not allowed, how long until a token is available
}

// Limiter is a Redis-backed token-bucket rate limiter.
type Limiter struct {
	rdb      *redis.Client
	script   *redis.Script
	capacity float64
	rate     float64          // tokens per second
	now      func() time.Time // injectable clock for deterministic tests
}

// NewLimiter creates a limiter where each key allows bursts of up to capacity
// requests and refills at refillPerSec tokens per second.
func NewLimiter(rdb *redis.Client, capacity int, refillPerSec float64) *Limiter {
	return &Limiter{
		rdb:      rdb,
		script:   redis.NewScript(tokenBucketScript),
		capacity: float64(capacity),
		rate:     refillPerSec,
		now:      time.Now,
	}
}

// Allow charges one token to key's bucket and reports whether the request may
// proceed.
func (l *Limiter) Allow(ctx context.Context, key string) (Result, error) {
	nowMs := l.now().UnixMilli()
	raw, err := l.script.Run(ctx, l.rdb,
		[]string{"ratelimit:" + key},
		l.capacity, l.rate, nowMs, 1,
	).Result()
	if err != nil {
		return Result{}, fmt.Errorf("ratelimit: run script: %w", err)
	}

	arr, ok := raw.([]interface{})
	if !ok || len(arr) != 2 {
		return Result{}, fmt.Errorf("ratelimit: unexpected script result %v", raw)
	}
	allowed, _ := arr[0].(int64)
	remaining, _ := strconv.ParseFloat(fmt.Sprint(arr[1]), 64)

	res := Result{Allowed: allowed == 1, Remaining: remaining}
	if !res.Allowed && l.rate > 0 {
		secs := (1 - remaining) / l.rate
		res.RetryAfter = time.Duration(secs * float64(time.Second))
	}
	return res, nil
}
