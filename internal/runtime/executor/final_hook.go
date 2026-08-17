package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func applyFinalHookBody(ctx context.Context, httpReq *http.Request, model string, format sdktranslator.Format, stream bool, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) ([]byte, error) {
	if err := cliproxyexecutor.ApplyFinalProviderRequestHook(ctx, httpReq, model, format, stream, req.Metadata, opts); err != nil {
		return nil, err
	}
	body, err := io.ReadAll(httpReq.Body)
	if err != nil {
		return nil, fmt.Errorf("read final provider request: %w", err)
	}
	resetFinalRequestBody(httpReq, body)
	return body, nil
}

func applyFinalHookBytes(ctx context.Context, body []byte, model string, format sdktranslator.Format, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) ([]byte, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://token-count.local", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	return applyFinalHookBody(ctx, httpReq, model, format, false, req, opts)
}

func resetFinalRequestBody(req *http.Request, body []byte) {
	copyBody := bytes.Clone(body)
	req.Body = io.NopCloser(bytes.NewReader(copyBody))
	req.ContentLength = int64(len(copyBody))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(copyBody)), nil }
	req.Header.Del("Content-Length")
}

func finalProviderHookUnsupported(opts cliproxyexecutor.Options, provider string) error {
	if opts.FinalProviderRequestHook == nil {
		return nil
	}
	return &finalProviderRequestUnsupportedError{provider: provider}
}

type finalProviderRequestUnsupportedError struct{ provider string }

func (e *finalProviderRequestUnsupportedError) Error() string {
	return e.provider + " transport cannot safely transform final JSON body"
}
func (e *finalProviderRequestUnsupportedError) IsRequestScoped() bool { return true }
