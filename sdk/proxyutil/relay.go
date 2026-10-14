package proxyutil

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

type relayRoundTripper struct {
	base  http.RoundTripper
	relay *url.URL
}

// WrapRelay rewrites external HTTPS requests through an edge relay worker
// using the x-relay-target/x-relay-path two-header protocol. Loopback hosts,
// non-HTTPS schemes, and requests already aimed at the relay pass through
// unchanged. An empty or invalid relayURL returns base untouched. A relay URL
// with a path prefix (e.g. a Netlify function path) is preserved: the relay
// request line becomes prefix + target-path?query.
func WrapRelay(base http.RoundTripper, relayURL string) http.RoundTripper {
	raw := strings.TrimSpace(relayURL)
	if raw == "" {
		return base
	}
	relay, err := url.Parse(raw)
	if err != nil || (relay.Scheme != "http" && relay.Scheme != "https") || relay.Host == "" {
		return base
	}
	if base == nil {
		base = http.DefaultTransport
	}
	return &relayRoundTripper{base: base, relay: relay}
}

func (r *relayRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if !r.shouldRelay(req.URL) {
		return r.base.RoundTrip(req)
	}
	clone := req.Clone(req.Context())
	clone.Header.Set("x-relay-target", req.URL.Scheme+"://"+req.URL.Host)
	clone.Header.Set("x-relay-path", req.URL.RequestURI())
	clone.URL.Scheme = r.relay.Scheme
	clone.URL.Host = r.relay.Host
	clone.Host = r.relay.Host
	clone.URL.Path = strings.TrimRight(r.relay.Path, "/") + req.URL.Path
	clone.URL.RawQuery = req.URL.RawQuery
	return r.base.RoundTrip(clone)
}

func (r *relayRoundTripper) shouldRelay(target *url.URL) bool {
	if target.Scheme != "https" || target.Host == "" {
		return false
	}
	if target.Host == r.relay.Host {
		return false
	}
	host := target.Hostname()
	if host == "localhost" || host == "::1" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return false
	}
	return true
}
