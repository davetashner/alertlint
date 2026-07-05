package adapter

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Retry policy shared by the HTTP adapters. A 90-day fleet pull WILL be
// throttled (PagerDuty and Datadog aggressively rate-limit); without
// retries one 429 aborts the whole source, because errors are never empty
// results (provider-adapters.md §1).
const (
	// RetryMaxAttempts is the total number of tries per request.
	RetryMaxAttempts = 5
	// retryBaseDelay doubles per attempt: 0.5s, 1s, 2s, 4s.
	retryBaseDelay = 500 * time.Millisecond
	// retryMaxDelay caps any single wait, including Retry-After values —
	// a server asking for a 10-minute pause means the pull should fail
	// loudly, not hang.
	retryMaxDelay = 30 * time.Second
)

// retryableStatus: throttling and transient server errors only. Client
// errors (401/403/404) are real answers and never retried.
func retryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// sleepFn is swapped in tests to avoid real waits.
var sleepFn = time.Sleep

// DoWithRetry executes an idempotent GET with exponential backoff,
// honoring Retry-After (seconds form) when the server sends one. Retried
// response bodies are drained and closed so connections are reused. An
// exhausted budget returns the last response untouched — the adapter's
// status check then fails the pull loudly (never a silent short result).
func DoWithRetry(rt http.RoundTripper, req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet {
		return rt.RoundTrip(req) // only idempotent requests are retried
	}
	var resp *http.Response
	var err error
	for attempt := 0; attempt < RetryMaxAttempts; attempt++ {
		if attempt > 0 {
			sleepFn(retryDelay(attempt, resp))
			if resp != nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
		}
		resp, err = rt.RoundTrip(req)
		if err != nil {
			// Transport-level failures (connection reset, timeout) retry
			// on the same schedule.
			continue
		}
		if !retryableStatus(resp.StatusCode) {
			return resp, nil
		}
	}
	if err != nil {
		return nil, fmt.Errorf("after %d attempts: %w", RetryMaxAttempts, err)
	}
	return resp, nil // last throttled/5xx response; caller reports it
}

// retryDelay picks the wait before the given attempt (1-based backoff
// step), preferring the server's Retry-After when present and sane.
func retryDelay(attempt int, prev *http.Response) time.Duration {
	if prev != nil {
		if ra := prev.Header.Get("Retry-After"); ra != "" {
			if secs, err := strconv.Atoi(ra); err == nil && secs >= 0 {
				d := time.Duration(secs) * time.Second
				if d > retryMaxDelay {
					return retryMaxDelay
				}
				return d
			}
		}
	}
	d := retryBaseDelay << (attempt - 1)
	if d > retryMaxDelay {
		return retryMaxDelay
	}
	return d
}
