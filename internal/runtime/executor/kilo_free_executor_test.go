package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestKiloFreeExecutorSendsRequestWithoutCredentials(t *testing.T) {
	var gotRequest *http.Request
	var gotBody []byte
	server := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		gotRequest = r.Clone(r.Context())
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	})
	defer server.Close()

	oldBaseURL := kiloFreeBaseURL
	kiloFreeBaseURL = server.URL
	defer func() { kiloFreeBaseURL = oldBaseURL }()

	executor := NewKiloFreeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "kilo-free",
		Provider: "kilo-free",
		Attributes: map[string]string{
			"api_key":              "must-not-be-forwarded",
			"header:Authorization": "Bearer must-not-be-forwarded",
			"header:x-api-key":     "must-not-be-forwarded",
			"header:user-agent":    "wrong-agent",
		},
	}
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   registry.KiloFreeModelPrefix + "vendor/free-model",
		Payload: []byte(`{"model":"kilo-free/vendor/free-model","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high"}`),
	}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotRequest == nil {
		t.Fatal("upstream request was not captured")
	}
	if gotRequest.URL.Path != "/chat/completions" {
		t.Errorf("request path = %q, want /chat/completions", gotRequest.URL.Path)
	}
	if got := gotRequest.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want no credential", got)
	}
	if got := gotRequest.Header.Get("X-Api-Key"); got != "" {
		t.Errorf("X-Api-Key = %q, want no credential", got)
	}
	if got := gotRequest.Header.Get("User-Agent"); got != kiloFreeUA {
		t.Errorf("User-Agent = %q, want %q", got, kiloFreeUA)
	}
	var payload map[string]any
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatalf("upstream body is not JSON: %v", err)
	}
	if payload["model"] != "vendor/free-model" {
		t.Errorf("upstream model = %v, want vendor/free-model", payload["model"])
	}
	reasoning, ok := payload["reasoning"].(map[string]any)
	if payload["reasoning_effort"] != nil || !ok || reasoning["effort"] != "high" {
		t.Errorf("Kilo reasoning format not translated: %v", payload)
	}
}

func TestKiloFreeExecutorStreamsWithoutCredentials(t *testing.T) {
	var gotAuthorization string
	var gotUserAgent string
	server := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		gotUserAgent = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl_1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n"))
	})
	defer server.Close()

	oldBaseURL := kiloFreeBaseURL
	kiloFreeBaseURL = server.URL
	defer func() { kiloFreeBaseURL = oldBaseURL }()

	executor := NewKiloFreeExecutor(&config.Config{})
	result, err := executor.ExecuteStream(context.Background(), &cliproxyauth.Auth{Provider: "kilo-free"}, cliproxyexecutor.Request{
		Model:   registry.KiloFreeModelPrefix + "vendor/free-model",
		Payload: []byte(`{"model":"kilo-free/vendor/free-model","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	for range result.Chunks {
	}
	if gotAuthorization != "" {
		t.Errorf("stream sent Authorization %q, want none", gotAuthorization)
	}
	if gotUserAgent != kiloFreeUA {
		t.Errorf("stream User-Agent = %q, want %q", gotUserAgent, kiloFreeUA)
	}
}

func TestKiloFreeExecutionDoesNotMutateAuth(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Provider:   "kilo",
		Attributes: map[string]string{"api_key": "existing-account-key", "custom": "value"},
	}
	opts := cliproxyexecutor.Options{Headers: http.Header{"Authorization": []string{"Bearer client-key"}}}

	publicAuth, publicOpts := kiloFreeExecution(auth, opts)
	if publicAuth.Provider != "kilo-free" || publicAuth.Attributes["base_url"] != kiloFreeBaseURL {
		t.Errorf("public auth not configured for the free endpoint: %+v", publicAuth)
	}
	if publicAuth.Attributes["api_key"] != "" || publicAuth.Attributes["header:authorization"] != "" {
		t.Errorf("credentials retained in free auth: %+v", publicAuth.Attributes)
	}
	if publicAuth.Attributes["custom"] != "value" {
		t.Errorf("custom attribute was not preserved: %+v", publicAuth.Attributes)
	}
	if auth.Provider != "kilo" || auth.Attributes["api_key"] != "existing-account-key" {
		t.Errorf("original authenticated Kilo auth was mutated: %+v", auth)
	}
	if publicOpts.Headers.Get("Authorization") != "" || opts.Headers.Get("Authorization") == "" {
		t.Errorf("authorization header sanitizing/mutation mismatch: public=%v original=%v", publicOpts.Headers, opts.Headers)
	}
}

func TestKiloFreeReasoningBodyMapsDisabledEffort(t *testing.T) {
	got := kiloFreeReasoningBody([]byte(`{"reasoning_effort":"none"}`))
	var payload map[string]any
	if err := json.Unmarshal(got, &payload); err != nil {
		t.Fatalf("reasoning body is not JSON: %v", err)
	}
	reasoning, ok := payload["reasoning"].(map[string]any)
	if payload["reasoning_effort"] != nil || !ok || reasoning["enabled"] != false {
		t.Errorf("disabled reasoning not translated to Kilo format: %v", payload)
	}
}

func TestKiloFreeExecutorPrepareRequestRemovesCredentials(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "https://example.test/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer key")
	req.Header.Set("X-Api-Key", "key")
	if err := NewKiloFreeExecutor(&config.Config{}).PrepareRequest(req, nil); err != nil {
		t.Fatalf("PrepareRequest error: %v", err)
	}
	if req.Header.Get("Authorization") != "" || req.Header.Get("X-Api-Key") != "" {
		t.Errorf("credentials were not removed from request: %v", req.Header)
	}
	if req.Header.Get("User-Agent") != kiloFreeUA {
		t.Errorf("User-Agent = %q, want %q", req.Header.Get("User-Agent"), kiloFreeUA)
	}
}
