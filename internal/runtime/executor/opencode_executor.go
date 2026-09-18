package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

const (
	// OpenCode base URL for the zen API
	openCodeBaseURL = "https://opencode.ai/zen/v1"

	// openCodeSessionHeader is the header name for OpenCode session identity
	openCodeSessionHeader = "x-opencode-session"

	// openCodeMaxSessionLength is the maximum allowed session ID length
	openCodeMaxSessionLength = 256

	// openCodeSessionHashLength is the number of hex characters to use from SHA-256
	openCodeSessionHashLength = 32
)

// openCodeNormalizeSession validates and trims a session ID candidate.
// Returns empty string for invalid, empty, or oversized values.
func openCodeNormalizeSession(value string) string {
	if value == "" {
		return ""
	}
	normalized := strings.TrimSpace(value)
	if normalized == "" || len(normalized) > openCodeMaxSessionLength {
		return ""
	}
	return normalized
}

// openCodeNativeSession extracts the x-opencode-session header from request headers.
// Returns the normalized session if present, empty string otherwise.
func openCodeNativeSession(headers http.Header) string {
	if headers == nil {
		return ""
	}
	// Case-insensitive header lookup
	for key, values := range headers {
		if strings.EqualFold(key, openCodeSessionHeader) {
			for _, value := range values {
				if normalized := openCodeNormalizeSession(value); normalized != "" {
					return normalized
				}
			}
		}
	}
	return ""
}

// openCodeTranslatedSession creates a deterministic, opaque session identifier
// from a downstream session ID and client tool name.
//
// The format is: ses_<first 32 hex chars of SHA-256>
//
// This ensures:
//   - Same conversation produces same ID (deterministic)
//   - Different agents/tools produce different IDs (isolation)
//   - Session IDs are opaque and don't leak downstream identifiers
func openCodeTranslatedSession(sessionID, clientTool string) string {
	if sessionID == "" {
		return ""
	}
	if clientTool == "" {
		clientTool = "generic"
	}
	// Deterministic hash: includes scope to isolate across different agents
	input := "opencode-go\x00" + clientTool + "\x00" + sessionID
	sum := sha256.Sum256([]byte(input))
	return "ses_" + hex.EncodeToString(sum[:])[:openCodeSessionHashLength]
}

// openCodeResolveSession resolves a stable session identity from the request context.
//
// Resolution priority (matching 9router behavior):
//  1. Native x-opencode-session header (authoritative if present)
//  2. Extracted session from downstream request (via ExtractSessionID)
//  3. Deterministic hash of request payload (conversation-stable)
//  4. Fallback to stable ID from auth connection (no generation)
//
// The clientTool parameter isolates sessions across different downstream agents
// (e.g., "claude-code", "opencode", "generic").
func openCodeResolveSession(headers http.Header, payload []byte, auth *cliproxyauth.Auth, clientTool string) string {
	// 1. Check for native x-opencode-session header (highest priority)
	if native := openCodeNativeSession(headers); native != "" {
		return native
	}

	// 2. Extract session from downstream request using existing logic
	if extracted := cliproxyauth.ExtractSessionID(headers, payload, nil); extracted != "" {
		return openCodeTranslatedSession(extracted, clientTool)
	}

	// 3. If auth has a stable connection ID, use it for session stability
	if auth != nil && auth.Attributes != nil {
		if connectionID := auth.Attributes["connection_id"]; connectionID != "" {
			return openCodeTranslatedSession(connectionID, clientTool)
		}
	}

	// 4. Last resort: return empty - caller will generate a fallback
	return ""
}

