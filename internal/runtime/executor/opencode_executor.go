package executor

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

const (
	// OpenCode base URL for the zen API
	openCodeBaseURL = "https://opencode.ai/zen/v1"
)

// OpenCodeExecutor is a dedicated executor for OpenCode's zen API.
// It wraps OpenAICompatExecutor and adds OpenCode-specific headers and session management.
type OpenCodeExecutor struct {
	*OpenAICompatExecutor
	cfg *config.Config
}

// NewOpenCodeExecutor creates a new OpenCode executor.
func NewOpenCodeExecutor(cfg *config.Config) *OpenCodeExecutor {
	return &OpenCodeExecutor{
		OpenAICompatExecutor: NewOpenAICompatExecutor("opencode", cfg),
		cfg:                  cfg,
	}
}

// Identifier returns the executor identifier.
func (e *OpenCodeExecutor) Identifier() string { return "opencode" }

// PrepareRequest injects OpenCode-specific credentials and headers into the outgoing HTTP request.
func (e *OpenCodeExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}

	// Set Authorization header for OpenCode (free tier uses "public")
	if auth != nil && auth.Attributes != nil {
		if apiKey := strings.TrimSpace(auth.Attributes["api_key"]); apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
	}

	// Apply custom headers from auth attributes (header:* keys)
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)

	// Set OpenCode-specific headers
	// User-Agent: always "opencode" to match the official client
	req.Header.Set("User-Agent", "opencode")

	// Pass through client headers if present, otherwise generate new IDs
	if req.Header.Get("x-opencode-session") == "" {
		req.Header.Set("x-opencode-session", "ses_"+strings.ReplaceAll(uuid.New().String(), "-", ""))
	}
	if req.Header.Get("x-opencode-request") == "" {
		req.Header.Set("x-opencode-request", "msg_"+strings.ReplaceAll(uuid.New().String(), "-", ""))
	}
	if req.Header.Get("x-opencode-project") == "" {
		req.Header.Set("x-opencode-project", "global")
	}
	if req.Header.Get("x-opencode-client") == "" {
		req.Header.Set("x-opencode-client", "desktop")
	}

	return nil
}

// HttpRequest injects OpenCode credentials into the request and executes it.
func (e *OpenCodeExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, nil
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

// injectOpenCodeHeaders adds per-request OpenCode headers to auth attributes
// so they survive through OpenAICompatExecutor.Execute() -> ApplyCustomHeadersFromAttrs.
func injectOpenCodeHeaders(auth *cliproxyauth.Auth) {
	if auth == nil {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	if auth.Attributes["base_url"] == "" {
		auth.Attributes["base_url"] = openCodeBaseURL
	}
	// Set OpenCode-specific headers that ApplyCustomHeadersFromAttrs will apply
	if auth.Attributes["header:User-Agent"] == "" {
		auth.Attributes["header:User-Agent"] = "opencode"
	}
	if auth.Attributes["header:x-opencode-session"] == "" {
		auth.Attributes["header:x-opencode-session"] = "ses_" + strings.ReplaceAll(uuid.New().String(), "-", "")
	}
	if auth.Attributes["header:x-opencode-request"] == "" {
		auth.Attributes["header:x-opencode-request"] = "msg_" + strings.ReplaceAll(uuid.New().String(), "-", "")
	}
	if auth.Attributes["header:x-opencode-project"] == "" {
		auth.Attributes["header:x-opencode-project"] = "global"
	}
	if auth.Attributes["header:x-opencode-client"] == "" {
		auth.Attributes["header:x-opencode-client"] = "desktop"
	}
}

// Execute performs a non-streaming chat completion request to OpenCode.
func (e *OpenCodeExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	injectOpenCodeHeaders(auth)
	return e.OpenAICompatExecutor.Execute(ctx, auth, req, opts)
}

// ExecuteStream performs a streaming chat completion request to OpenCode.
func (e *OpenCodeExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	injectOpenCodeHeaders(auth)
	return e.OpenAICompatExecutor.ExecuteStream(ctx, auth, req, opts)
}
