package proxyutil

import (
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// errScriptedRT answers by relay host: hosts in fail return failErr (or a plain
// error when failErr is nil), everything else returns an empty 200. It records
// each attempt's host and body so failover classification and body replay can be
// asserted without touching the network.
type errScriptedRT struct {
	mu       sync.Mutex
	attempts []string
	bodies   []string
	fail     map[string]bool
	failErr  error
}

func newErrScriptedRT(failErr error, failHosts ...string) *errScriptedRT {
	rt := &errScriptedRT{fail: make(map[string]bool, len(failHosts)), failErr: failErr}
	for _, host := range failHosts {
		rt.fail[host] = true
	}
	return rt
}

func (s *errScriptedRT) RoundTrip(req *http.Request) (*http.Response, error) {
	var body string
	if req.Body != nil {
		raw, _ := io.ReadAll(req.Body)
		body = string(raw)
	}
	s.mu.Lock()
	s.attempts = append(s.attempts, req.URL.Host)
	s.bodies = append(s.bodies, body)
	shouldFail := s.fail[req.URL.Host]
	err := s.failErr
	s.mu.Unlock()

	if shouldFail {
		if err == nil {
			err = errors.New("scripted failure for " + req.URL.Host)
		}
		return nil, err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func (s *errScriptedRT) recorded() ([]string, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	attempts := append([]string(nil), s.attempts...)
	bodies := append([]string(nil), s.bodies...)
	return attempts, bodies
}

// TestWrapRelayWithSharedRotatesAcrossTransports is the regression test for the
// production bug: client factories build a fresh wrapped transport per request,
// so rotation only advances if the start slot lives outside the wrapper.
func TestWrapRelayWithSharedRotatesAcrossTransports(t *testing.T) {
	t.Parallel()

	opts := RelayOptions{
		URLs: []string{
			"https://shared-rr-a.example.workers.dev",
			"https://shared-rr-b.example.workers.dev",
			"https://shared-rr-c.example.workers.dev",
		},
		Strategy: RelayStrategyRoundRobin,
	}
	base := newHostScriptedRT()

	// A brand-new wrapper each iteration, mirroring the per-request client
	// construction on the no-proxy path.
	for i := range 6 {
		rt := WrapRelayWithShared(base, opts)
		if _, err := rt.RoundTrip(newRelayTestRequest(t)); err != nil {
			t.Fatalf("round-trip %d failed: %v", i, err)
		}
	}

	attempts, _ := base.recorded()
	want := []string{
		"shared-rr-a.example.workers.dev",
		"shared-rr-b.example.workers.dev",
		"shared-rr-c.example.workers.dev",
		"shared-rr-a.example.workers.dev",
		"shared-rr-b.example.workers.dev",
		"shared-rr-c.example.workers.dev",
	}
	if len(attempts) != len(want) {
		t.Fatalf("attempts = %v, want %v", attempts, want)
	}
	for i := range want {
		if attempts[i] != want[i] {
			t.Fatalf("attempt %d = %q, want %q (all: %v)", i, attempts[i], want[i], attempts)
		}
	}
}

// TestWrapRelayWithKeepsStateIsolated locks in the contract that the
// non-shared constructor owns private state, so unit tests never interfere
// through the process-wide pool.
func TestWrapRelayWithKeepsStateIsolated(t *testing.T) {
	t.Parallel()

	opts := RelayOptions{
		URLs: []string{
			"https://isolated-a.example.workers.dev",
			"https://isolated-b.example.workers.dev",
		},
		Strategy: RelayStrategyRoundRobin,
	}
	base := newHostScriptedRT()

	for range 2 {
		rt := WrapRelayWith(base, opts)
		if _, err := rt.RoundTrip(newRelayTestRequest(t)); err != nil {
			t.Fatalf("round-trip failed: %v", err)
		}
	}

	attempts, _ := base.recorded()
	// Each transport starts at slot 0, so both requests hit candidate A.
	for i, host := range attempts {
		if host != "isolated-a.example.workers.dev" {
			t.Fatalf("attempt %d = %q, want isolated-a (all: %v)", i, host, attempts)
		}
	}
}

// TestWrapRelayWithSharedParksRelayAcrossTransports proves the breaker is shared
// too: a relay parked by one request is skipped by a later request even though
// that request holds a freshly built transport.
func TestWrapRelayWithSharedParksRelayAcrossTransports(t *testing.T) {
	t.Parallel()

	opts := RelayOptions{
		URLs: []string{
			"https://shared-park-dead.example.workers.dev",
			"https://shared-park-live.example.workers.dev",
		},
		Strategy: RelayStrategyRoundRobin,
	}
	base := newHostScriptedRT("shared-park-dead.example.workers.dev")

	// Request 1 starts on the dead relay, parks it, and fails over.
	rt1 := WrapRelayWithShared(base, opts)
	resp, err := rt1.RoundTrip(newRelayTestRequest(t))
	if err != nil {
		t.Fatalf("expected failover to the live relay, got: %v", err)
	}
	resp.Body.Close()

	// Request 2 (a separate transport) must not retry the parked relay.
	rt2 := WrapRelayWithShared(base, opts)
	resp2, err := rt2.RoundTrip(newRelayTestRequest(t))
	if err != nil {
		t.Fatalf("second round-trip failed: %v", err)
	}
	resp2.Body.Close()

	attempts, _ := base.recorded()
	want := []string{
		"shared-park-dead.example.workers.dev",
		"shared-park-live.example.workers.dev",
		"shared-park-live.example.workers.dev",
	}
	if len(attempts) != len(want) {
		t.Fatalf("attempts = %v, want %v", attempts, want)
	}
	for i := range want {
		if attempts[i] != want[i] {
			t.Fatalf("attempt %d = %q, want %q (all: %v)", i, attempts[i], want[i], attempts)
		}
	}
}

// TestWrapRelayWithSharedReplaysBodyedPostOnConnectError covers the industry
// standard retry rule: a POST whose body the caller made rewindable (GetBody) may
// fail over when the failure happened before the request was written — a DNS or
// connect error cannot have reached the upstream.
func TestWrapRelayWithSharedReplaysBodyedPostOnConnectError(t *testing.T) {
	t.Parallel()

	opts := RelayOptions{
		URLs: []string{
			"https://shared-post-dead.example.workers.dev",
			"https://shared-post-live.example.workers.dev",
		},
		Strategy: RelayStrategyRoundRobin,
	}
	dialErr := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connectex: access forbidden")}
	base := newErrScriptedRT(dialErr, "shared-post-dead.example.workers.dev")

	payload := `{"prompt":"hi"}`
	req, err := http.NewRequest(http.MethodPost, "https://api.example.com/v1/chat/completions", bytes.NewReader([]byte(payload)))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	if req.GetBody == nil {
		t.Fatal("precondition: bytes.Reader should set GetBody so the body is rewindable")
	}

	rt := WrapRelayWithShared(base, opts)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("expected failover on a connect error, got: %v", err)
	}
	resp.Body.Close()

	attempts, bodies := base.recorded()
	if len(attempts) != 2 {
		t.Fatalf("attempts = %v, want dead then live", attempts)
	}
	if attempts[0] != "shared-post-dead.example.workers.dev" || attempts[1] != "shared-post-live.example.workers.dev" {
		t.Fatalf("attempts = %v, want dead then live", attempts)
	}
	for i, body := range bodies {
		if body != payload {
			t.Fatalf("attempt %d body = %q, want the original payload replayed", i, body)
		}
	}
}

// TestWrapRelayWithSharedDoesNotReplayBodyedPostOnMidStreamError keeps the other
// half of the rule: a failure after the request may have been written is not
// replayed, because that could duplicate a streaming POST.
func TestWrapRelayWithSharedDoesNotReplayBodyedPostOnMidStreamError(t *testing.T) {
	t.Parallel()

	opts := RelayOptions{
		URLs: []string{
			"https://shared-mid-dead.example.workers.dev",
			"https://shared-mid-live.example.workers.dev",
		},
		Strategy: RelayStrategyRoundRobin,
	}
	base := newErrScriptedRT(errors.New("connection reset by peer"), "shared-mid-dead.example.workers.dev")

	req, err := http.NewRequest(http.MethodPost, "https://api.example.com/v1/chat/completions", bytes.NewReader([]byte(`{"prompt":"hi"}`)))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}

	rt := WrapRelayWithShared(base, opts)
	if _, err := rt.RoundTrip(req); err == nil {
		t.Fatal("expected the mid-stream error to surface, got nil")
	}

	attempts, _ := base.recorded()
	if len(attempts) != 1 || attempts[0] != "shared-mid-dead.example.workers.dev" {
		t.Fatalf("attempts = %v, want exactly the first candidate", attempts)
	}
}

// TestWrapRelayWithSharedReturnsBaseWhenRelayDisabled keeps zero-relay behavior
// byte-identical: no candidate and not strict returns the base untouched.
func TestWrapRelayWithSharedReturnsBaseWhenRelayDisabled(t *testing.T) {
	t.Parallel()

	base := newHostScriptedRT()
	if got := WrapRelayWithShared(base, RelayOptions{}); got != http.RoundTripper(base) {
		t.Fatalf("WrapRelayWithShared with no candidates = %T, want the base transport", got)
	}
	if got := WrapRelayWithShared(base, RelayOptions{URLs: []string{"not a url"}}); got != http.RoundTripper(base) {
		t.Fatalf("WrapRelayWithShared with only invalid candidates = %T, want the base transport", got)
	}
}
