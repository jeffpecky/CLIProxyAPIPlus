package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// ApplyFinalProviderRequestHook applies wire-only body and header changes.
func ApplyFinalProviderRequestHook(ctx context.Context, req *http.Request, model string, format sdktranslator.Format, stream bool, metadata map[string]any, opts Options) error {
	if opts.FinalProviderRequestHook == nil || req == nil {
		return nil
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return &finalProviderRequestError{err: fmt.Errorf("read final provider request: %w", err)}
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	result, err := opts.FinalProviderRequestHook(ctx, FinalProviderRequest{
		Model: model, Format: format, Stream: stream, Headers: req.Header.Clone(),
		Body: bytes.Clone(body), Metadata: cloneFinalRequestMetadata(metadata),
	})
	if err != nil {
		return &finalProviderRequestError{err: err}
	}
	for _, name := range result.ClearHeaders {
		req.Header.Del(name)
	}
	for name, values := range result.Headers {
		req.Header.Del(name)
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	if result.Body != nil {
		replacement := bytes.Clone(result.Body)
		req.Body = io.NopCloser(bytes.NewReader(replacement))
		req.ContentLength = int64(len(replacement))
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(replacement)), nil
		}
		req.Header.Del("Content-Length")
	}
	return nil
}

type finalProviderRequestError struct{ err error }

func (e *finalProviderRequestError) Error() string {
	return fmt.Sprintf("final provider request hook: %v", e.err)
}
func (e *finalProviderRequestError) Unwrap() error         { return e.err }
func (e *finalProviderRequestError) IsRequestScoped() bool { return true }

func cloneFinalRequestMetadata(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}
