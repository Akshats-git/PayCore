package webhook

import (
	"math/rand"
	"testing"
	"time"
)

func TestSignVerify(t *testing.T) {
	secret := []byte("s3cr3t")
	payload := []byte(`{"type":"charge.succeeded","amount":50000}`)

	sig := Sign(secret, payload)

	if !Verify(secret, payload, sig) {
		t.Fatal("a valid signature should verify")
	}
	if Verify(secret, []byte(`{"amount":99999}`), sig) {
		t.Fatal("a tampered payload must not verify")
	}
	if Verify([]byte("wrong-secret"), payload, sig) {
		t.Fatal("the wrong secret must not verify")
	}
}

func TestNextBackoffGrowsAndCaps(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	base := time.Second
	maxDelay := 10 * time.Second

	// With equal jitter the result lies in [delay/2, delay], where
	// delay = min(maxDelay, base * 2^(attempt-1)).
	cases := []struct {
		attempt          int
		wantMin, wantMax time.Duration
	}{
		{1, 500 * time.Millisecond, 1 * time.Second},
		{2, 1 * time.Second, 2 * time.Second},
		{3, 2 * time.Second, 4 * time.Second},
		{10, 5 * time.Second, 10 * time.Second}, // capped
	}
	for _, tc := range cases {
		for i := 0; i < 100; i++ {
			d := nextBackoff(tc.attempt, base, maxDelay, rng)
			if d < tc.wantMin || d > tc.wantMax {
				t.Fatalf("attempt %d: backoff %v outside [%v, %v]", tc.attempt, d, tc.wantMin, tc.wantMax)
			}
		}
	}
}
