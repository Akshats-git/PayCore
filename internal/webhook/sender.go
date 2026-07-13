package webhook

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
)

// SignatureHeader carries the HMAC signature of the payload.
const SignatureHeader = "X-PayCore-Signature"

// Sender delivers one webhook payload. It returns nil on success and an error on
// any failure (a network problem or a non-2xx response), which the worker treats
// as "retry later".
type Sender interface {
	Send(ctx context.Context, payload []byte) error
}

// HTTPSender POSTs payloads to a fixed URL, signing each with an HMAC secret.
type HTTPSender struct {
	url    string
	secret []byte
	client *http.Client
}

// NewHTTPSender returns an HTTPSender that delivers to url, signing with secret.
func NewHTTPSender(url string, secret []byte, client *http.Client) *HTTPSender {
	return &HTTPSender{url: url, secret: secret, client: client}
}

func (s *HTTPSender) Send(ctx context.Context, payload []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(SignatureHeader, Sign(s.secret, payload))

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("deliver webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook receiver returned %d", resp.StatusCode)
	}
	return nil
}
