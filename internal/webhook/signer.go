// Package webhook delivers outbox events to a receiver over HTTP: it signs each
// payload with an HMAC, retries failures with exponential backoff and jitter, and
// dead-letters events that never succeed.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// Sign returns the HMAC-SHA256 of payload, formatted as "sha256=<hex>", using
// secret. A receiver recomputes this over the raw body it received to verify the
// webhook genuinely came from us and wasn't tampered with in transit — like a wax
// seal that proves a letter wasn't opened and swapped.
func Sign(secret, payload []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// Verify reports whether signature equals Sign(secret, payload). It uses a
// constant-time comparison so an attacker can't learn the correct signature byte
// by byte from timing.
func Verify(secret, payload []byte, signature string) bool {
	expected := Sign(secret, payload)
	return hmac.Equal([]byte(expected), []byte(signature))
}