// OpenCodeExecutor is a dedicated executor for OpenCode's zen API.
// It wraps OpenAICompatExecutor and adds OpenCode-specific headers and session management.
//
// Session resolution follows the 9router pattern:
//   - Deterministic SHA-256 hashing for stable session IDs
//   - Native x-opencode-session header preserved when present
//   - Agent-based isolation via clientTool parameter
//   - Fallback to connection-scoped stable ID
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
// Session resolution uses deterministic hashing for stability across requests.
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

	// Resolve session identity using deterministic hashing (9router pattern)
	// Priority: native header > extracted session > connection ID > fallback
	resolvedSession := openCodeResolveSession(req.Header, nil, auth, "desktop")
	if resolvedSession == "" {
		// Last resort: generate a stable session from request context
		resolvedSession = openCodeTranslatedSession(req.URL.String(), "desktop")
	}
	if req.Header.Get(openCodeSessionHeader) == "" {
		req.Header.Set(openCodeSessionHeader, resolvedSession)
	}

	// Request ID: use existing or generate deterministic ID
	if req.Header.Get("x-opencode-request") == "" {
		reqIDSum := sha256.Sum256([]byte(req.URL.String() + req.Header.Get(openCodeSessionHeader)))
		req.Header.Set("x-opencode-request", "msg_"+hex.EncodeToString(reqIDSum[:])[:16])
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
//
// Session resolution uses deterministic hashing for stability:
//   - Same connection produces same session ID
//   - Different clients produce different IDs via clientTool isolation
func injectOpenCodeHeaders(auth *cliproxyauth.Auth, headers http.Header, payload []byte, clientTool string) {
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

	// Resolve session using deterministic hashing (9router pattern)
	if auth.Attributes["header:"+openCodeSessionHeader] == "" {
		resolvedSession := openCodeResolveSession(headers, payload, auth, clientTool)
		if resolvedSession == "" {
			// Fallback: use connection ID for stability within a connection
			connectionID := auth.Attributes["connection_id"]
			if connectionID == "" {
				connectionID = "default"
			}
			resolvedSession = openCodeTranslatedSession(connectionID, clientTool)
		}
		auth.Attributes["header:"+openCodeSessionHeader] = resolvedSession
	}

	// Request ID: deterministic from session context
	if auth.Attributes["header:x-opencode-request"] == "" {
		sessionID := auth.Attributes["header:"+openCodeSessionHeader]
		reqIDSum := sha256.Sum256([]byte(sessionID))
		auth.Attributes["header:x-opencode-request"] = "msg_" + hex.EncodeToString(reqIDSum[:])[:16]
	}

	if auth.Attributes["header:x-opencode-project"] == "" {
		auth.Attributes["header:x-opencode-project"] = "global"
	}
	if auth.Attributes["header:x-opencode-client"] == "" {
		auth.Attributes["header:x-opencode-client"] = clientTool
	}
}

// Execute performs a non-streaming chat completion request to OpenCode.
// Session resolution uses deterministic hashing for stability across requests.
func (e *OpenCodeExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	// Extract client tool from metadata for session isolation
	clientTool := extractClientTool(req.Metadata)
	// Extract headers from options for session resolution
	headers := opts.Headers
	injectOpenCodeHeaders(auth, headers, req.Payload, clientTool)
	return e.OpenAICompatExecutor.Execute(ctx, auth, req, opts)
}

// ExecuteStream performs a streaming chat completion request to OpenCode.
// Session resolution uses deterministic hashing for stability across requests.
func (e *OpenCodeExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	// Extract client tool from metadata for session isolation
	clientTool := extractClientTool(req.Metadata)
	// Extract headers from options for session resolution
	headers := opts.Headers
	injectOpenCodeHeaders(auth, headers, req.Payload, clientTool)
	return e.OpenAICompatExecutor.ExecuteStream(ctx, auth, req, opts)
}

// extractClientTool extracts the client tool identifier from request metadata.
// This is used for session isolation across different downstream agents.
func extractClientTool(metadata map[string]any) string {
	if metadata == nil {
		return "generic"
	}
	if tool, ok := metadata["client_tool"].(string); ok && tool != "" {
		return tool
	}
	if tool, ok := metadata["clientTool"].(string); ok && tool != "" {
		return tool
	}
	return "generic"
}
