package proxyutil

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
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

// ---- relay breaker ----

const (
	throttledRelay = "relay-throttled.example.workers.dev"
	liveRelay      = "relay-live.example.workers.dev"
)

// relaySpec scripts how one relay host answers: status (0 means 200), content
// type, body, and whether the response carries a worker's x-relay-proxied
// stamp. Relays deployed before the stamp existed pass responses through raw.
type relaySpec struct {
	status      int
	contentType string
	stamped     bool
	body        string
}

// statusScriptedRT answers each relay host with its scripted response and
// records every attempt, so breaker decisions can be asserted without a
// network.
type statusScriptedRT struct {
	mu       sync.Mutex
	attempts []string
	specs    map[string]relaySpec
}

func newStatusScriptedRT(specs map[string]relaySpec) *statusScriptedRT {
	return &statusScriptedRT{specs: specs}
}

func (s *statusScriptedRT) RoundTrip(req *http.Request) (*http.Response, error) {
	s.mu.Lock()
	s.attempts = append(s.attempts, req.URL.Host)
	spec, known := s.specs[req.URL.Host]
	s.mu.Unlock()
	if !known {
		spec = relaySpec{}
	}
	status := spec.status
	if status == 0 {
		status = http.StatusOK
	}
	header := make(http.Header)
	if spec.contentType != "" {
		header.Set("Content-Type", spec.contentType)
	}
	if spec.stamped {
		header.Set("x-relay-proxied", "1")
	}
	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(spec.body)),
		Request:    req,
	}, nil
}

// set rewrites one host's script so a test can flip a relay from failing to
// healthy between requests.
func (s *statusScriptedRT) set(host string, spec relaySpec) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.specs[host] = spec
}

func (s *statusScriptedRT) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.attempts...)
}

// ctxFailingRT fails a request the way http.Transport does once the caller's
// context is done, and answers 200 otherwise.
type ctxFailingRT struct {
	mu       sync.Mutex
	attempts []string
}

func (s *ctxFailingRT) RoundTrip(req *http.Request) (*http.Response, error) {
	s.mu.Lock()
	s.attempts = append(s.attempts, req.URL.Host)
	s.mu.Unlock()
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func (s *ctxFailingRT) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.attempts...)
}

// expireBreak fast-forwards one relay's parked window — a deterministic
// stand-in for waiting out relayBreakTTL.
func expireBreak(t *testing.T, rt http.RoundTripper, host string) {
	t.Helper()
	relay, ok := rt.(*relayRoundTripper)
	if !ok {
		t.Fatalf("rt is %T, want *relayRoundTripper", rt)
	}
	for i, candidate := range relay.relays {
		if candidate.Host == host {
			relay.breakers[i].untilNanos.Store(time.Now().Add(-time.Second).UnixNano())
			return
		}
	}
	t.Fatalf("relay %s is not a candidate of this transport", host)
}

// attemptsEq asserts the exact sequence of relay hosts the base transport saw.
func attemptsEq(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("attempts = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("attempts = %v, want %v", got, want)
		}
	}
}

// newRelayTestPostRequest builds a body-carrying HTTPS request: its body
// cannot be replayed, so it is never failed over mid-request.
func newRelayTestPostRequest(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "https://api.example.com/v1/messages", strings.NewReader(`{"prompt":"hi"}`))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	return req
}

// A 429 the relay's own platform produced parks that relay: the bodyless
// request fails over, and the next request never touches the throttled host.
func TestRelayBreakerParksRelayAfterPlatformThrottle(t *testing.T) {
	t.Parallel()

	base := newStatusScriptedRT(map[string]relaySpec{
		throttledRelay: {status: http.StatusTooManyRequests, contentType: "text/html", body: "<html>Too Many Requests</html>"},
	})
	rt := WrapRelayWith(base, RelayOptions{
		URLs: []string{"https://" + throttledRelay, "https://" + liveRelay},
	})

	resp, err := rt.RoundTrip(newRelayTestRequest(t))
	if err != nil {
		t.Fatalf("expected failover to the live relay, got error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 after failover", resp.StatusCode)
	}

	resp, err = rt.RoundTrip(newRelayTestRequest(t))
	if err != nil {
		t.Fatalf("second round-trip failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 from the live relay", resp.StatusCode)
	}

	attemptsEq(t, base.recorded(), throttledRelay, liveRelay, liveRelay)
}

