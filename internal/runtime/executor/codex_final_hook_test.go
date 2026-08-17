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

func TestCodexFinalHookSeesCacheFinalizedWireBody(t *testing.T) {
	var wire []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wire, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"))
	}))
	defer server.Close()
	original := []byte(`{"model":"gpt-5.4","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"long"}]}]}`)
	req := cliproxyexecutor.Request{Model: "gpt-5.4", Payload: bytes.Clone(original), Metadata: map[string]any{cliproxyexecutor.DerivedSessionIDMetadataKey: "ctx:v1:test"}}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: bytes.Clone(original),
		FinalProviderRequestHook: func(_ context.Context, in cliproxyexecutor.FinalProviderRequest) (cliproxyexecutor.FinalProviderRequestResult, error) {
			if gjson.GetBytes(in.Body, "prompt_cache_key").String() == "" {
				t.Fatalf("hook missed cacheHelper mutation: %s", in.Body)
			}
			body, _ := sjson.SetBytes(in.Body, "hooked", true)
			return cliproxyexecutor.FinalProviderRequestResult{Body: body}, nil
		}}
	exec := NewCodexExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	_, err := exec.Execute(context.Background(), &cliproxyauth.Auth{Attributes: map[string]string{"base_url": server.URL, "api_key": "key"}}, req, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !gjson.GetBytes(wire, "hooked").Bool() || !bytes.Equal(req.Payload, original) || !bytes.Equal(opts.OriginalRequest, original) {
		t.Fatalf("wire/source mismatch: %s", wire)
	}
}

func TestCodexStreamFinalHookSeesCacheFinalizedWireBody(t *testing.T) {
	var wire []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wire, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"))
	}))
	defer server.Close()
	original := []byte(`{"model":"gpt-5.4","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"long"}]}]}`)
	req := cliproxyexecutor.Request{Model: "gpt-5.4", Payload: bytes.Clone(original), Metadata: map[string]any{cliproxyexecutor.DerivedSessionIDMetadataKey: "ctx:v1:stream-test"}}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: bytes.Clone(original), Stream: true,
		FinalProviderRequestHook: func(_ context.Context, in cliproxyexecutor.FinalProviderRequest) (cliproxyexecutor.FinalProviderRequestResult, error) {
			if !in.Stream || gjson.GetBytes(in.Body, "prompt_cache_key").String() == "" {
				t.Fatalf("hook missed stream cacheHelper mutation: %s", in.Body)
			}
			body, _ := sjson.SetBytes(in.Body, "hooked_stream", true)
			return cliproxyexecutor.FinalProviderRequestResult{Body: body}, nil
		}}
	exec := NewCodexExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	result, err := exec.ExecuteStream(context.Background(), &cliproxyauth.Auth{Attributes: map[string]string{"base_url": server.URL, "api_key": "key"}}, req, opts)
	if err != nil {
		t.Fatal(err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
	}
	if !gjson.GetBytes(wire, "hooked_stream").Bool() || !bytes.Equal(req.Payload, original) || !bytes.Equal(opts.OriginalRequest, original) {
		t.Fatalf("wire/source mismatch: %s", wire)
	}
}
