package executor

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"math/big"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

const (
	openCodeBaseURL         = "https://opencode.ai/zen/v1"
	openCodeSessionHeader   = "x-opencode-session"
	openCodeMaxSessionLength = 256
	openCodeUA              = "opencode/1.18.31"
)

var base62Chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// openCodeDecoyTools are injected to satisfy upstream free-tier verification
// which requires both 'bash' and 'read' in the tools payload.
var openCodeDecoyTools = []map[string]any{
	{
		"type": "function",
		"function": map[string]any{
			"name":        "bash",
			"description": "This tool is currently unavailable and must not be used.",
			"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
		},
	},
	{
		"type": "function",
		"function": map[string]any{
			"name":        "read",
			"description": "This tool is currently unavailable and must not be used.",
			"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
		},
	},
}

func openCodeBase62Random(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	var out strings.Builder
	for i := 0; i < n; i++ {
		out.WriteByte(base62Chars[int(b[i])%62])
	}
	return out.String()
}

// openCodeGenerateSessionId generates a canonical session ID:
// ses_ + 12 hex chars (from inverted timestamp) + 14 base62 chars = 30 chars total.
func openCodeGenerateSessionId() string {
	ts := uint64(0)
	n, _ := rand.Int(rand.Reader, big.NewInt(1<<40))
	ts = uint64(n.Int64())
	inverted := ^ts
	hex12 := ""
	for i := 5; i >= 0; i-- {
		hex12 += string("0123456789abcdef"[(inverted>>(uint(i)*8))&0xff>>4])
		hex12 += string("0123456789abcdef"[(inverted>>(uint(i)*8))&0xff&0x0f])
	}
	if len(hex12) > 12 {
		hex12 = hex12[:12]
	}
	return "ses_" + hex12 + openCodeBase62Random(14)
}

// openCodeGenerateRequestId generates a canonical request ID:
// msg_ + 12 hex chars + 14 base62 chars = 30 chars total.
func openCodeGenerateRequestId() string {
	return "msg_" + openCodeBase62Random(12) + openCodeBase62Random(14)
}

// openCodeTranslateSession creates a deterministic session ID from a downstream
// session ID and client tool name, in canonical format: ses_ + 12 hex + 14 base62.
func openCodeTranslateSession(sessionID, clientTool string) string {
	if sessionID == "" {
		return ""
	}
	if clientTool == "" {
		clientTool = "generic"
	}
	input := "opencode\x00" + clientTool + "\x00" + sessionID
	sum := sha256.Sum256([]byte(input))
	// 12 hex chars from first 6 bytes of SHA-256
	hexPart := ""
	for i := 0; i < 6; i++ {
		hexPart += string("0123456789abcdef"[sum[i]>>4])
		hexPart += string("0123456789abcdef"[sum[i]&0x0f])
	}
	// 14 base62 chars from bytes 6-19
	base62Part := ""
	for i := 6; i < 20 && i < len(sum); i++ {
		base62Part += string(base62Chars[sum[i]%62])
	}
	return "ses_" + hexPart + base62Part
}

// openCodeNormalizeSession validates and trims a session ID candidate.
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

