package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestOpenAICompatFinalHookMutatesOnlyWireBody(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "nonstream", true: "stream"}[stream], func(t *testing.T) {
			var upstreamBody []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upstreamBody, _ = io.ReadAll(r.Body)
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte("data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`))
			}))
			t.Cleanup(server.Close)

			original := []byte(`{"model":"client-model","messages":[{"role":"user","content":"original"}]}`)
			hookCalls := 0
			opts := cliproxyexecutor.Options{
				SourceFormat:    sdktranslator.FormatOpenAI,
				OriginalRequest: bytes.Clone(original),
				Stream:          stream,
				FinalProviderRequestHook: func(_ context.Context, request cliproxyexecutor.FinalProviderRequest) (cliproxyexecutor.FinalProviderRequestResult, error) {
					hookCalls++
					if request.Model != "upstream-model" || request.Format != sdktranslator.FormatOpenAI || request.Stream != stream {
						t.Fatalf("hook request = %+v", request)
					}
					if gjson.GetBytes(request.Body, "model").String() != "upstream-model" {
						t.Fatalf("hook saw pre-translation body: %s", request.Body)
					}
					body, _ := sjson.SetBytes(request.Body, "messages.0.content", "compressed")
					return cliproxyexecutor.FinalProviderRequestResult{Body: body}, nil
				},
			}
			executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": server.URL, "api_key": "test"}}
			req := cliproxyexecutor.Request{Model: "upstream-model", Payload: original}
			if stream {
				result, err := executor.ExecuteStream(context.Background(), auth, req, opts)
				if err != nil {
					t.Fatal(err)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
				}
			} else if _, err := executor.Execute(context.Background(), auth, req, opts); err != nil {
				t.Fatal(err)
			}
			if hookCalls != 1 || gjson.GetBytes(upstreamBody, "messages.0.content").String() != "compressed" {
				t.Fatalf("hook calls=%d upstream=%s", hookCalls, upstreamBody)
			}
			if !bytes.Equal(opts.OriginalRequest, original) || !bytes.Equal(req.Payload, original) {
				t.Fatalf("translator inputs mutated: original=%s payload=%s", opts.OriginalRequest, req.Payload)
			}
		})
	}
}
