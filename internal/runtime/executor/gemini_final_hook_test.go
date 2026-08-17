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

func TestGeminiStandardFinalHookMutatesStreamAndNonstreamWire(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "nonstream", true: "stream"}[stream], func(t *testing.T) {
			var wire []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				wire, _ = io.ReadAll(r.Body)
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte("data: {\"candidates\":[],\"usageMetadata\":{}}\n\n"))
				} else {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"candidates":[],"usageMetadata":{}}`))
				}
			}))
			t.Cleanup(server.Close)
			original := []byte(`{"model":"client","messages":[{"role":"user","content":"long"}]}`)
			req := cliproxyexecutor.Request{Model: "gemini-2.5-flash", Payload: bytes.Clone(original)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, OriginalRequest: bytes.Clone(original), Stream: stream,
				FinalProviderRequestHook: func(_ context.Context, in cliproxyexecutor.FinalProviderRequest) (cliproxyexecutor.FinalProviderRequestResult, error) {
					if in.Model != "gemini-2.5-flash" || in.Format != sdktranslator.FormatGemini || in.Stream != stream || !gjson.GetBytes(in.Body, "contents").Exists() {
						t.Fatalf("hook input = %+v body=%s", in, in.Body)
					}
					body, _ := sjson.SetBytes(in.Body, "hooked", true)
					return cliproxyexecutor.FinalProviderRequestResult{Body: body}, nil
				}}
			exec := NewGeminiExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key", "base_url": server.URL}}
			if stream {
				result, err := exec.ExecuteStream(context.Background(), auth, req, opts)
				if err != nil {
					t.Fatal(err)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
				}
			} else if _, err := exec.Execute(context.Background(), auth, req, opts); err != nil {
				t.Fatal(err)
			}
			if !gjson.GetBytes(wire, "hooked").Bool() || !bytes.Equal(req.Payload, original) || !bytes.Equal(opts.OriginalRequest, original) {
				t.Fatalf("wire/source mismatch: %s", wire)
			}
		})
	}
}
