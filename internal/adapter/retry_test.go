package adapter

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// scriptedTransport returns canned responses in order, recording sleeps.
type scriptedTransport struct {
	responses []*http.Response
	errs      []error
	calls     int
}

func (s *scriptedTransport) RoundTrip(*http.Request) (*http.Response, error) {
	i := s.calls
	s.calls++
	if i >= len(s.responses) {
		i = len(s.responses) - 1
	}
	return s.responses[i], s.errs[i]
}

func resp(code int, headers map[string]string) *http.Response {
	h := http.Header{}
	for k, v := range headers {
		h.Set(k, v)
	}
	return &http.Response{StatusCode: code, Header: h, Body: io.NopCloser(strings.NewReader("{}"))}
}

func captureSleeps(t *testing.T) *[]time.Duration {
	t.Helper()
	var sleeps []time.Duration
	orig := sleepFn
	sleepFn = func(d time.Duration) { sleeps = append(sleeps, d) }
	t.Cleanup(func() { sleepFn = orig })
	return &sleeps
}

func getReq(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "https://api.example.com/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestRetryOn429ThenSuccess(t *testing.T) {
	sleeps := captureSleeps(t)
	st := &scriptedTransport{
		responses: []*http.Response{resp(429, map[string]string{"Retry-After": "2"}), resp(200, nil)},
		errs:      []error{nil, nil},
	}
	r, err := DoWithRetry(st, getReq(t))
	if err != nil || r.StatusCode != 200 {
		t.Fatalf("resp = %v err = %v", r, err)
	}
	if st.calls != 2 {
		t.Errorf("calls = %d, want 2", st.calls)
	}
	if len(*sleeps) != 1 || (*sleeps)[0] != 2*time.Second {
		t.Errorf("sleeps = %v, want [2s] (Retry-After honored)", *sleeps)
	}
}

func TestExponentialBackoffWithoutRetryAfter(t *testing.T) {
	sleeps := captureSleeps(t)
	st := &scriptedTransport{
		responses: []*http.Response{resp(503, nil), resp(503, nil), resp(200, nil)},
		errs:      []error{nil, nil, nil},
	}
	r, err := DoWithRetry(st, getReq(t))
	if err != nil || r.StatusCode != 200 {
		t.Fatalf("resp = %v err = %v", r, err)
	}
	want := []time.Duration{500 * time.Millisecond, time.Second}
	if len(*sleeps) != 2 || (*sleeps)[0] != want[0] || (*sleeps)[1] != want[1] {
		t.Errorf("sleeps = %v, want %v", *sleeps, want)
	}
}

func TestExhaustedBudgetReturnsLastResponse(t *testing.T) {
	captureSleeps(t)
	st := &scriptedTransport{
		responses: []*http.Response{resp(429, nil)},
		errs:      []error{nil},
	}
	r, err := DoWithRetry(st, getReq(t))
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode != 429 {
		t.Fatalf("status = %d, want the final 429 for the caller to report", r.StatusCode)
	}
	if st.calls != RetryMaxAttempts {
		t.Errorf("calls = %d, want %d", st.calls, RetryMaxAttempts)
	}
}

func TestClientErrorsNeverRetried(t *testing.T) {
	sleeps := captureSleeps(t)
	st := &scriptedTransport{
		responses: []*http.Response{resp(403, nil)},
		errs:      []error{nil},
	}
	r, _ := DoWithRetry(st, getReq(t))
	if r.StatusCode != 403 || st.calls != 1 || len(*sleeps) != 0 {
		t.Errorf("403 must return immediately: calls=%d sleeps=%v", st.calls, *sleeps)
	}
}

func TestRetryAfterCapped(t *testing.T) {
	sleeps := captureSleeps(t)
	st := &scriptedTransport{
		responses: []*http.Response{resp(429, map[string]string{"Retry-After": "600"}), resp(200, nil)},
		errs:      []error{nil, nil},
	}
	if _, err := DoWithRetry(st, getReq(t)); err != nil {
		t.Fatal(err)
	}
	if (*sleeps)[0] != 30*time.Second {
		t.Errorf("sleep = %v, want capped 30s", (*sleeps)[0])
	}
}

func TestNonGETNotRetried(t *testing.T) {
	captureSleeps(t)
	st := &scriptedTransport{
		responses: []*http.Response{resp(503, nil)},
		errs:      []error{nil},
	}
	req, _ := http.NewRequest(http.MethodPost, "https://api.example.com/x", nil)
	r, _ := DoWithRetry(st, req)
	if r.StatusCode != 503 || st.calls != 1 {
		t.Errorf("POST must not retry: calls=%d", st.calls)
	}
}
