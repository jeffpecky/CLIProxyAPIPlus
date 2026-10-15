package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// cursorRecordingTransport records the requests that reach it and answers with
// a canned 200, so the tests exercise the client/transport wiring end to end
// without dialing anything (httptest-free, no network).
type cursorRecordingTransport struct {
	seen []http.Request
}

func (rt *cursorRecordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.seen = append(rt.seen, *req.Clone(req.Context()))
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    req,
	}, nil
}

func cursorRelayTestConfig(relayURL string) *config.Config {
	cfg := &config.Config{}
	cfg.RelayURL = relayURL
	return cfg
}

// TestCursorHttpRequestRelayEgress pins that HttpRequest's transport is
// relay-wrapped when cfg.RelayURL is set and NOT wrapped when it is empty.
// The ctx-injected "cliproxy.roundtripper" is the observation seam: with no
// proxy configured the relay-aware factory uses it as the base transport, the
// relay wrapper (when present) rewrites the request before it, and the
// fabricated response returns without any dial.
func TestCursorHttpRequestRelayEgress(t *testing.T) {
	t.Parallel()

	const target = "https://api2.cursor.sh/agent.v1.AgentService/Run"

	tests := []struct {
		name        string
		relayURL    string
		wantHost    string
		wantRelayed bool
	}{
		{
			name:        "relay configured wraps the transport",
			relayURL:    "https://relay.example.workers.dev",
			wantHost:    "relay.example.workers.dev",
			wantRelayed: true,
		},
		{
			name:     "empty relay stays direct",
			relayURL: "",
			wantHost: "api2.cursor.sh",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := &CursorExecutor{cfg: cursorRelayTestConfig(tt.relayURL)}

			transport := &cursorRecordingTransport{}
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
			req, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader("{}"))
			if errRequest != nil {
				t.Fatalf("new request: %v", errRequest)
			}

			resp, errDo := e.HttpRequest(ctx, cursorTestAuth(), req)
			if errDo != nil {
				t.Fatalf("HttpRequest: %v", errDo)
			}
			_ = resp.Body.Close()

			if len(transport.seen) != 1 {
				t.Fatalf("transport saw %d requests, want exactly 1", len(transport.seen))
			}
			seen := transport.seen[0]
			if seen.Header.Get("Authorization") != "Bearer test-token" {
				t.Fatalf("Authorization = %q, want bearer access token", seen.Header.Get("Authorization"))
			}
			if seen.URL.Host != tt.wantHost {
				t.Fatalf("egress host = %q, want %q", seen.URL.Host, tt.wantHost)
			}
			gotTarget := seen.Header.Get("x-relay-target")
			if tt.wantRelayed {
				if gotTarget != "https://api2.cursor.sh" {
					t.Fatalf("x-relay-target = %q, want %q", gotTarget, "https://api2.cursor.sh")
				}
				if gotPath := seen.Header.Get("x-relay-path"); gotPath != "/agent.v1.AgentService/Run" {
					t.Fatalf("x-relay-path = %q, want run path", gotPath)
				}
			} else if gotTarget != "" {
				t.Fatalf("x-relay-target = %q, want no relay header on a zero-relay config", gotTarget)
			}
		})
	}
}

// TestFetchCursorModelsRelayEgress pins the model-catalog client: it must go
// through the same relay-aware factory (the raw http2.Transport it replaced was
// relay-invisible) and stay direct when relay-url is empty. The empty canned
// body makes the fetch fall back to cached/fallback models — the assertion is
// on what reached the transport, not on catalog contents.
func TestFetchCursorModelsRelayEgress(t *testing.T) {
	withIsolatedCursorModelsCache(t)

	tests := []struct {
		name        string
		relayURL    string
		wantHost    string
		wantRelayed bool
	}{
		{
			name:        "relay configured wraps the catalog fetch",
			relayURL:    "https://relay.example.workers.dev",
			wantHost:    "relay.example.workers.dev",
			wantRelayed: true,
		},
		{
			name:     "empty relay stays direct",
			relayURL: "",
			wantHost: "api2.cursor.sh",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := &cursorRecordingTransport{}
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
			auth := &cliproxyauth.Auth{
				ID:       "cursor-relay-models",
				Metadata: map[string]any{"access_token": "test-token"},
			}

			FetchCursorModels(ctx, auth, cursorRelayTestConfig(tt.relayURL))

			if len(transport.seen) != 1 {
				t.Fatalf("transport saw %d requests, want exactly 1", len(transport.seen))
			}
			seen := transport.seen[0]
			if seen.URL.Host != tt.wantHost {
				t.Fatalf("catalog egress host = %q, want %q", seen.URL.Host, tt.wantHost)
			}
			gotTarget := seen.Header.Get("x-relay-target")
			if tt.wantRelayed {
				if gotTarget != "https://api2.cursor.sh" {
					t.Fatalf("x-relay-target = %q, want %q", gotTarget, "https://api2.cursor.sh")
				}
				if gotPath := seen.Header.Get("x-relay-path"); gotPath != "/agent.v1.AgentService/GetUsableModels" {
					t.Fatalf("x-relay-path = %q, want models path", gotPath)
				}
			} else if gotTarget != "" {
				t.Fatalf("x-relay-target = %q, want no relay header on a zero-relay config", gotTarget)
			}
		})
	}
}
