package helps

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
)

func TestNewProxyAwareHTTPClientRequestProxyOverridesAuthAndGlobal(t *testing.T) {
	t.Parallel()

	ctx := coreexecutor.WithRequestProxyURL(context.Background(), "http://request-proxy.example:8081")
	client := NewProxyAwareHTTPClient(
		ctx,
		&config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"}},
		&cliproxyauth.Auth{ProxyURL: "http://auth-proxy.example:8080"},
		0,
	)
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.Proxy == nil {
		t.Fatalf("transport = %#v, want request proxy", client.Transport)
	}
	req, errReq := http.NewRequest(http.MethodGet, "https://upstream.example/v1", nil)
	if errReq != nil {
		t.Fatalf("request: %v", errReq)
	}
	proxyURL, errProxy := transport.Proxy(req)
	if errProxy != nil {
		t.Fatalf("proxy: %v", errProxy)
	}
	if proxyURL == nil || proxyURL.String() != "http://request-proxy.example:8081" {
		t.Fatalf("proxy URL = %v, want request proxy", proxyURL)
	}

	refreshCtx := coreexecutor.WithoutRequestProxyURL(ctx)
	refreshClient := NewProxyAwareHTTPClient(
		refreshCtx,
		&config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"}},
		&cliproxyauth.Auth{ProxyURL: "http://auth-proxy.example:8080"},
		0,
	)
	refreshTransport, ok := refreshClient.Transport.(*http.Transport)
	if !ok || refreshTransport.Proxy == nil {
		t.Fatalf("refresh transport = %#v, want auth proxy", refreshClient.Transport)
	}
	refreshProxy, errRefresh := refreshTransport.Proxy(req)
	if errRefresh != nil {
		t.Fatalf("refresh proxy: %v", errRefresh)
	}
	if refreshProxy == nil || refreshProxy.String() != "http://auth-proxy.example:8080" {
		t.Fatalf("refresh proxy URL = %v, want auth proxy", refreshProxy)
	}
}

func TestNewProxyAwareHTTPClientDirectBypassesGlobalProxy(t *testing.T) {
	t.Parallel()

	client := NewProxyAwareHTTPClient(
		context.Background(),
		&config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"}},
		&cliproxyauth.Auth{ProxyURL: "direct"},
		0,
	)

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("expected direct transport to disable proxy function")
	}
}

func TestNewDevinHTTPClient_ReusesTransportFromContext(t *testing.T) {
	baseTransport := &http.Transport{}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", baseTransport)

	c1 := NewDevinHTTPClient(ctx, nil, nil, 0)
	c2 := NewDevinHTTPClient(ctx, nil, nil, 0)

	if c1.Transport != c2.Transport {
		t.Errorf("expected c1.Transport == c2.Transport across requests, got different pointers %p vs %p", c1.Transport, c2.Transport)
	}

	tr, ok := c1.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", c1.Transport)
	}
	if !tr.DisableCompression {
		t.Error("expected DisableCompression = true")
	}
}

func TestNewDevinHTTPClient_NonStandardRoundTripperDisablesGzip(t *testing.T) {
	var seenEncoding string
	customRT := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		seenEncoding = req.Header.Get("Accept-Encoding")
		return &http.Response{StatusCode: 200}, nil
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", customRT)

	c := NewDevinHTTPClient(ctx, nil, nil, 0)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://example.invalid", nil)
	_, _ = c.Transport.RoundTrip(req)

	if seenEncoding != "identity" {
		t.Errorf("expected Accept-Encoding: identity, got %q", seenEncoding)
	}
}

type roundTripperFunc func(req *http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestNewProxyAwareHTTPClientWrapsRelay(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	cfg.RelayURL = "https://relay.example.workers.dev"
	client := NewProxyAwareHTTPClient(context.Background(), cfg, nil, 0)
	if !proxyutil.IsRelayRoundTripper(client.Transport) {
		t.Fatalf("transport %T is not relay-wrapped", client.Transport)
	}

	// The relay guard is TrimSpace-sensitive: whitespace-only values must
	// leave the zero-relay behavior untouched (nil transport, no wrapper).
	blank := &config.Config{}
	blank.RelayURL = "   \t "
	blankClient := NewProxyAwareHTTPClient(context.Background(), blank, nil, 0)
	if blankClient.Transport != nil {
		t.Fatalf("whitespace-only relay-url produced transport %T, want none", blankClient.Transport)
	}
}

func TestNewProxyAwareHTTPClientRelayCacheKeySeparation(t *testing.T) {
	t.Parallel()

	// One proxy URL shared by all three clients so any cache-key collision
	// would surface as a shared transport. Proxy transports are built, never
	// dialed, so the fake hostnames stay offline.
	const proxyURL = "http://relay-sep-proxy.example:8080"
	plainCfg := &config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: proxyURL}}
	relayedCfg := &config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: proxyURL}}
	relayedCfg.RelayURL = "https://relay-a.example.workers.dev"
	otherCfg := &config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: proxyURL}}
	otherCfg.RelayURL = "https://relay-b.example.workers.dev"

	plain := NewProxyAwareHTTPClient(context.Background(), plainCfg, nil, 0)
	relayed := NewProxyAwareHTTPClient(context.Background(), relayedCfg, nil, 0)
	other := NewProxyAwareHTTPClient(context.Background(), otherCfg, nil, 0)

	if plain.Transport == nil {
		t.Fatal("plain client has no transport")
	}
	if plain.Transport == relayed.Transport {
		t.Fatal("relay and non-relay clients share transport")
	}
	if proxyutil.IsRelayRoundTripper(plain.Transport) {
		t.Fatal("plain client transport must not be relay-wrapped")
	}
	if !proxyutil.IsRelayRoundTripper(relayed.Transport) {
		t.Fatalf("relayed client transport %T is not relay-wrapped", relayed.Transport)
	}
	if relayed.Transport == other.Transport {
		t.Fatal("clients with distinct relay URLs share transport")
	}
}

// TestNewUtlsHTTPClientRelayRewritesThroughRelay is behavioral: with relay-url
// set, a request through the uTLS client must reach the relay with the
// two-header protocol instead of dialing the upstream directly. The relay is a
// plain-HTTP loopback server (an http:// relay URL is valid), which sidesteps
// the httptest TLS certificate trust problem: the client's fallback transport
// dials it as ordinary HTTP.
func TestNewUtlsHTTPClientRelayRewritesThroughRelay(t *testing.T) {
	t.Parallel()

	var (
		mu          sync.Mutex
		relayTarget string
		relayPath   string
	)
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		relayTarget = r.Header.Get("x-relay-target")
		relayPath = r.Header.Get("x-relay-path")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(relay.Close)

	cfg := &config.Config{}
	cfg.RelayURL = relay.URL
	client := NewUtlsHTTPClient(context.Background(), cfg, nil, 0)

	resp, errReq := client.Get("https://api.anthropic.com/v1/messages")
	if errReq != nil {
		t.Fatalf("request: %v", errReq)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("relay status = %d, want 200", resp.StatusCode)
	}

	mu.Lock()
	defer mu.Unlock()
	if relayTarget != "https://api.anthropic.com" {
		t.Fatalf("x-relay-target = %q, want %q", relayTarget, "https://api.anthropic.com")
	}
	if relayPath != "/v1/messages" {
		t.Fatalf("x-relay-path = %q, want %q", relayPath, "/v1/messages")
	}
}
