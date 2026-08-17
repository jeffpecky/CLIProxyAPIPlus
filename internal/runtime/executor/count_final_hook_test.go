package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestLocalCountPathsApplyFinalHookToTranslatedBody(t *testing.T) {
	tests := []struct {
		name   string
		format sdktranslator.Format
		count  func(cliproxyexecutor.Request, cliproxyexecutor.Options) error
	}{
		{"openai compat", sdktranslator.FormatOpenAI, func(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
			_, err := NewOpenAICompatExecutor("openai-compatibility", &config.Config{}).CountTokens(context.Background(), nil, req, opts)
			return err
		}},
		{"claude local", sdktranslator.FormatClaude, func(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
			_, err := NewClaudeExecutor(&config.Config{}).CountTokens(context.Background(), &cliproxyauth.Auth{Attributes: map[string]string{"base_url": "http://local.invalid"}}, req, opts)
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			req := cliproxyexecutor.Request{Model: "gpt-4o", Payload: []byte(`{"messages":[{"role":"user","content":"long"}]}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, FinalProviderRequestHook: func(_ context.Context, in cliproxyexecutor.FinalProviderRequest) (cliproxyexecutor.FinalProviderRequestResult, error) {
				calls++
				if in.Format != test.format {
					t.Fatalf("format=%q want=%q", in.Format, test.format)
				}
				body, _ := sjson.SetBytes(in.Body, "messages.0.content", "short")
				return cliproxyexecutor.FinalProviderRequestResult{Body: body}, nil
			}}
			if err := test.count(req, opts); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("hook calls=%d", calls)
			}
		})
	}
}

func TestGeminiCountAppliesFinalHookToWireBody(t *testing.T) {
	var wire []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wire, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"totalTokens":1}`))
	}))
	defer server.Close()
	calls := 0
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, FinalProviderRequestHook: func(_ context.Context, in cliproxyexecutor.FinalProviderRequest) (cliproxyexecutor.FinalProviderRequestResult, error) {
		calls++
		body, _ := sjson.SetBytes(in.Body, "hooked", true)
		return cliproxyexecutor.FinalProviderRequestResult{Body: body}, nil
	}}
	_, err := NewGeminiExecutor(&config.Config{}).CountTokens(context.Background(), &cliproxyauth.Auth{Attributes: map[string]string{"base_url": server.URL, "api_key": "key"}}, cliproxyexecutor.Request{Model: "gemini-2.5-flash", Payload: []byte(`{"messages":[{"role":"user","content":"long"}]}`)}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !gjson.GetBytes(wire, "hooked").Bool() {
		t.Fatalf("calls=%d wire=%s", calls, wire)
	}
}

func TestRepresentativeLocalCountsApplyFinalHook(t *testing.T) {
	tests := []struct {
		name  string
		count func(cliproxyexecutor.Request, cliproxyexecutor.Options) error
	}{
		{"codex", func(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
			_, err := NewCodexExecutor(&config.Config{}).CountTokens(context.Background(), nil, req, opts)
			return err
		}},
		{"xai", func(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
			_, err := NewXAIExecutor(&config.Config{}).CountTokens(context.Background(), nil, req, opts)
			return err
		}},
		{"kiro", func(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
			_, err := NewKiroExecutor(nil).CountTokens(context.Background(), nil, req, opts)
			return err
		}},
		{"github copilot", func(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
			_, err := NewGitHubCopilotExecutor(&config.Config{}).CountTokens(context.Background(), nil, req, opts)
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, FinalProviderRequestHook: func(_ context.Context, in cliproxyexecutor.FinalProviderRequest) (cliproxyexecutor.FinalProviderRequestResult, error) {
				calls++
				return cliproxyexecutor.FinalProviderRequestResult{Body: in.Body}, nil
			}}
			if err := test.count(cliproxyexecutor.Request{Model: "gpt-4o", Payload: []byte(`{"messages":[{"role":"user","content":"hello"}]}`)}, opts); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("hook calls=%d", calls)
			}
		})
	}
}

func TestResetFinalRequestBodyUpdatesReplayState(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "http://example.test", strings.NewReader("old"))
	want := []byte("new-body")
	resetFinalRequestBody(req, want)
	replayed, err := req.GetBody()
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(replayed)
	if string(got) != string(want) || req.ContentLength != int64(len(want)) {
		t.Fatalf("replay=%q length=%d", got, req.ContentLength)
	}
}

func TestClaudeCCHFinalBodyUpdatesGetBodyForReplay(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"transformed"}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.220.test; cc_entrypoint=sdk-cli; cch=00000;"}],"max_tokens":8}`)
	finalized, err := finalizeAnthropicMessagesBodyCCH(body, "")
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, "http://example.test", strings.NewReader("stale"))
	resetFinalRequestBody(req, finalized)
	replay, err := req.GetBody()
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(replay)
	if string(got) != string(finalized) || req.ContentLength != int64(len(finalized)) {
		t.Fatalf("replay differs from final CCH body: %s", got)
	}
}