// A streaming POST cannot be replayed, so this request hands back the
// platform 429 — but the relay it came from is parked, and the next POST
// skips it outright instead of burning another body on a dead host.
func TestRelayBreakerSkipsParkedRelayOnNextPost(t *testing.T) {
	t.Parallel()

	base := newStatusScriptedRT(map[string]relaySpec{
		throttledRelay: {status: http.StatusTooManyRequests, contentType: "text/html", body: "<html>Too Many Requests</html>"},
	})
	rt := WrapRelayWith(base, RelayOptions{
		URLs: []string{"https://" + throttledRelay, "https://" + liveRelay},
	})

	resp, err := rt.RoundTrip(newRelayTestPostRequest(t))
	if err != nil {
		t.Fatalf("round-trip failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want the platform 429 passthrough", resp.StatusCode)
	}

	resp, err = rt.RoundTrip(newRelayTestPostRequest(t))
	if err != nil {
		t.Fatalf("second round-trip failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 from the live relay", resp.StatusCode)
	}

	attemptsEq(t, base.recorded(), throttledRelay, liveRelay)
}

// An upstream rate limit is not the relay's fault — the relay forwarded a
// real answer — so the pool must keep it, stamped or not, POST after POST.
func TestRelayBreakerLeavesUpstreamRateLimitOnTheRelay(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		spec relaySpec
	}{
		{
			name: "stamped worker forwards upstream 429",
			spec: relaySpec{status: http.StatusTooManyRequests, contentType: "application/json", stamped: true, body: `{"error":{"type":"FreeUsageLimitError"}}`},
		},
		{
			name: "pre-stamp worker forwards upstream 429",
			spec: relaySpec{status: http.StatusTooManyRequests, contentType: "application/json", body: `{"error":{"type":"FreeUsageLimitError"}}`},
		},
		{
			name: "pre-stamp worker forwards upstream 503 stream",
			spec: relaySpec{status: http.StatusServiceUnavailable, contentType: "text/event-stream"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			base := newStatusScriptedRT(map[string]relaySpec{throttledRelay: tc.spec})
			rt := WrapRelayWith(base, RelayOptions{
				URLs: []string{"https://" + throttledRelay, "https://" + liveRelay},
			})

			for range 2 {
				resp, err := rt.RoundTrip(newRelayTestPostRequest(t))
				if err != nil {
					t.Fatalf("round-trip failed: %v", err)
				}
				if resp.StatusCode != tc.spec.status {
					t.Fatalf("status = %d, want the upstream status %d passed through", resp.StatusCode, tc.spec.status)
				}
				// The worker's stamp is the breaker's business, not the caller's.
				if got := resp.Header.Get("x-relay-proxied"); got != "" {
					t.Fatalf("x-relay-proxied = %q, want it stripped", got)
				}
				resp.Body.Close()
			}

			// The relay was never parked: both requests started at it.
			attemptsEq(t, base.recorded(), throttledRelay, throttledRelay)
		})
	}
}

// When every relay looks unhealthy the breaker fails open instead of turning
// a bad moment into a dark pool, and a success releases the recovered relay.
func TestRelayBreakerFailsOpenWhenEveryRelayIsParked(t *testing.T) {
	t.Parallel()

	platform := relaySpec{status: http.StatusServiceUnavailable, contentType: "text/html", body: "<html>error</html>"}
	base := newStatusScriptedRT(map[string]relaySpec{
		throttledRelay: platform,
		liveRelay:      platform,
	})
	rt := WrapRelayWith(base, RelayOptions{
		URLs: []string{"https://" + throttledRelay, "https://" + liveRelay},
	})

	// The last candidate's failure is what the caller sees, body intact.
	resp, err := rt.RoundTrip(newRelayTestRequest(t))
	if err != nil {
		t.Fatalf("round-trip failed: %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want the last relay's 503", resp.StatusCode)
	}
	if body, _ := io.ReadAll(resp.Body); string(body) != "<html>error</html>" {
		t.Fatalf("body = %q, want the last relay's body (returned open, not drained)", body)
	}
	resp.Body.Close()

	// Both relays are parked now — the pool must not go dark.
	base.set(throttledRelay, relaySpec{})
	base.set(liveRelay, relaySpec{})
	resp, err = rt.RoundTrip(newRelayTestRequest(t))
	if err != nil {
		t.Fatalf("fail-open round-trip failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 from the fail-open attempt", resp.StatusCode)
	}

	// The success released that relay; the other stays parked until its
	// window elapses, so rotation keeps hitting the recovered one.
	resp, err = rt.RoundTrip(newRelayTestRequest(t))
	if err != nil {
		t.Fatalf("third round-trip failed: %v", err)
	}
	resp.Body.Close()

	attemptsEq(t, base.recorded(), throttledRelay, liveRelay, throttledRelay, throttledRelay)
}