// openCodeIsValidSessionFormat checks if a session matches canonical format:
// ses_ + 12 hex + 14 base62 = 30 chars total.
func openCodeIsValidSessionFormat(s string) bool {
	if len(s) != 30 || !strings.HasPrefix(s, "ses_") {
		return false
	}
	hexPart := s[4:16]
	for _, c := range hexPart {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	base62Part := s[16:30]
	for _, c := range base62Part {
		if !((c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')) {
			return false
		}
	}
	return true
}

// openCodeNativeSession extracts a valid canonical x-opencode-session header.
func openCodeNativeSession(headers http.Header) string {
	if headers == nil {
		return ""
	}
	for key, values := range headers {
		if strings.EqualFold(key, openCodeSessionHeader) {
			for _, value := range values {
				normalized := openCodeNormalizeSession(value)
				if normalized != "" && openCodeIsValidSessionFormat(normalized) {
					return normalized
				}
			}
		}
	}
	return ""
}

// openCodeResolveSession resolves a stable session identity from the request context.
//
// Resolution priority:
//  1. Native x-opencode-session header (authoritative if present and valid format)
//  2. Extracted session from downstream request (via ExtractSessionID)
//  3. Connection ID from auth attributes
//  4. Fallback: generate a new canonical session
func openCodeResolveSession(headers http.Header, payload []byte, auth *cliproxyauth.Auth, clientTool string) string {
	// 1. Native header
	if native := openCodeNativeSession(headers); native != "" {
		return native
	}

	// 2. Extracted session from downstream
	if extracted := cliproxyauth.ExtractSessionID(headers, payload, nil); extracted != "" {
		return openCodeTranslateSession(extracted, clientTool)
	}

	// 3. Connection ID for stability
	if auth != nil && auth.Attributes != nil {
		if connectionID := auth.Attributes["connection_id"]; connectionID != "" {
			return openCodeTranslateSession(connectionID, clientTool)
		}
	}

	// 4. Fallback
	return openCodeGenerateSessionId()
}

// openCodeDeriveRequestId derives a request ID deterministically from session + last user text.
func openCodeDeriveRequestId(sessionId string, payload []byte) string {
	text := ""
	if payload != nil {
		var body map[string]any
		if json.Unmarshal(payload, &body) == nil {
			if msgs, ok := body["messages"].([]any); ok && len(msgs) > 0 {
				for i := len(msgs) - 1; i >= 0; i-- {
					msg, ok := msgs[i].(map[string]any)
					if !ok {
						continue
					}
					role, _ := msg["role"].(string)
					if role != "user" {
						continue
					}
					switch c := msg["content"].(type) {
					case string:
						if strings.TrimSpace(c) != "" {
							text = c
							if len(text) > 600 {
								text = text[len(text)-600:]
							}
							break
						}
					}
					if text != "" {
						break
					}
				}
			}
		}
	}
	if text == "" {
		return openCodeGenerateRequestId()
	}
	input := "opencode-req\x00" + sessionId + "\x00" + text
	sum := sha256.Sum256([]byte(input))
	hexPart := ""
	for i := 0; i < 6; i++ {
		hexPart += string("0123456789abcdef"[sum[i]>>4])
		hexPart += string("0123456789abcdef"[sum[i]&0x0f])
	}
	base62Part := ""
	for i := 6; i < 20 && i < len(sum); i++ {
		base62Part += string(base62Chars[sum[i]%62])
	}
	return "msg_" + hexPart + base62Part
}

// openCodeCloakDecoyTools injects decoy bash/read tools if not already present.
func openCodeCloakDecoyTools(payload []byte) []byte {
	if payload == nil {
		return payload
	}
	var body map[string]any
	if json.Unmarshal(payload, &body) != nil {
		return payload
	}
	existing := map[string]bool{}
	if tools, ok := body["tools"].([]any); ok {
		for _, t := range tools {
			tm, ok := t.(map[string]any)
			if !ok {
				continue
			}
			name := ""
			if fn, ok := tm["function"].(map[string]any); ok {
				name, _ = fn["name"].(string)
			}
			if name == "" {
				name, _ = tm["name"].(string)
			}
			if name != "" {
				existing[name] = true
			}
		}
	}
	needsTools := len(existing) == 0
	if !needsTools {
		needsTools = !existing["bash"] || !existing["read"]
	}
	if needsTools {
		if _, ok := body["tools"].([]any); !ok {
			body["tools"] = []any{}
		}
		tools := body["tools"].([]any)
		for _, decoy := range openCodeDecoyTools {
			dcopy := map[string]any{
				"type": decoy["type"],
				"function": map[string]any{
					"name":        decoy["function"].(map[string]any)["name"],
					"description": decoy["function"].(map[string]any)["description"],
					"parameters":  decoy["function"].(map[string]any)["parameters"],
				},
			}
			name := dcopy["function"].(map[string]any)["name"].(string)
			if !existing[name] {
				tools = append(tools, dcopy)
			}
		}
		body["tools"] = tools
		if _, hasTC := body["tool_choice"]; !hasTC {
			body["tool_choice"] = "none"
		}
	}
	out, _ := json.Marshal(body)
	return out
}

// openCodeForceStream ensures body.stream is true for free-tier SSE aggregation.
func openCodeForceStream(payload []byte) []byte {
	if payload == nil {
		return payload
	}
	var body map[string]any
	if json.Unmarshal(payload, &body) != nil {
		return payload
	}
	body["stream"] = true
	out, _ := json.Marshal(body)
	return out
}

// OpenCodeExecutor is a dedicated executor for OpenCode's zen API.
// It wraps OpenAICompatExecutor and adds OpenCode-specific headers and session management.
type OpenCodeExecutor struct {
	*OpenAICompatExecutor
	cfg *config.Config
}

func NewOpenCodeExecutor(cfg *config.Config) *OpenCodeExecutor {
	return &OpenCodeExecutor{
		OpenAICompatExecutor: NewOpenAICompatExecutor("opencode", cfg),
		cfg:                  cfg,
	}
}

func (e *OpenCodeExecutor) Identifier() string { return "opencode" }

// PrepareRequest injects OpenCode-specific credentials and headers into the outgoing HTTP request.
func (e *OpenCodeExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}

	// Free tier: always use "Bearer public"
	req.Header.Set("Authorization", "Bearer public")

	// Apply custom headers from auth attributes
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)

	// User-Agent: canonical version string for free-tier fingerprint validation
	req.Header.Set("User-Agent", openCodeUA)

	// Resolve session
	resolvedSession := openCodeResolveSession(req.Header, nil, auth, "desktop")
	if req.Header.Get(openCodeSessionHeader) == "" {
		req.Header.Set(openCodeSessionHeader, resolvedSession)
	}

	// Request ID: derive from session
	if req.Header.Get("x-opencode-request") == "" {
		req.Header.Set("x-opencode-request", openCodeDeriveRequestId(resolvedSession, nil))
	}

	if req.Header.Get("x-opencode-project") == "" {
		req.Header.Set("x-opencode-project", "global")
	}
	if req.Header.Get("x-opencode-client") == "" {
		req.Header.Set("x-opencode-client", "desktop")
	}

	return nil
}

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

	// Free tier: always use "Bearer public"
	auth.Attributes["header:Authorization"] = "Bearer public"
	auth.Attributes["header:User-Agent"] = openCodeUA

	// Resolve session
	if auth.Attributes["header:"+openCodeSessionHeader] == "" {
		resolvedSession := openCodeResolveSession(headers, payload, auth, clientTool)
		auth.Attributes["header:"+openCodeSessionHeader] = resolvedSession
	}

	// Request ID
	if auth.Attributes["header:x-opencode-request"] == "" {
		sessionID := auth.Attributes["header:"+openCodeSessionHeader]
		auth.Attributes["header:x-opencode-request"] = openCodeDeriveRequestId(sessionID, payload)
	}

	if auth.Attributes["header:x-opencode-project"] == "" {
		auth.Attributes["header:x-opencode-project"] = "global"
	}
	if auth.Attributes["header:x-opencode-client"] == "" {
		auth.Attributes["header:x-opencode-client"] = clientTool
	}
}

// Execute performs a non-streaming chat completion request to OpenCode.
func (e *OpenCodeExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	clientTool := extractClientTool(req.Metadata)
	headers := opts.Headers
	injectOpenCodeHeaders(auth, headers, req.Payload, clientTool)
	// Force stream + decoy tools for free tier
	req.Payload = openCodeForceStream(req.Payload)
	req.Payload = openCodeCloakDecoyTools(req.Payload)
	return e.OpenAICompatExecutor.Execute(ctx, auth, req, opts)
}

// ExecuteStream performs a streaming chat completion request to OpenCode.
func (e *OpenCodeExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	clientTool := extractClientTool(req.Metadata)
	headers := opts.Headers
	injectOpenCodeHeaders(auth, headers, req.Payload, clientTool)
	// Force stream + decoy tools for free tier
	req.Payload = openCodeForceStream(req.Payload)
	req.Payload = openCodeCloakDecoyTools(req.Payload)
	return e.OpenAICompatExecutor.ExecuteStream(ctx, auth, req, opts)
}

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
