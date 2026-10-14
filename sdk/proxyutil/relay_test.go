package proxyutil

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// recordingRT records the request it receives and returns an empty 200
// response without ever touching the network.
type recordingRT struct {
	got *http.Request
}

func (rc *recordingRT) RoundTrip(req *http.Request) (*http.Response, error) {
	rc.got = req
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func TestWrapRelayRewritesHTTPSAndInjectsHeaders(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var relayTarget, relayPath string
	var upRequestURI string
	var upHeader http.Header
	var upBody []byte

	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		upRequestURI = r.URL.RequestURI()
		upHeader = r.Header.Clone()
		upBody = body
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("streamed"))
	}))
	defer upstream.Close()

	relayed := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := r.Header.Get("x-relay-target")
		path := r.Header.Get("x-relay-path")
		mu.Lock()
		relayTarget = target
		relayPath = path
		mu.Unlock()

		// Compose the upstream URL exactly as a real relay worker does
		// (target + path from the two headers), then redirect scheme/host to
		// the local upstream test server — this test never dials the real
		// Anthropic API or any other non-loopback address.
		upURL, errURL := url.Parse(target + path)
		if errURL != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		baseURL, errBase := url.Parse(upstream.URL)
		if errBase != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		upURL.Scheme = baseURL.Scheme
		upURL.Host = baseURL.Host

		forward, errNew := http.NewRequest(r.Method, upURL.String(), r.Body)
		if errNew != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		forward.Header = r.Header.Clone()
		forward.Header.Del("x-relay-target")
		forward.Header.Del("x-relay-path")
		forward.Host = ""
		resp, errRound := upstream.Client().Transport.RoundTrip(forward)
		if errRound != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		buf, _ := io.ReadAll(resp.Body)
		_, _ = w.Write(buf)
	}))
	defer relayed.Close()

	client := &http.Client{Transport: WrapRelay(upstream.Client().Transport, relayed.URL)}
	resp, err := client.Post("https://api.anthropic.com/v1/messages?beta=true", "application/json", strings.NewReader(`{"x":1}`))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	respBody, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		t.Fatalf("read response body: %v", errRead)
	}
	if string(respBody) != "streamed" {
		t.Fatalf("response body = %q, want %q", respBody, "streamed")
	}

	mu.Lock()
	gotTarget := relayTarget
	gotPath := relayPath
	gotURI := upRequestURI
	gotHeader := upHeader
	gotUpBody := upBody
	mu.Unlock()

	// The relay worker received the two-header protocol with the query intact.
	if gotTarget != "https://api.anthropic.com" {
		t.Fatalf("x-relay-target = %q, want %q", gotTarget, "https://api.anthropic.com")
	}
	if gotPath != "/v1/messages?beta=true" {
		t.Fatalf("x-relay-path = %q, want %q", gotPath, "/v1/messages?beta=true")
	}

	// The upstream saw the composed target+path, stripped relay headers,
	// and the untouched request body.
	if gotURI == "" {
		t.Fatal("upstream never reached")
	}
	if gotURI != "/v1/messages?beta=true" {
		t.Fatalf("upstream request URI = %q, want %q", gotURI, "/v1/messages?beta=true")
	}
	if gotHeader.Get("x-relay-target") != "" || gotHeader.Get("x-relay-path") != "" {
		t.Fatalf("relay headers leaked upstream: %v", gotHeader)
	}
	if string(gotUpBody) != `{"x":1}` {
		t.Fatalf("upstream body = %q, want %q", gotUpBody, `{"x":1}`)
	}
}

func TestWrapRelayBypassesLoopbackAndNonHTTPS(t *testing.T) {
	t.Parallel()

	const relayURL = "https://relay.example.workers.dev"
	targets := []string{
		"http://127.0.0.1:8317/v1",
		"http://localhost:11434/x",
		"http://example.com/x",
		"https://[::1]:8443/x",
		"https://relay.example.workers.dev/anything", // aimed at the relay itself
	}
	for _, target := range targets {
		rec := &recordingRT{}
		rt := WrapRelay(rec, relayURL)
		req, errReq := http.NewRequest(http.MethodGet, target, nil)
		if errReq != nil {
			t.Fatalf("new request for %s: %v", target, errReq)
		}
		if _, errRT := rt.RoundTrip(req); errRT != nil {
			t.Fatalf("RoundTrip(%s): %v", target, errRT)
		}
		// Identity: the exact request object reached the base transport,
		// untouched, with no relay headers added — no client, no network.
		if rec.got != req {
			t.Fatalf("RoundTrip(%s) did not pass the original request through untouched", target)
		}
		if rec.got.Header.Get("x-relay-target") != "" {
			t.Fatalf("x-relay-target set for bypass target %s: %q", target, rec.got.Header.Get("x-relay-target"))
		}
		if rec.got.Header.Get("x-relay-path") != "" {
			t.Fatalf("x-relay-path set for bypass target %s: %q", target, rec.got.Header.Get("x-relay-path"))
		}
	}
}

