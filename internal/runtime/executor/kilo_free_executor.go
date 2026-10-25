package executor

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const kiloFreeUA = "cli-proxy-kilo-free"

var kiloFreeBaseURL = "https://api.kilo.ai/api/gateway"

// KiloFreeExecutor routes to Kilo AI's public, no-key free model pool.
// It is deliberately separate from KiloExecutor, which uses account tokens.
type KiloFreeExecutor struct {
	*OpenAICompatExecutor
}

func NewKiloFreeExecutor(cfg *config.Config) *KiloFreeExecutor {
	return &KiloFreeExecutor{
		OpenAICompatExecutor: NewOpenAICompatExecutor("kilo-free", cfg),
	}
}

func (e *KiloFreeExecutor) Identifier() string { return "kilo-free" }

// PrepareRequest never adds credentials: this gateway's free pool is keyless.
func (e *KiloFreeExecutor) PrepareRequest(req *http.Request, _ *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	deleteKiloCredentialHeaders(req.Header)
	req.Header.Set("User-Agent", kiloFreeUA)
	return nil
}

func (e *KiloFreeExecutor) HttpRequest(ctx context.Context, _ *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, nil
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, nil); err != nil {
		return nil, err
	}
	return newProxyAwareHTTPClient(ctx, e.cfg, nil, 0).Do(httpReq)
}

func (e *KiloFreeExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	publicAuth, publicOpts := kiloFreeExecution(auth, opts)
	req = kiloFreeUpstreamRequest(req)
	return e.OpenAICompatExecutor.Execute(ctx, publicAuth, req, publicOpts)
}

func (e *KiloFreeExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	publicAuth, publicOpts := kiloFreeExecution(auth, opts)
	req = kiloFreeUpstreamRequest(req)
	return e.OpenAICompatExecutor.ExecuteStream(ctx, publicAuth, req, publicOpts)
}

func kiloFreeUpstreamRequest(req cliproxyexecutor.Request) cliproxyexecutor.Request {
	if !strings.HasPrefix(req.Model, registry.KiloFreeModelPrefix) {
		return req
	}
	req.Model = strings.TrimPrefix(req.Model, registry.KiloFreeModelPrefix)
	if payload, err := sjson.SetBytes(req.Payload, "model", req.Model); err == nil {
		req.Payload = payload
	}
	return req
}

func kiloFreeExecution(auth *cliproxyauth.Auth, opts cliproxyexecutor.Options) (*cliproxyauth.Auth, cliproxyexecutor.Options) {
	publicAuth := &cliproxyauth.Auth{ID: "kilo-free", Provider: "kilo-free"}
	if auth != nil {
		*publicAuth = *auth
		publicAuth.Provider = "kilo-free"
	}
	attrs := make(map[string]string, len(publicAuth.Attributes)+2)
	for key, value := range publicAuth.Attributes {
		lower := strings.ToLower(key)
		if lower == "api_key" || lower == "header:authorization" || lower == "header:x-api-key" {
			continue
		}
		attrs[key] = value
	}
	attrs["base_url"] = kiloFreeBaseURL
	attrs["header:user-agent"] = kiloFreeUA
	publicAuth.Attributes = attrs

	opts.Headers = opts.Headers.Clone()
	deleteKiloCredentialHeaders(opts.Headers)
	previousHook := opts.FinalProviderRequestHook
	opts.FinalProviderRequestHook = func(ctx context.Context, request cliproxyexecutor.FinalProviderRequest) (cliproxyexecutor.FinalProviderRequestResult, error) {
		result := cliproxyexecutor.FinalProviderRequestResult{}
		if previousHook != nil {
			var err error
			result, err = previousHook(ctx, request)
			if err != nil {
				return result, err
			}
		}
		body := request.Body
		if result.Body != nil {
			body = result.Body
		}
		updated := kiloFreeReasoningBody(body)
		if string(updated) != string(body) {
			result.Body = updated
		}
		return result, nil
	}
	return publicAuth, opts
}

func kiloFreeReasoningBody(body []byte) []byte {
	effortValue := gjson.GetBytes(body, "reasoning_effort")
	if !effortValue.Exists() {
		return body
	}
	effort := strings.ToLower(strings.TrimSpace(effortValue.String()))
	updated := body
	if effort == "none" || effort == "off" || effort == "disabled" {
		if result, err := sjson.DeleteBytes(updated, "reasoning.effort"); err == nil {
			updated = result
		}
		if result, err := sjson.SetBytes(updated, "reasoning.enabled", false); err == nil {
			updated = result
		}
	} else if effort != "" {
		if result, err := sjson.SetBytes(updated, "reasoning.effort", effort); err == nil {
			updated = result
		}
	}
	if result, err := sjson.DeleteBytes(updated, "reasoning_effort"); err == nil {
		return result
	}
	return updated
}

func deleteKiloCredentialHeaders(headers http.Header) {
	for name := range headers {
		if strings.EqualFold(name, "Authorization") || strings.EqualFold(name, "X-Api-Key") {
			delete(headers, name)
		}
	}
}

func (e *KiloFreeExecutor) Refresh(_ context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	return auth, nil
}

func (e *KiloFreeExecutor) CountTokens(_ context.Context, _ *cliproxyauth.Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, fmt.Errorf("kilo free: count tokens not supported")
}
