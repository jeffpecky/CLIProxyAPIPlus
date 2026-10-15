package proxyutil

import (
	"errors"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
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
// transport level the remaining candidates are tried in order, but only for
// bodyless requests — a request body already handed to the base transport
// cannot be rewound, so replaying it would duplicate a streaming POST
// upstream. An empty candidate list returns base untouched unless Strict is
// set, in which case the wrapper rejects external HTTPS requests.
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
	for i, relay := range candidates {
		resp, err := r.base.RoundTrip(r.rewrite(req, relay))
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if i == len(candidates)-1 || !retryableRequest(req) {
			break
		}
	}
	return nil, lastErr
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

// order returns the candidates for this request, rotated per Strategy. The
// counter is shared by every request on the transport so round-robin spreads
// load rather than pinning a start slot.
func (r *relayRoundTripper) order() []*url.URL {
	n := len(r.relays)
	switch r.strategy {
	case RelayStrategyRoundRobin:
		return rotate(r.relays, int((r.counter.Add(1)-1)%uint64(n)))
	case RelayStrategyRandom:
		return rotate(r.relays, rand.Intn(n))
	default:
		return r.relays
	}
}

func rotate(relays []*url.URL, start int) []*url.URL {
	if start <= 0 {
		return relays
	}
	rotated := make([]*url.URL, 0, len(relays))
	rotated = append(rotated, relays[start:]...)
	return append(rotated, relays[:start]...)
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