func TestWrapRelayDisabledAndInvalid(t *testing.T) {
	t.Parallel()

	base := http.DefaultTransport
	if WrapRelay(base, "") != base {
		t.Fatal("empty relay-url must return base unchanged")
	}
	if WrapRelay(base, "not a url") != base {
		t.Fatal("invalid relay-url must fall back to base")
	}
	if WrapRelay(base, "ftp://relay.example.com") != base {
		t.Fatal("non-http(s) relay-url must fall back to base")
	}
	if WrapRelay(base, "https://") != base {
		t.Fatal("hostless relay-url must fall back to base")
	}

	rt := WrapRelay(nil, "https://relay.example.com")
	relayRT, ok := rt.(*relayRoundTripper)
	if !ok {
		t.Fatalf("WrapRelay(nil, valid) type = %T, want *relayRoundTripper", rt)
	}
	if relayRT.base != http.DefaultTransport {
		t.Fatal("nil base must fall back to http.DefaultTransport")
	}
}

func TestWrapRelayPreservesRelayPathPrefix(t *testing.T) {
	t.Parallel()

	const wantURL = "https://my.netlify.app/.netlify/functions/relay/v1/messages?beta=true"

	rec := &recordingRT{}
	rt := WrapRelay(rec, "https://my.netlify.app/.netlify/functions/relay")
	req, errReq := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages?beta=true", strings.NewReader(`{"x":1}`))
	if errReq != nil {
		t.Fatalf("new request: %v", errReq)
	}
	if _, errRT := rt.RoundTrip(req); errRT != nil {
		t.Fatalf("RoundTrip: %v", errRT)
	}
	got := rec.got
	if got == nil {
		t.Fatal("base transport never reached")
	}
	if got.URL.String() != wantURL {
		t.Fatalf("relay request URL = %q, want %q", got.URL.String(), wantURL)
	}
	if got.URL.Scheme != "https" {
		t.Fatalf("relay request scheme = %q, want %q", got.URL.Scheme, "https")
	}
	if got.URL.Host != "my.netlify.app" {
		t.Fatalf("relay request host = %q, want %q", got.URL.Host, "my.netlify.app")
	}
	if got.URL.Path != "/.netlify/functions/relay/v1/messages" {
		t.Fatalf("relay request path = %q, want %q", got.URL.Path, "/.netlify/functions/relay/v1/messages")
	}
	if got.URL.RawQuery != "beta=true" {
		t.Fatalf("relay request query = %q, want %q", got.URL.RawQuery, "beta=true")
	}
	if got.Host != "my.netlify.app" {
		t.Fatalf("relay request Host header = %q, want %q", got.Host, "my.netlify.app")
	}
	if got.Header.Get("x-relay-target") != "https://api.anthropic.com" {
		t.Fatalf("x-relay-target = %q, want %q", got.Header.Get("x-relay-target"), "https://api.anthropic.com")
	}
	if got.Header.Get("x-relay-path") != "/v1/messages?beta=true" {
		t.Fatalf("x-relay-path = %q, want %q", got.Header.Get("x-relay-path"), "/v1/messages?beta=true")
	}

	// Path-prefix concatenation edge cases: trailing slash on the relay side,
	// leading slash (or none) on the target side.
	concat := []struct {
		name     string
		relayURL string
		target   string
		want     string
	}{
		{"relay trailing slash", "https://my.netlify.app/relay/", "https://api.anthropic.com/v1/x", "https://my.netlify.app/relay/v1/x"},
		{"origin-only relay", "https://relay.example.com", "https://api.anthropic.com/v1/x", "https://relay.example.com/v1/x"},
		{"target without path", "https://my.netlify.app/relay", "https://api.anthropic.com", "https://my.netlify.app/relay"},
	}
	for _, tc := range concat {
		recCase := &recordingRT{}
		rtCase := WrapRelay(recCase, tc.relayURL)
		reqCase, errNew := http.NewRequest(http.MethodGet, tc.target, nil)
		if errNew != nil {
			t.Fatalf("%s: new request: %v", tc.name, errNew)
		}
		if _, errRT := rtCase.RoundTrip(reqCase); errRT != nil {
			t.Fatalf("%s: RoundTrip: %v", tc.name, errRT)
		}
		if recCase.got == nil || recCase.got.URL.String() != tc.want {
			gotURL := "<nil>"
			if recCase.got != nil {
				gotURL = recCase.got.URL.String()
			}
			t.Fatalf("%s: relay request URL = %q, want %q", tc.name, gotURL, tc.want)
		}
	}

	// The self-loop guard still bypasses for the relay's own host.
	selfRec := &recordingRT{}
	selfRT := WrapRelay(selfRec, "https://my.netlify.app/.netlify/functions/relay")
	selfReq, errSelf := http.NewRequest(http.MethodGet, "https://my.netlify.app/anything", nil)
	if errSelf != nil {
		t.Fatalf("new self-loop request: %v", errSelf)
	}
	if _, errRT := selfRT.RoundTrip(selfReq); errRT != nil {
		t.Fatalf("self-loop RoundTrip: %v", errRT)
	}
	if selfRec.got != selfReq {
		t.Fatal("request aimed at the relay host must pass through untouched")
	}
	if selfReq.Header.Get("x-relay-target") != "" {
		t.Fatalf("x-relay-target set on self-loop request: %q", selfReq.Header.Get("x-relay-target"))
	}
}
