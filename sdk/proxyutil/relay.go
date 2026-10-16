package proxyutil

import (
	"errors"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
)

// Relay strategies accepted by RelayOptions.Strategy. Any other value,
// including the empty string, selects the first candidate.
const (
	RelayStrategyNone       = "none"
	RelayStrategyRoundRobin = "round-robin"
	RelayStrategyRandom     = "random"
)

// ErrRelayStrict is returned for external HTTPS requests when strict relay
// mode is enabled and no candidate relay is configured. Strict mode fails
// closed: an unrelayed request would egress from the local IP, which is
// exactly what the operator configured the relay to prevent.
var ErrRelayStrict = errors.New("relay strict mode is enabled but no relay URL is configured")

// relayBreakTTL is how long a relay that failed at its own platform (or was
// unreachable at the transport level) is skipped before requests try it
// again. Long enough to ride out a Vercel/Cloudflare throttle burst, short
// enough that a recovered relay rejoins the pool without operator action.
const relayBreakTTL = 30 * time.Second

// relayProxyHeader stamps every response an edge relay worker actually
// proxied, letting the breaker tell an answer the upstream provider really
// gave (the relay is healthy — only its target said no) from a 429/5xx the
// worker's own hosting platform produced (the relay itself is unhealthy).
// See relayHostFailure.
const relayProxyHeader = "x-relay-proxied"

// relayBreaker parks one relay for a window of time. The zero value is an
// open (healthy) breaker; all operations are safe for concurrent use.
type relayBreaker struct {
	untilNanos atomic.Int64
}

func (b *relayBreaker) parked(now time.Time) bool {
	return b.untilNanos.Load() > now.UnixNano()
}

// park closes the breaker for window and reports whether it opened the window
// just now — a transition, so callers can log the park once instead of on
// every request that lands on an already-dark relay.
func (b *relayBreaker) park(now time.Time, window time.Duration) bool {
	alreadyParked := b.parked(now)
	b.untilNanos.Store(now.Add(window).UnixNano())
	return !alreadyParked
}

// release opens the breaker and reports whether a window had been set, so a
// recovery is logged once rather than on every healthy response.
func (b *relayBreaker) release() bool {
	wasSet := b.untilNanos.Load() != 0
	b.untilNanos.Store(0)
	return wasSet
}

// RelayOptions describes the relays a transport may use and how a request
// picks among them.
type RelayOptions struct {
	// URLs are the candidate relays in priority order. Invalid entries are
	// dropped; an empty result disables relaying unless Strict is set.
	URLs []string
	// Strategy selects the candidate a request starts with. "" and "none"
	// always use the first URL, "round-robin" advances one slot per request,
	// and "random" picks a uniformly random slot. Remaining candidates are
	// still used as a failover tail.
	Strategy string
	// Strict turns an empty candidate list into a hard error for external
	// HTTPS requests instead of falling through to direct egress.
	Strict bool
}

type relayRoundTripper struct {
	base     http.RoundTripper
	relays   []*url.URL
	strategy string
	strict   bool
	counter  atomic.Uint64
	// breakers parks relays that failed at their own platform;
	// index-aligned with relays.
	breakers []relayBreaker
}

// WrapRelay rewrites external HTTPS requests through an edge relay worker
// using the x-relay-target/x-relay-path two-header protocol. Loopback hosts,
// non-HTTPS schemes, and requests already aimed at the relay pass through
// unchanged. An empty or invalid relayURL returns base untouched; a nil base
// uses http.DefaultTransport. A relay URL with a path prefix (e.g. a Netlify
// function path) is preserved: the relay request line becomes
// prefix + target-path?query. A query string or userinfo in relayURL is
// ignored — the target's query replaces it; keep relay URLs to scheme://host/path.
//
// This is the single-candidate form of WrapRelayWith.
func WrapRelay(base http.RoundTripper, relayURL string) http.RoundTripper {
	return WrapRelayWith(base, RelayOptions{URLs: []string{relayURL}})
}

