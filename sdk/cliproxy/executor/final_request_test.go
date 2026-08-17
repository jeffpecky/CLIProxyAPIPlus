package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestApplyFinalProviderRequestHookReplacesWireRequest(t *testing.T) {
	original := []byte(`{"messages":[{"role":"user","content":"original"}]}`)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://example.com/v1/messages", strings.NewReader(string(original)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Keep", "yes")
	req.Header.Set("X-Clear", "old")
	opts := Options{
		OriginalRequest: original,
		FinalProviderRequestHook: func(_ context.Context, in FinalProviderRequest) (FinalProviderRequestResult, error) {
			in.Body[0] = '['
			in.Headers.Set("X-Keep", "mutated-copy")
			return FinalProviderRequestResult{
				Body:         []byte(`{"messages":[{"role":"user","content":"compressed"}]}`),
				Headers:      http.Header{"X-Added": []string{"yes"}},
				ClearHeaders: []string{"X-Clear"},
			}, nil
		},
	}

	if err := ApplyFinalProviderRequestHook(context.Background(), req, "claude-final", sdktranslator.FormatClaude, false, nil, opts); err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(req.Body)
	if !strings.Contains(string(body), "compressed") || req.ContentLength != int64(len(body)) || req.GetBody == nil {
		t.Fatalf("wire body not replaced correctly: body=%s length=%d getBody=%v", body, req.ContentLength, req.GetBody != nil)
	}
	if string(opts.OriginalRequest) != string(original) || original[0] != '{' {
		t.Fatalf("original request mutated: %s", opts.OriginalRequest)
	}
	if req.Header.Get("X-Keep") != "yes" || req.Header.Get("X-Added") != "yes" || req.Header.Get("X-Clear") != "" {
		t.Fatalf("headers = %#v", req.Header)
	}
}

func TestApplyFinalProviderRequestHookErrorIsRequestScoped(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "https://example.com", strings.NewReader(`{}`))
	want := errors.New("hook failed")
	err := ApplyFinalProviderRequestHook(context.Background(), req, "m", sdktranslator.FormatOpenAI, true, nil, Options{
		FinalProviderRequestHook: func(context.Context, FinalProviderRequest) (FinalProviderRequestResult, error) {
			return FinalProviderRequestResult{}, want
		},
	})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
	var scoped RequestScopedError
	if !errors.As(err, &scoped) || !scoped.IsRequestScoped() {
		t.Fatalf("error is not request scoped: %T", err)
	}
}
