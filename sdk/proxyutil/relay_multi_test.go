package proxyutil

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// hostScriptedRT is a transport that answers by relay host: hosts listed in
// failHosts return a dial error, everything else returns an empty 200. It
// records every attempt so rotation and failover can be asserted without
// touching the network.
type hostScriptedRT struct {
	mu        sync.Mutex
	attempts  []string
	got       []*http.Request
	failHosts map[string]bool
}

func newHostScriptedRT(failHosts ...string) *hostScriptedRT {
	rt := &hostScriptedRT{failHosts: make(map[string]bool, len(failHosts))}
	for _, host := range failHosts {
		rt.failHosts[host] = true
	}
	return rt
}

func (s *hostScriptedRT) RoundTrip(req *http.Request) (*http.Response, error) {
	s.mu.Lock()
	s.attempts = append(s.attempts, req.URL.Host)
	s.got = append(s.got, req)
	shouldFail := s.failHosts[req.URL.Host]
	s.mu.Unlock()

	if shouldFail {
		return nil, errors.New("dial failure for " + req.URL.Host)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func (s *hostScriptedRT) recorded() ([]string, []*http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	attempts := append([]string(nil), s.attempts...)
	got := append([]*http.Request(nil), s.got...)
	return attempts, got
}

func TestWrapRelayWithRoundRobinStartsAtEachCandidate(t *testing.T) {
	t.Parallel()

	base := newHostScriptedRT()
	rt := WrapRelayWith(base, RelayOptions{
		URLs:     []string{"https://relay-a.example.workers.dev", "https://relay-b.example.workers.dev", "https://relay-c.example.workers.dev"},
		Strategy: RelayStrategyRoundRobin,
	})

	for range 6 {
		if _, err := rt.RoundTrip(newRelayTestRequest(t)); err != nil {
			t.Fatalf("round-trip failed: %v", err)
		}
	}

	attempts, _ := base.recorded()
	want := []string{
		"relay-a.example.workers.dev",
		"relay-b.example.workers.dev",
		"relay-c.example.workers.dev",
		"relay-a.example.workers.dev",
		"relay-b.example.workers.dev",
		"relay-c.example.workers.dev",
	}
	if len(attempts) != len(want) {
		t.Fatalf("attempts = %v, want %v", attempts, want)
	}
	for i := range want {
		if attempts[i] != want[i] {
			t.Fatalf("attempt %d = %q, want %q (all attempts: %v)", i, attempts[i], want[i], attempts)
		}
	}
}

func TestWrapRelayWithNoneAlwaysUsesFirstCandidate(t *testing.T) {
	t.Parallel()

	base := newHostScriptedRT()
	rt := WrapRelayWith(base, RelayOptions{
		URLs:     []string{"https://relay-a.example.workers.dev", "https://relay-b.example.workers.dev"},
		Strategy: RelayStrategyNone,
	})

	for range 4 {
		if _, err := rt.RoundTrip(newRelayTestRequest(t)); err != nil {
			t.Fatalf("round-trip failed: %v", err)
		}
	}

	attempts, _ := base.recorded()
	for i, host := range attempts {
		if host != "relay-a.example.workers.dev" {
			t.Fatalf("attempt %d = %q, want relay-a (all attempts: %v)", i, host, attempts)
		}
	}
}

func TestWrapRelayWithRandomSpreadsAcrossCandidates(t *testing.T) {
	t.Parallel()

	base := newHostScriptedRT()
	rt := WrapRelayWith(base, RelayOptions{
		URLs:     []string{"https://relay-a.example.workers.dev", "https://relay-b.example.workers.dev"},
		Strategy: RelayStrategyRandom,
	})

	const requests = 50
	seen := map[string]bool{}
	for range requests {
		if _, err := rt.RoundTrip(newRelayTestRequest(t)); err != nil {
			t.Fatalf("round-trip failed: %v", err)
		}
	}
	attempts, _ := base.recorded()
	for _, host := range attempts {
		seen[host] = true
	}
	// With 50 draws the chance of missing a candidate is (1/2)^50.
	if len(seen) != 2 {
		t.Fatalf("random strategy only reached %v, want both candidates", attempts)
	}
}

func TestWrapRelayWithFailsOverToNextCandidate(t *testing.T) {
	t.Parallel()

	base := newHostScriptedRT("relay-dead.example.workers.dev")
	rt := WrapRelayWith(base, RelayOptions{
		URLs: []string{
			"https://relay-dead.example.workers.dev",
			"https://relay-live.example.workers.dev",
		},
	})

	resp, err := rt.RoundTrip(newRelayTestRequest(t))
	if err != nil {
		t.Fatalf("expected failover to the live relay, got error: %v", err)
	}
	resp.Body.Close()

	attempts, _ := base.recorded()
	want := []string{"relay-dead.example.workers.dev", "relay-live.example.workers.dev"}
	if len(attempts) != len(want) {
		t.Fatalf("attempts = %v, want %v", attempts, want)
	}
	for i := range want {
		if attempts[i] != want[i] {
			t.Fatalf("attempt %d = %q, want %q (all attempts: %v)", i, attempts[i], want[i], attempts)
		}
	}
}

func TestWrapRelayWithDoesNotReplayBodyedRequests(t *testing.T) {
	t.Parallel()

	base := newHostScriptedRT("relay-dead.example.workers.dev")
	rt := WrapRelayWith(base, RelayOptions{
		URLs: []string{
			"https://relay-dead.example.workers.dev",
			"https://relay-live.example.workers.dev",
		},
	})

	req := newRelayTestRequest(t)
	req.Method = http.MethodPost
	req.Body = io.NopCloser(strings.NewReader(`{"prompt":"hi"}`))

	if _, err := rt.RoundTrip(req); err == nil {
		t.Fatal("expected the transport error to surface, got nil")
	}

	attempts, _ := base.recorded()
	// A body already handed to the base transport cannot be rewound: replaying
	// it would duplicate the streaming POST upstream.
	if len(attempts) != 1 || attempts[0] != "relay-dead.example.workers.dev" {
		t.Fatalf("attempts = %v, want exactly the first candidate", attempts)
	}
}

func TestWrapRelayWithStrictRejectsWhenNoCandidateIsConfigured(t *testing.T) {
	t.Parallel()

	base := newHostScriptedRT()
	rt := WrapRelayWith(base, RelayOptions{Strict: true})

	_, err := rt.RoundTrip(newRelayTestRequest(t))
	if !errors.Is(err, ErrRelayStrict) {
		t.Fatalf("error = %v, want ErrRelayStrict", err)
	}

	attempts, _ := base.recorded()
	if len(attempts) != 0 {
		t.Fatalf("strict mode leaked %v to the base transport", attempts)
	}
}

func TestWrapRelayWithStrictStillRelaysWhenCandidatesExist(t *testing.T) {
	t.Parallel()

	base := newHostScriptedRT()
	rt := WrapRelayWith(base, RelayOptions{
		URLs:     []string{"https://relay-a.example.workers.dev"},
		Strict:   true,
		Strategy: RelayStrategyRoundRobin,
	})

	resp, err := rt.RoundTrip(newRelayTestRequest(t))
	if err != nil {
		t.Fatalf("round-trip failed: %v", err)
	}
	resp.Body.Close()

	attempts, _ := base.recorded()
	if len(attempts) != 1 || attempts[0] != "relay-a.example.workers.dev" {
		t.Fatalf("attempts = %v, want the configured relay", attempts)
	}
}

func TestWrapRelayWithWithoutStrictLeavesEmptyListOnBase(t *testing.T) {
	t.Parallel()

	base := newHostScriptedRT()
	rt := WrapRelayWith(base, RelayOptions{})

	if _, err := rt.RoundTrip(newRelayTestRequest(t)); err != nil {
		t.Fatalf("expected the base transport to answer directly, got error: %v", err)
	}

	attempts, _ := base.recorded()
	if len(attempts) != 1 {
		t.Fatalf("attempts = %v, want exactly one direct attempt", attempts)
	}
}

func TestWrapRelayWithDropsInvalidAndDuplicateCandidates(t *testing.T) {
	t.Parallel()

	base := newHostScriptedRT()
	rt := WrapRelayWith(base, RelayOptions{
		URLs: []string{
			"",
			"   ",
			"not-a-url",
			"ftp://relay-ftp.example.workers.dev",
			"https://relay-a.example.workers.dev",
			"https://relay-a.example.workers.dev",
			"https://relay-b.example.workers.dev",
		},
		Strategy: RelayStrategyRoundRobin,
	})

	for range 4 {
		if _, err := rt.RoundTrip(newRelayTestRequest(t)); err != nil {
			t.Fatalf("round-trip failed: %v", err)
		}
	}

	attempts, _ := base.recorded()
	want := []string{
		"relay-a.example.workers.dev",
		"relay-b.example.workers.dev",
		"relay-a.example.workers.dev",
		"relay-b.example.workers.dev",
	}
	if len(attempts) != len(want) {
		t.Fatalf("attempts = %v, want %v", attempts, want)
	}
	for i := range want {
		if attempts[i] != want[i] {
			t.Fatalf("attempt %d = %q, want %q (all attempts: %v)", i, attempts[i], want[i], attempts)
		}
	}
}

func TestWrapRelayWithBypassesLoopbackTargetAcrossCandidates(t *testing.T) {
	t.Parallel()

	base := newHostScriptedRT()
	rt := WrapRelayWith(base, RelayOptions{
		URLs:     []string{"https://relay-a.example.workers.dev", "https://relay-b.example.workers.dev"},
		Strategy: RelayStrategyRoundRobin,
	})

	req, err := http.NewRequest(http.MethodGet, "https://localhost/v1/messages", nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("round-trip failed: %v", err)
	}

	attempts, _ := base.recorded()
	if len(attempts) != 1 || attempts[0] != "localhost" {
		t.Fatalf("attempts = %v, want a single direct attempt at localhost", attempts)
	}
}

func TestRelayIdentity(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		urls     []string
		strategy string
		strict   bool
		want     string
	}{
		{name: "disabled", want: ""},
		{name: "disabled with whitespace-only urls", urls: []string{"  ", ""}, want: ""},
		{name: "strict with no candidates", strict: true, want: "strict|none"},
		{
			name: "single relay without strategy",
			urls: []string{"https://relay-a.example.workers.dev"},
			want: "https://relay-a.example.workers.dev|",
		},
		{
			name:     "round-robin list",
			urls:     []string{"https://relay-a.example.workers.dev", "https://relay-b.example.workers.dev"},
			strategy: RelayStrategyRoundRobin,
			want:     "https://relay-a.example.workers.dev,https://relay-b.example.workers.dev|round-robin",
		},
		{
			name:   "list with strict suffix",
			urls:   []string{"https://relay-a.example.workers.dev"},
			strict: true,
			want:   "https://relay-a.example.workers.dev||strict",
		},
		{
			name: "invalid and duplicate entries collapse",
			urls: []string{"not-a-url", "https://relay-a.example.workers.dev", "https://relay-a.example.workers.dev"},
			want: "https://relay-a.example.workers.dev|",
		},
		{
			name:     "unknown strategy normalizes to none",
			urls:     []string{"https://relay-a.example.workers.dev"},
			strategy: "fastest",
			want:     "https://relay-a.example.workers.dev|",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := RelayIdentity(tc.urls, tc.strategy, tc.strict)
			if got != tc.want {
				t.Fatalf("RelayIdentity(%v, %q, %v) = %q, want %q", tc.urls, tc.strategy, tc.strict, got, tc.want)
			}
		})
	}
}

// newRelayTestRequest builds a bodyless HTTPS request to a non-loopback host,
// so it is eligible for relay rewriting and for failover replay.
func newRelayTestRequest(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "https://api.example.com/v1/messages?beta=true", nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	return req
}