// WrapRelayWith is WrapRelay for a candidate list. Each request is aimed at
// the candidate selected by Strategy; when that candidate fails at the
// transport level — or answers with a failure its own platform produced (see
// relayHostFailure) — it is parked for relayBreakTTL and the remaining
// candidates are tried in order, but only for bodyless requests: a request
// body already handed to the base transport cannot be rewound, so replaying
// it would duplicate a streaming POST upstream. Later requests skip parked
// candidates until the window elapses; if every candidate is parked the full
// list is used anyway, because parking must never become an outage (or leak
// a strict-mode request onto the local IP). An empty candidate list returns
// base untouched unless Strict is set, in which case the wrapper rejects
// external HTTPS requests.
func WrapRelayWith(base http.RoundTripper, opts RelayOptions) http.RoundTripper {
	relays := parseRelayURLs(opts.URLs)
	if len(relays) == 0 && !opts.Strict {
		return base
	}
	if base == nil {
		base = http.DefaultTransport
	}
	return &relayRoundTripper{
		base:     base,
		relays:   relays,
		strategy: normalizeRelayStrategy(opts.Strategy),
		strict:   opts.Strict,
		breakers: make([]relayBreaker, len(relays)),
	}
}

// RelayIdentity returns the canonical cache-key fragment describing relay
// egress. Two configurations resolving to the same candidates, strategy, and
// strict flag may share a transport; anything else must not, or a transport
// built for one relay setting would leak into another.
func RelayIdentity(urls []string, strategy string, strict bool) string {
	relays := parseRelayURLs(urls)
	if len(relays) == 0 {
		if !strict {
			return ""
		}
		return "strict|none"
	}
	ids := make([]string, 0, len(relays))
	for _, relay := range relays {
		ids = append(ids, relay.String())
	}
	key := strings.Join(ids, ",") + "|" + normalizeRelayStrategy(strategy)
	if strict {
		key += "|strict"
	}
	return key
}

func (r *relayRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL == nil {
		// Parity with http.Transport: the base reports the malformed request.
		return r.base.RoundTrip(req)
	}
	if !r.shouldRelay(req.URL) {
		return r.base.RoundTrip(req)
	}
	if len(r.relays) == 0 {
		if r.strict {
			return nil, ErrRelayStrict
		}
		return r.base.RoundTrip(req)
	}
	candidates := r.order()
	var lastErr error
	for i, idx := range candidates {
		resp, err := r.base.RoundTrip(r.rewrite(req, r.relays[idx]))
		if err != nil {
			lastErr = err
			// A caller that hung up is not the relay's fault: never park a
			// relay on behalf of a cancelled or expired request context.
			cancelled := req.Context().Err() != nil
			if !cancelled && r.breakers[idx].park(time.Now(), relayBreakTTL) {
				log.Warnf("relay %s parked for %s after a transport error: %v",
					r.relays[idx].Host, relayBreakTTL, err)
			}
			if i == len(candidates)-1 || !retryableRequest(req) || cancelled {
				break
			}
			continue
		}
		if relayHostFailure(resp) {
			// The relay's own platform throttled or failed it, so park it for
			// later requests. This request fails over only if it can: a
			// bodyless request replays cleanly against the next candidate, a
			// body already handed to the base transport cannot.
			if r.breakers[idx].park(time.Now(), relayBreakTTL) {
				log.Warnf("relay %s parked for %s after a platform failure: HTTP %d",
					r.relays[idx].Host, relayBreakTTL, resp.StatusCode)
			}
			if i < len(candidates)-1 && retryableRequest(req) {
				drainClose(resp)
				continue
			}
			return finishRelayResponse(resp), nil
		}
		// The relay answered — even if the answer is an upstream rate limit,
		// the relay itself is healthy, so it keeps its slot.
		if r.breakers[idx].release() {
			log.Infof("relay %s healthy again; breaker cleared", r.relays[idx].Host)
		}
		return finishRelayResponse(resp), nil
	}
	// order() never returns an empty list while relays are configured, so the
	// loop above always ends with either a returned response or a recorded
	// error; lastErr is non-nil whenever control reaches this line.
	return nil, lastErr
}

// relayHostFailure reports whether a 429/5xx came from the relay's own
// hosting platform rather than being forwarded from the upstream provider.
//
// A response stamped with x-relay-proxied was proxied by the worker: the
// provider answered, and no amount of relaying egress changes an account
// quota, so the relay stays in the pool. An unstamped failure shaped like an
// API payload (JSON/SSE) is a pre-stamp relay forwarding that same provider
// answer — also left alone. An unstamped failure with any other content type
// is a platform error page (Vercel concurrency 429, Cloudflare error 1015,
// a gateway HTML page): the relay is unhealthy and gets parked, which is
// exactly the case a bodyed POST cannot fail over out of on its own.
func relayHostFailure(resp *http.Response) bool {
	if resp == nil {
		return false
	}
	if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < http.StatusInternalServerError {
		return false
	}
	if resp.Header.Get(relayProxyHeader) != "" {
		return false
	}
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	return !strings.Contains(contentType, "json") && !strings.Contains(contentType, "event-stream")
}