// A parked relay rejoins the pool once its window elapses.
func TestRelayBreakerReleasesRelayAfterWindowExpires(t *testing.T) {
	t.Parallel()

	base := newStatusScriptedRT(map[string]relaySpec{
		throttledRelay: {status: http.StatusTooManyRequests, contentType: "text/html"},
	})
	rt := WrapRelayWith(base, RelayOptions{
		URLs: []string{"https://" + throttledRelay, "https://" + liveRelay},
	})

	resp, err := rt.RoundTrip(newRelayTestRequest(t))
	if err != nil {
		t.Fatalf("round-trip failed: %v", err)
	}
	resp.Body.Close()
	resp, err = rt.RoundTrip(newRelayTestRequest(t))
	if err != nil {
		t.Fatalf("second round-trip failed: %v", err)
	}
	resp.Body.Close()
	attemptsEq(t, base.recorded(), throttledRelay, liveRelay, liveRelay)

	// Fast-forward past relayBreakTTL: the relay is tried again.
	expireBreak(t, rt, throttledRelay)
	resp, err = rt.RoundTrip(newRelayTestRequest(t))
	if err != nil {
		t.Fatalf("third round-trip failed: %v", err)
	}
	resp.Body.Close()
	attemptsEq(t, base.recorded(), throttledRelay, liveRelay, liveRelay, throttledRelay, liveRelay)
}

// A transport-level failure parks the relay too, so later requests go
// straight to the live host instead of paying the dial error again.
func TestRelayBreakerParksRelayAfterTransportError(t *testing.T) {
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

	resp, err = rt.RoundTrip(newRelayTestRequest(t))
	if err != nil {
		t.Fatalf("second round-trip failed: %v", err)
	}
	resp.Body.Close()

	attempts, _ := base.recorded()
	attemptsEq(t, attempts,
		"relay-dead.example.workers.dev", "relay-live.example.workers.dev", "relay-live.example.workers.dev")
}

// A caller that hung up is not evidence against the relay: the cancellation
// surfaces as an error without parking anyone.
func TestRelayBreakerDoesNotParkForCancelledCaller(t *testing.T) {
	t.Parallel()

	base := &ctxFailingRT{}
	rt := WrapRelayWith(base, RelayOptions{
		URLs: []string{"https://" + throttledRelay, "https://" + liveRelay},
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.example.com/v1/messages", nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	if _, err := rt.RoundTrip(req); err == nil {
		t.Fatal("expected the caller's cancellation to surface, got nil")
	}

	// Had the cancelled call parked the first relay, this healthy request
	// would have started at the second one instead.
	resp, err := rt.RoundTrip(newRelayTestRequest(t))
	if err != nil {
		t.Fatalf("second round-trip failed: %v", err)
	}
	resp.Body.Close()

	attemptsEq(t, base.recorded(), throttledRelay, throttledRelay)
}

// The park predicate: platform error pages park, provider answers never do.
func TestRelayHostFailureMatrix(t *testing.T) {
	t.Parallel()

	platformFailure := func(status int, contentType string, stamped bool) *http.Response {
		header := make(http.Header)
		if contentType != "" {
			header.Set("Content-Type", contentType)
		}
		if stamped {
			header.Set("x-relay-proxied", "1")
		}
		return &http.Response{StatusCode: status, Header: header, Body: http.NoBody}
	}

	for _, tc := range []struct {
		name string
		resp *http.Response
		want bool
	}{
		{name: "nil response", resp: nil, want: false},
		{name: "platform html 429", resp: platformFailure(http.StatusTooManyRequests, "text/html", false), want: true},
		{name: "platform html 500", resp: platformFailure(http.StatusInternalServerError, "text/html", false), want: true},
		{name: "platform without content type", resp: platformFailure(http.StatusServiceUnavailable, "", false), want: true},
		{name: "stamped upstream 429", resp: platformFailure(http.StatusTooManyRequests, "application/json", true), want: false},
		{name: "stamped html passthrough", resp: platformFailure(http.StatusTooManyRequests, "text/html", true), want: false},
		{name: "pre-stamp upstream 429", resp: platformFailure(http.StatusTooManyRequests, "application/json", false), want: false},
		{name: "pre-stamp upstream problem+json", resp: platformFailure(http.StatusServiceUnavailable, "application/problem+json", false), want: false},
		{name: "pre-stamp upstream sse", resp: platformFailure(http.StatusServiceUnavailable, "text/event-stream", false), want: false},
		{name: "worker fetch failure 502 json", resp: platformFailure(http.StatusBadGateway, "application/json", false), want: false},
		{name: "upstream auth 403", resp: platformFailure(http.StatusForbidden, "text/html", false), want: false},
		{name: "upstream not found", resp: platformFailure(http.StatusNotFound, "text/html", false), want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			status := 0
			if tc.resp != nil {
				status = tc.resp.StatusCode
			}
			if got := relayHostFailure(tc.resp); got != tc.want {
				t.Fatalf("relayHostFailure(status=%d) = %v, want %v", status, got, tc.want)
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
