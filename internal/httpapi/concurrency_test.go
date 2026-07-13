package httpapi_test

// This is the test that sells the whole project. It fires many identical charge
// requests at the real HTTP server — same idempotency key, all at once — and
// proves that exactly one charge lands in the ledger and every response is the
// same. It exercises the entire stack: HTTP handler → charge service → the
// atomic idempotency claim → Postgres. It is proof, not a claim, that the
// double-submit race is closed.

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/Akshats-git/PayCore/internal/accounts"
	"github.com/Akshats-git/PayCore/internal/charges"
	"github.com/Akshats-git/PayCore/internal/httpapi"
	"github.com/Akshats-git/PayCore/internal/ledger"
	"github.com/Akshats-git/PayCore/internal/storage"
	"github.com/Akshats-git/PayCore/migrations"
)

func TestConcurrentIdenticalChargesCreateExactlyOne(t *testing.T) {
	url := os.Getenv("PAYCORE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PAYCORE_TEST_DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()

	if err := storage.RunMigrations(url, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := storage.NewPostgresPool(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx,
		`TRUNCATE accounts, transactions, ledger_entries, idempotency_keys, outbox RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	// Wire the entire real stack: repositories → services → router → HTTP server.
	accountRepo := storage.NewAccountRepo(pool)
	ledgerRepo := storage.NewLedgerRepo(pool)
	idempotencyRepo := storage.NewIdempotencyRepo(pool)
	router := httpapi.NewRouter(httpapi.Deps{
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Ready:    func(context.Context) error { return nil },
		Accounts: accounts.NewService(accountRepo),
		Charges:  charges.NewService(pool, ledgerRepo, idempotencyRepo, storage.NewOutboxRepo(pool)),
	})
	server := httptest.NewServer(router)
	defer server.Close()

	// Two accounts to move money between.
	alice, err := accountRepo.Create(ctx, "alice", ledger.Liability, "INR")
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := accountRepo.Create(ctx, "bob", ledger.Liability, "INR")
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}

	const (
		n   = 50
		key = "the-one-idempotency-key"
	)
	body := fmt.Sprintf(`{"from_account":%d,"to_account":%d,"amount":50000,"currency":"INR"}`, alice.ID, bob.ID)

	type result struct {
		status int
		body   string
		err    error
	}
	results := make([]result, n)

	// A barrier so all n goroutines fire as close to simultaneously as possible,
	// maximizing the race on the idempotency claim.
	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			<-start

			req, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/charges", strings.NewReader(body))
			req.Header.Set("Idempotency-Key", key)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				results[i] = result{err: err}
				return
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			results[i] = result{status: resp.StatusCode, body: string(b)}
		}(i)
	}
	close(start) // release them all at once
	wg.Wait()

	// Every response must have succeeded (201) and be byte-for-byte identical:
	// the winner created the charge, everyone else replayed its stored response.
	first := results[0]
	for i, r := range results {
		if r.err != nil {
			t.Fatalf("request %d errored: %v", i, r.err)
		}
		if r.status != http.StatusCreated {
			t.Fatalf("request %d: status %d, want 201 (body=%s)", i, r.status, r.body)
		}
		if r.body != first.body {
			t.Fatalf("request %d response differs from the first:\n got: %s\nfirst: %s", i, r.body, first.body)
		}
	}

	// THE assertion: exactly one charge exists — one transaction, two entries.
	var txCount, entryCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM transactions`).Scan(&txCount); err != nil {
		t.Fatalf("count transactions: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ledger_entries`).Scan(&entryCount); err != nil {
		t.Fatalf("count entries: %v", err)
	}
	if txCount != 1 {
		t.Fatalf("EXPECTED EXACTLY ONE CHARGE after %d concurrent identical requests, got %d transactions", n, txCount)
	}
	if entryCount != 2 {
		t.Fatalf("expected exactly 2 ledger entries (one debit, one credit), got %d", entryCount)
	}

	// And the payee was credited exactly once.
	balance, err := accountRepo.Balance(ctx, bob.ID)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance != 50000 {
		t.Fatalf("payee balance = %d, want 50000 (credited exactly once)", balance)
	}

	t.Logf("%d concurrent identical requests → exactly 1 transaction, 2 entries, payee credited once; all %d responses identical", n, n)
}