// finishRelayResponse strips the worker's breaker stamp so the caller sees a
// response indistinguishable from a direct upstream reply.
func finishRelayResponse(resp *http.Response) *http.Response {
	if resp != nil && resp.Header != nil {
		resp.Header.Del(relayProxyHeader)
	}
	return resp
}

// drainClose discards a failure response being replaced by the next
// candidate, releasing its connection instead of leaking the body.
func drainClose(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
}

// rewrite re-points an outbound request at the given relay worker, carrying
// the original destination in the two protocol headers.
func (r *relayRoundTripper) rewrite(req *http.Request, relay *url.URL) *http.Request {
	clone := req.Clone(req.Context())
	if clone.Header == nil {
		clone.Header = make(http.Header)
	}
	clone.Header.Set("x-relay-target", req.URL.Scheme+"://"+req.URL.Host)
	clone.Header.Set("x-relay-path", req.URL.RequestURI())
	clone.URL.Scheme = relay.Scheme
	clone.URL.Host = relay.Host
	clone.Host = relay.Host
	clone.URL.Path = strings.TrimRight(relay.Path, "/") + req.URL.Path
	clone.URL.RawQuery = req.URL.RawQuery
	return clone
}

// order returns the indices of the candidates for this request, rotated per
// Strategy. Relays the breaker has parked are skipped; if every relay is
// parked the full rotated list comes back anyway, so a dark pool can never
// become an outage. The counter is shared by every request on the transport
// so round-robin spreads load rather than pinning a start slot.
func (r *relayRoundTripper) order() []int {
	n := len(r.relays)
	var start int
	switch r.strategy {
	case RelayStrategyRoundRobin:
		start = int((r.counter.Add(1) - 1) % uint64(n))
	case RelayStrategyRandom:
		start = rand.Intn(n)
	}
	now := time.Now()
	indices := make([]int, 0, n)
	for offset := 0; offset < n; offset++ {
		idx := (start + offset) % n
		if !r.breakers[idx].parked(now) {
			indices = append(indices, idx)
		}
	}
	if len(indices) > 0 {
		return indices
	}
	// Everything is parked: ignore the breakers rather than send nothing.
	for offset := 0; offset < n; offset++ {
		indices = append(indices, (start+offset)%n)
	}
	return indices
}

// retryableRequest reports whether the request can be replayed against a
// different relay after a transport error. A body already handed to the base
// transport cannot be rewound, so only bodyless requests are retried.
func retryableRequest(req *http.Request) bool {
	return req.Body == nil || req.Body == http.NoBody
}

// IsRelayRoundTripper reports whether rt was produced by WrapRelay/WrapRelayWith.
// Callers use it to detect an already-relayed transport (e.g. to avoid
// double-wrapping or to assert relay wiring in tests) without exporting the
// wrapper type.
func IsRelayRoundTripper(rt http.RoundTripper) bool {
	_, ok := rt.(*relayRoundTripper)
	return ok
}

func (r *relayRoundTripper) shouldRelay(target *url.URL) bool {
	if target.Scheme != "https" || target.Host == "" {
		return false
	}
	for _, relay := range r.relays {
		if strings.EqualFold(target.Host, relay.Host) {
			return false
		}
	}
	host := target.Hostname()
	if strings.EqualFold(host, "localhost") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return false
	}
	return true
}

// parseRelayURLs drops empty, malformed, and duplicate entries while
// preserving priority order.
func parseRelayURLs(urls []string) []*url.URL {
	relays := make([]*url.URL, 0, len(urls))
	seen := make(map[string]struct{}, len(urls))
	for _, raw := range urls {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		if _, duplicate := seen[trimmed]; duplicate {
			continue
		}
		relay, err := url.Parse(trimmed)
		if err != nil || (relay.Scheme != "http" && relay.Scheme != "https") || relay.Host == "" {
			continue
		}
		seen[trimmed] = struct{}{}
		relays = append(relays, relay)
	}
	return relays
}

func normalizeRelayStrategy(strategy string) string {
	switch strings.ToLower(strings.TrimSpace(strategy)) {
	case RelayStrategyRoundRobin:
		return RelayStrategyRoundRobin
	case RelayStrategyRandom:
		return RelayStrategyRandom
	default:
		return ""
	}
}
