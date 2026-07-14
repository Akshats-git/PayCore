# PayCore

[![CI](https://github.com/Akshats-git/PayCore/actions/workflows/ci.yml/badge.svg)](https://github.com/Akshats-git/PayCore/actions/workflows/ci.yml)

A small payment-processing engine written in Go. It moves money between accounts
on a double-entry ledger and focuses on the properties that matter for handling
money: correctness under concurrency, safe retries, and predictable behaviour
under load.

## Features

- **Double-entry ledger** — every charge is a balanced transfer (debits equal
  credits). The invariant is enforced by the database itself, and money is stored
  as integer minor units (paise/cents), never floating point.
- **Idempotent charges** — each charge carries an `Idempotency-Key`. Retries and
  concurrent duplicates resolve to exactly one charge, and the original response
  is replayed byte-for-byte.
- **Rate limiting** — per-client token bucket backed by Redis, returning `429`
  with a `Retry-After` header.
- **Load shedding** — an in-flight request limit that sheds lower-priority traffic
  first when the service is overloaded.
- **Webhooks** — charge events are written to a transactional outbox in the same
  transaction as the charge, then delivered by a background worker with HMAC
  signatures, exponential backoff + jitter, and a dead-letter queue.
- **Risk scoring** — an inline fraud model scores each charge before it is posted,
  wrapped in a latency budget and a circuit breaker so a slow or unavailable
  scorer degrades gracefully instead of blocking the charge path.

## Architecture

The service is organised in layers, each depending only on the one below it:

```
HTTP handlers (internal/httpapi)
        │
application services (internal/accounts, internal/charges)
        │
storage / repositories (internal/storage)   pure domain (internal/ledger)
        │
   PostgreSQL, Redis
```

Cross-cutting concerns live in their own packages: `internal/ratelimit`,
`internal/risk`, and `internal/webhook`.

## Requirements

- Go 1.26+
- Docker (for local PostgreSQL and Redis)
- Python 3 with `numpy` and `scikit-learn` — only needed to retrain the risk
  model; the trained artifact is committed, so the service runs without Python.

## Getting started

Bring up PostgreSQL and Redis:

```bash
docker compose up -d
```

Run the service (schema migrations are applied automatically on startup):

```bash
go run ./cmd/server
```

Check it's up:

```bash
curl -s localhost:8080/healthz
# {"status":"ok"}
```

## API

### Accounts

```bash
# Create an account (type is "asset" or "liability")
curl -s -X POST localhost:8080/v1/accounts \
  -d '{"name":"alice","type":"liability","currency":"INR"}'
# {"id":1,"name":"alice","type":"liability","currency":"INR","balance":0}

# Fetch an account (balance is derived from the ledger)
curl -s localhost:8080/v1/accounts/1
```

### Charges

Amounts are in minor units (e.g. `50000` = ₹500.00). Every charge requires an
`Idempotency-Key` header.

```bash
curl -s -X POST localhost:8080/v1/charges \
  -H 'Idempotency-Key: 7c1f...' \
  -d '{"from_account":1,"to_account":2,"amount":50000,"currency":"INR"}'
# {"id":1,"from_account":1,"to_account":2,"amount":50000,"currency":"INR","status":"succeeded",...}

# Fetch a charge
curl -s localhost:8080/v1/charges/1
```

A charge rejected by the risk model returns `402` with a declined body:

```json
{"status":"declined","reason":"model risk score above threshold","risk_score":79}
```

### Health

| Endpoint | Purpose |
|---|---|
| `GET /healthz` | Liveness — the process is running. Touches no dependencies. |
| `GET /readyz`  | Readiness — PostgreSQL and Redis are reachable. |

## Configuration

All configuration comes from the environment; defaults target the local Docker
setup.

| Variable | Default | Description |
|---|---|---|
| `PAYCORE_ADDR` | `:8080` | HTTP listen address |
| `PAYCORE_DATABASE_URL` | `postgres://paycore:paycore@localhost:5433/paycore?sslmode=disable` | PostgreSQL DSN |
| `PAYCORE_REDIS_URL` | `redis://localhost:6379/0` | Redis URL |
| `PAYCORE_RATE_LIMIT_CAPACITY` | `20` | Per-client token-bucket size (burst) |
| `PAYCORE_RATE_LIMIT_REFILL_PER_SEC` | `10` | Bucket refill rate (tokens/second) |
| `PAYCORE_LOAD_SHED_MAX_INFLIGHT` | `100` | In-flight limit before shedding non-critical traffic |
| `PAYCORE_WEBHOOK_URL` | *(empty)* | Webhook destination; empty disables delivery |
| `PAYCORE_WEBHOOK_SECRET` | `dev-webhook-secret` | HMAC signing secret |
| `PAYCORE_RISK_LATENCY_BUDGET` | `50ms` | Max time the scorer gets per charge |
| `PAYCORE_RISK_BREAKER_THRESHOLD` | `5` | Consecutive scorer failures that trip the breaker |
| `PAYCORE_RISK_BREAKER_COOLDOWN` | `5s` | How long the breaker stays open |
| `PAYCORE_RISK_BLOCK_AT_OR_ABOVE` | `500000` | Block threshold for the fallback rule scorer (minor units) |

## Testing

Unit tests run without any external services:

```bash
go test ./...
```

Integration tests exercise the real database and Redis. They are skipped unless
their connection URLs are set, and they should be run serially with the race
detector:

```bash
export PAYCORE_TEST_DATABASE_URL="postgres://paycore:paycore@localhost:5433/paycore?sslmode=disable"
export PAYCORE_TEST_REDIS_URL="redis://localhost:6379/0"
go test -race -p 1 ./...
```

CI runs the full suite against PostgreSQL and Redis service containers on every
push.

## Risk model

The fraud model is trained offline in Python and served in-process in Go. The
training script writes a JSON artifact (feature scaling, coefficients, threshold)
that the Go binary embeds and interprets — no Python is involved at runtime.

```bash
python3 scripts/train_fraud_model.py
# writes internal/risk/model/fraud_model.json
```

## Project layout

```
cmd/server/          service entry point and wiring
internal/ledger/     pure domain: money, accounts, double-entry transactions
internal/storage/    PostgreSQL/Redis adapters and migrations runner
internal/accounts/   account application service
internal/charges/    charge application service (idempotency, outbox, risk gate)
internal/httpapi/    HTTP handlers, routing, middleware
internal/ratelimit/  Redis token-bucket rate limiter
internal/risk/       fraud scoring: model, circuit breaker, latency-budget guard
internal/webhook/    signed webhook delivery worker
internal/config/     environment-based configuration
migrations/          embedded SQL schema migrations
scripts/             offline model training
```

## License

No license has been chosen yet. Add a `LICENSE` file to set usage terms.
