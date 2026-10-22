package executor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	codexopenai "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/codex/openai/chat-completions"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	// OpenCode free tier moved from /zen/v1 to /inference/v1 (OpenCode v2);
	// /zen/v1 now answers every free request with 403 FreeTierError.
	openCodeBaseURL          = "https://opencode.ai/inference/v1"
	openCodeResponsesURL     = "https://opencode.ai/inference/v1/responses"
	openCodeSessionHeader    = "x-opencode-session"
	openCodeMaxSessionLength = 256
	// openCodeFreeUA is the OpenCode v2 client fingerprint; upstream rejects the
	// v1 "opencode/1.18.x" form with 403 FreeTierError since 2026-10-14.
	openCodeFreeUA = "opencode/latest/2.0.26/cli"
	// openCodeFreeClient is the v2 x-opencode-client value (v1 used "desktop").
	openCodeFreeClient = "cli"
	// openCodeUA is the legacy v1 UA still used by the OpenCode Go lane.
	openCodeUA = "opencode/1.18.31"
)

// openCodeMuseSparkRe matches muse-spark model IDs.
var openCodeMuseSparkRe = regexp.MustCompile(`(?i)^(?:.+/)?muse[-_]?spark(?:$|[-_:.\s])`)

var base62Chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

func openCodeBase62Random(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	var out strings.Builder
	for i := 0; i < n; i++ {
		out.WriteByte(base62Chars[int(b[i])%62])
	}
	return out.String()
}

var (
	openCodeSessionMu     sync.Mutex
	openCodeLastTimestamp int64
	openCodeCounter       uint16
)

// openCodeGenerateSessionId generates a canonical session ID:
// ses_ + 12 hex chars (from inverted timestamp) + 14 base62 chars = 30 chars total.
func openCodeGenerateSessionId() string {
	openCodeSessionMu.Lock()
	nowMs := time.Now().UnixMilli()
	if nowMs != openCodeLastTimestamp {
		openCodeLastTimestamp = nowMs
		openCodeCounter = 0
	}
	openCodeCounter++
	cnt := openCodeCounter
	openCodeSessionMu.Unlock()

	current := (uint64(nowMs) * 0x1000) + uint64(cnt)
	inverted := ^current
	hex12 := ""
	for i := 5; i >= 0; i-- {
		hex12 += string("0123456789abcdef"[(inverted>>(uint(i)*8))&0xff>>4])
		hex12 += string("0123456789abcdef"[(inverted>>(uint(i)*8))&0xff&0x0f])
	}
	return "ses_" + hex12 + openCodeBase62Random(14)
}

// openCodeGenerateRequestId generates a canonical request ID:
// msg_ + 12 hex chars + 14 base62 chars = 30 chars total.
func openCodeGenerateRequestId() string {
	nowMs := time.Now().UnixMilli()
	current := (uint64(nowMs) * 0x1000) + 1
	hex12 := ""
	for i := 5; i >= 0; i-- {
		hex12 += string("0123456789abcdef"[(current>>(uint(i)*8))&0xff>>4])
		hex12 += string("0123456789abcdef"[(current>>(uint(i)*8))&0xff&0x0f])
	}
	return "msg_" + hex12 + openCodeBase62Random(14)
}

// Session prefix = 12 hex of ~(timeMs * 0x1000 + counter), low 48 bits, like the
// CLI. Upstream decodes it and rejects a non-small counter with 403 FreeTierError.
const (
	openCodeSessionPrefixMask = (1 << 48) - 1
	openCodeSessionTimeMask   = (1 << 36) - 1
)

// openCodeDescendingTimeHex encodes ~(timeValue*0x1000 + counter) into 12 hex.
func openCodeDescendingTimeHex(timeValue uint64, counter uint64) string {
	inverted := ^(timeValue*0x1000 + counter)
	hexPart := ""
	for i := 5; i >= 0; i-- {
		hexPart += string("0123456789abcdef"[(inverted>>(uint(i)*8))&0xff>>4])
		hexPart += string("0123456789abcdef"[(inverted>>(uint(i)*8))&0xff&0x0f])
	}
	return hexPart
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
	// Deterministic time field from the digest, counter pinned to 1 (what the CLI
	// sends); a non-canonical prefix is rejected upstream with 403 FreeTierError.
	timeValue := uint64(0)
	for i := 0; i < 5; i++ {
		timeValue = (timeValue << 8) | uint64(sum[i])
	}
	timeValue &= openCodeSessionTimeMask
	hexPart := openCodeDescendingTimeHex(timeValue, 1)
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

// openCodeNativeSession extracts a valid canonical session header.
func openCodeNativeSession(headers http.Header) string {
	if headers == nil {
		return ""
	}
	for _, headerKey := range []string{openCodeSessionHeader, "x-session-id", "x-session-affinity"} {
		for key, values := range headers {
			if strings.EqualFold(key, headerKey) {
				for _, value := range values {
					normalized := openCodeNormalizeSession(value)
					if normalized != "" && openCodeIsValidSessionFormat(normalized) {
						return normalized
					}
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

// openCodeIsMuseSpark returns true when the model should use /responses.
func openCodeIsMuseSpark(model string) bool {
	return openCodeMuseSparkRe.MatchString(model)
}

// openCodeChatToResponses converts a Chat Completions body to Responses API format.
// Returns nil if conversion is not needed or fails.
func openCodeChatToResponses(payload []byte) []byte {
	if payload == nil {
		return nil
	}
	var body map[string]any
	if json.Unmarshal(payload, &body) != nil {
		return nil
	}

	out := map[string]any{}
	if m, ok := body["model"].(string); ok {
		out["model"] = m
	}

	// Instructions from top-level system if present
	if sys, ok := body["system"].(string); ok && sys != "" {
		out["instructions"] = sys
	} else if sysParts, ok := body["system"].([]any); ok && len(sysParts) > 0 {
		var sb strings.Builder
		for _, sp := range sysParts {
			if sm, ok := sp.(map[string]any); ok {
				if t, ok := sm["text"].(string); ok {
					sb.WriteString(t)
				}
			}
		}
		if sb.Len() > 0 {
			out["instructions"] = sb.String()
		}
	}

	// Convert messages → input
	if msgs, ok := body["messages"].([]any); ok && len(msgs) > 0 {
		var input []any
		for _, rawMsg := range msgs {
			msg, ok := rawMsg.(map[string]any)
			if !ok {
				continue
			}
			role, _ := msg["role"].(string)
			content := msg["content"]
			switch role {
			case "system":
				// system messages become instructions (first one wins)
				if _, hasInstr := out["instructions"]; !hasInstr {
					switch s := content.(type) {
					case string:
						out["instructions"] = s
					case []any:
						var sb strings.Builder
						for _, sp := range s {
							if sm, ok := sp.(map[string]any); ok {
								if t, ok := sm["text"].(string); ok {
									sb.WriteString(t)
								}
							}
						}
						out["instructions"] = sb.String()
					}
				}
			case "user":
				item := map[string]any{"type": "message", "role": "user"}
				switch c := content.(type) {
				case string:
					item["content"] = []any{map[string]any{"type": "input_text", "text": c}}
				case []any:
					var parts []any
					for _, p := range c {
						pm, ok := p.(map[string]any)
						if !ok {
							continue
						}
						pType, _ := pm["type"].(string)
						if pType == "text" || pType == "input_text" {
							txt, _ := pm["text"].(string)
							parts = append(parts, map[string]any{"type": "input_text", "text": txt})
						} else {
							parts = append(parts, pm)
						}
					}
					item["content"] = parts
				default:
					item["content"] = c
				}
				input = append(input, item)
			case "assistant":
				item := map[string]any{"type": "message", "role": "assistant"}
				switch c := content.(type) {
				case string:
					item["content"] = []any{map[string]any{"type": "output_text", "text": c}}
				case []any:
					var parts []any
					for _, p := range c {
						pm, ok := p.(map[string]any)
						if !ok {
							continue
						}
						pType, _ := pm["type"].(string)
						if pType == "text" || pType == "output_text" {
							txt, _ := pm["text"].(string)
							parts = append(parts, map[string]any{"type": "output_text", "text": txt})
						} else {
							parts = append(parts, pm)
						}
					}
					item["content"] = parts
				default:
					item["content"] = c
				}
				input = append(input, item)
			}
		}
		if len(input) == 0 {
			input = []any{map[string]any{
				"type": "message", "role": "user",
				"content": []any{map[string]any{"type": "input_text", "text": "..."}},
			}}
		}
		out["input"] = input
	}

	// max_tokens / max_completion_tokens → max_output_tokens
	if v, ok := body["max_completion_tokens"]; ok {
		out["max_output_tokens"] = v
	} else if v, ok := body["max_tokens"]; ok {
		out["max_output_tokens"] = v
	}

	out["stream"] = true
	out["store"] = false

	// Copy tools in Responses format (flat, no "function" wrapper)
	if tools, ok := body["tools"].([]any); ok && len(tools) > 0 {
		var rtools []any
		for _, t := range tools {
			tm, ok := t.(map[string]any)
			if !ok {
				continue
			}
			fn, _ := tm["function"].(map[string]any)
			name := ""
			desc := ""
			var params any
			if fn != nil {
				name, _ = fn["name"].(string)
				desc, _ = fn["description"].(string)
				params = fn["parameters"]
			}
			if name == "" {
				name, _ = tm["name"].(string)
			}
			if desc == "" {
				desc, _ = tm["description"].(string)
			}
			if params == nil {
				params, _ = tm["parameters"]
			}
			if params == nil {
				params = tm["input_schema"]
			}
			if params == nil {
				params = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			if name != "" {
				rtools = append(rtools, map[string]any{
					"type":        "function",
					"name":        name,
					"description": desc,
					"parameters":  params,
				})
			}
		}
		out["tools"] = rtools
	}

	// tool_choice
	if tc, ok := body["tool_choice"]; ok {
		out["tool_choice"] = tc
	}

	result, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	return result
}

// openCodeCloakResponsesTools injects decoy tools in Responses API format.
func openCodeCloakResponsesTools(payload []byte) []byte {
	if payload == nil {
		return payload
	}
	var body map[string]any
	if json.Unmarshal(payload, &body) != nil {
		return payload
	}
	tools, _ := body["tools"].([]any)
	names := map[string]bool{}
	for _, t := range tools {
		if tm, ok := t.(map[string]any); ok {
			if n, ok := tm["name"].(string); ok {
				names[n] = true
			}
		}
	}
	if tools == nil {
		tools = []any{}
	}
	for _, name := range []string{"bash", "read"} {
		if !names[name] {
			tools = append(tools, map[string]any{
				"type":        "function",
				"name":        name,
				"description": "This tool is currently unavailable and must not be used.",
				"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
			})
		}
	}
	body["tools"] = tools
	if _, hasTC := body["tool_choice"]; !hasTC {
		body["tool_choice"] = "auto"
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
	// freeTier enables the OpenCode v2 free-tier fingerprint (Bearer public,
	// /inference/v1 headers and the read/shell tools). The OpenCode Go lane
	// embeds this executor but keeps its own keyed identity.
	freeTier bool
}

func NewOpenCodeExecutor(cfg *config.Config) *OpenCodeExecutor {
	return &OpenCodeExecutor{
		OpenAICompatExecutor: NewOpenAICompatExecutor("opencode", cfg),
		cfg:                  cfg,
		freeTier:             true,
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

	// User-Agent: OpenCode v2 client fingerprint for free-tier validation
	req.Header.Set("User-Agent", openCodeFreeUA)

	// Resolve session
	resolvedSession := openCodeResolveSession(req.Header, nil, auth, openCodeFreeClient)
	if req.Header.Get(openCodeSessionHeader) == "" {
		req.Header.Set(openCodeSessionHeader, resolvedSession)
	}

	// v2 identity: x-opencode-request is v1-only and now answered with 403.
	req.Header.Del("x-opencode-request")
	openCodeSetSessionHeaders(req.Header, resolvedSession)

	if req.Header.Get("x-opencode-project") == "" {
		req.Header.Set("x-opencode-project", "global")
	}
	if req.Header.Get("x-opencode-client") == "" {
		req.Header.Set("x-opencode-client", openCodeFreeClient)
	}

	return nil
}

// openCodeSetSessionHeaders mirrors the OpenCode v2 CLI, which sends the session
// id under several aliases next to x-opencode-session.
func openCodeSetSessionHeaders(headers http.Header, session string) {
	if headers == nil || session == "" {
		return
	}
	for _, name := range []string{"x-opencode-session-id", "x-session-affinity", "x-session-id"} {
		if headers.Get(name) == "" {
			headers.Set(name, session)
		}
	}
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
	auth.Attributes["header:User-Agent"] = openCodeFreeUA

	// Resolve session
	sessionID := auth.Attributes["header:"+openCodeSessionHeader]
	if sessionID == "" {
		sessionID = openCodeResolveSession(headers, payload, auth, clientTool)
		auth.Attributes["header:"+openCodeSessionHeader] = sessionID
	}

	// v2 identity: x-opencode-request is v1-only and now answered with 403.
	delete(auth.Attributes, "header:x-opencode-request")
	for _, name := range []string{"x-opencode-session-id", "x-session-affinity", "x-session-id"} {
		if auth.Attributes["header:"+name] == "" {
			auth.Attributes["header:"+name] = sessionID
		}
	}

	if auth.Attributes["header:x-opencode-project"] == "" {
		auth.Attributes["header:x-opencode-project"] = "global"
	}
	if auth.Attributes["header:x-opencode-client"] == "" {
		if clientTool == "" || clientTool == "generic" {
			clientTool = openCodeFreeClient
		}
		auth.Attributes["header:x-opencode-client"] = clientTool
	}
}

// normalizeToOpenAI ensures req.Payload is translated to standard OpenAI format
// so decoy tools and Responses API converter see valid OpenAI structures.
// It returns the client's expected response format.
func (e *OpenCodeExecutor) normalizeToOpenAI(ctx context.Context, req *cliproxyexecutor.Request, opts *cliproxyexecutor.Options) sdktranslator.Format {
	if len(opts.OriginalRequest) == 0 {
		opts.OriginalRequest = req.Payload
	}
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(*opts)
	opts.ResponseFormat = responseFormat

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	isCompat := helps.APIKeyModelIsCompat(*req)
	if from != to && from.String() != "" {
		req.Payload = helps.TranslateRequestWithAPIKeyModelCompatibility(ctx, opts.Headers, e.cfg, from, to, baseModel, req.Payload, opts.Stream, isCompat)
		opts.SourceFormat = to
	}
	return responseFormat
}

// Execute performs a non-streaming chat completion request to OpenCode.
func (e *OpenCodeExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	clientTool := extractClientTool(req.Metadata)
	headers := opts.Headers
	injectOpenCodeHeaders(auth, headers, req.Payload, clientTool)

	responseFormat := e.normalizeToOpenAI(ctx, &req, &opts)

	if openCodeIsMuseSpark(req.Model) {
		// muse-spark requires /responses endpoint with Responses API format
		return e.executeResponses(ctx, auth, req, opts, responseFormat)
	}

	// Force stream + free-tier fingerprint tools (chat/completions path)
	req.Payload = openCodeForceStream(req.Payload)
	toolNames := openCodeApplyFingerprintIfFree(e.freeTier, &req.Payload, false)
	resp, err := e.OpenAICompatExecutor.Execute(ctx, auth, req, opts)
	if err != nil {
		return resp, err
	}
	resp.Payload = helps.RestoreOpenCodeToolNames(resp.Payload, toolNames)
	return resp, nil
}

// ExecuteStream performs a streaming chat completion request to OpenCode.
func (e *OpenCodeExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	clientTool := extractClientTool(req.Metadata)
	headers := opts.Headers
	injectOpenCodeHeaders(auth, headers, req.Payload, clientTool)

	responseFormat := e.normalizeToOpenAI(ctx, &req, &opts)

	if openCodeIsMuseSpark(req.Model) {
		// muse-spark requires /responses endpoint with Responses API format
		return e.executeResponsesStream(ctx, auth, req, opts, responseFormat)
	}

	// Force stream + free-tier fingerprint tools (chat/completions path)
	req.Payload = openCodeForceStream(req.Payload)
	toolNames := openCodeApplyFingerprintIfFree(e.freeTier, &req.Payload, false)
	res, err := e.OpenAICompatExecutor.ExecuteStream(ctx, auth, req, opts)
	if err != nil {
		return res, err
	}
	if res != nil {
		res.Chunks = openCodeRestoreStreamChunks(ctx, res.Chunks, toolNames)
	}
	return res, nil
}

// openCodeApplyFingerprintIfFree applies the OpenCode v2 free-tier fingerprint
// in place and returns the alias -> original rename map (nil for other lanes).
func openCodeApplyFingerprintIfFree(freeTier bool, payload *[]byte, flat bool) map[string]string {
	if !freeTier || payload == nil {
		return nil
	}
	updated, names := helps.ApplyOpenCodeFingerprintTools(*payload, flat)
	*payload = updated
	return names
}

// openCodeRestoreStreamChunks maps concealed alias tool names back to the
// caller's originals on each streamed chunk.
func openCodeRestoreStreamChunks(ctx context.Context, in <-chan cliproxyexecutor.StreamChunk, names map[string]string) <-chan cliproxyexecutor.StreamChunk {
	if len(names) == 0 || in == nil {
		return in
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		for chunk := range in {
			if len(chunk.Payload) > 0 {
				chunk.Payload = helps.RestoreOpenCodeToolNames(chunk.Payload, names)
			}
			select {
			case out <- chunk:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

// executeResponses makes a direct HTTP call to /inference/v1/responses for muse-spark models.
func (e *OpenCodeExecutor) executeResponses(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, responseFormat sdktranslator.Format) (cliproxyexecutor.Response, error) {
	body := openCodeChatToResponses(req.Payload)
	if body == nil {
		body = req.Payload
	}
	var toolNames map[string]string
	if e.freeTier {
		body, toolNames = helps.ApplyOpenCodeFingerprintTools(body, true)
	} else {
		body = openCodeCloakResponsesTools(body)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, openCodeResponsesURL, bytes.NewReader(body))
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	e.applyOpenCodeHTTPHeaders(httpReq, auth)
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("opencode executor: close response body error: %v", errClose)
		}
	}()
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		return cliproxyexecutor.Response{}, statusErr{code: httpResp.StatusCode, msg: string(b)}
	}

	scanner := bufio.NewScanner(httpResp.Body)
	scanner.Buffer(nil, 52_428_800)
	var completedJSON []byte
	for scanner.Scan() {
		trimmed := bytes.TrimSpace(scanner.Bytes())
		if bytes.HasPrefix(trimmed, []byte("data:")) {
			data := bytes.TrimSpace(bytes.TrimPrefix(trimmed, []byte("data:")))
			if bytes.Contains(data, []byte(`"type":"response.completed"`)) {
				completedJSON = bytes.Clone(data)
			}
		}
	}
	if len(completedJSON) > 0 {
		var param any
		out := codexopenai.ConvertCodexResponseToOpenAINonStream(ctx, req.Model, opts.OriginalRequest, req.Payload, completedJSON, &param)
		if len(out) > 0 {
			to := sdktranslator.FromString("openai")
			if responseFormat != to && responseFormat.String() != "" {
				out = sdktranslator.TranslateNonStream(ctx, to, responseFormat, req.Model, opts.OriginalRequest, req.Payload, out, &param)
			}
			out = helps.RestoreOpenCodeToolNames(out, toolNames)
			return cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}, nil
		}
	}
	return cliproxyexecutor.Response{}, statusErr{code: http.StatusBadGateway, msg: "upstream responses stream completed without result"}
}

// executeResponsesStream makes a streaming HTTP call to /inference/v1/responses for muse-spark models
// and translates Responses API events into the client's expected stream format.
func (e *OpenCodeExecutor) executeResponsesStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, responseFormat sdktranslator.Format) (*cliproxyexecutor.StreamResult, error) {
	body := openCodeChatToResponses(req.Payload)
	if body == nil {
		body = req.Payload
	}
	var toolNames map[string]string
	if e.freeTier {
		body, toolNames = helps.ApplyOpenCodeFingerprintTools(body, true)
	} else {
		body = openCodeCloakResponsesTools(body)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, openCodeResponsesURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	e.applyOpenCodeHTTPHeaders(httpReq, auth)
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		_ = httpResp.Body.Close()
		return nil, statusErr{code: httpResp.StatusCode, msg: string(b)}
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	to := sdktranslator.FromString("openai")
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("opencode executor: close stream body error: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800) // 50MB
		var convParam any
		var claudeParam any
		claudeInputTokens := helps.NewClaudeInputTokenState(to, responseFormat, responseFormat, opts.OriginalRequest)
		// Detect whether the upstream is returning Chat Completions format
		// (has "object":"chat.completion.chunk") vs Responses API format (has "type":"response.*").
		// When the upstream returns Chat Completions chunks, forward them directly
		// instead of routing through ConvertCodexResponseToOpenAI which only handles Responses events.
		var upstreamFormat string // "" = unknown, "chat" = chat completions, "responses" = codex responses
		for scanner.Scan() {
			line := scanner.Bytes()
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) == 0 {
				continue
			}
			if !bytes.HasPrefix(trimmed, []byte("data:")) {
				continue
			}
			payload := bytes.TrimSpace(trimmed[len("data:"):])
			if bytes.Equal(payload, []byte("[DONE]")) {
				if responseFormat == to || responseFormat.String() == "" {
					select {
					case out <- cliproxyexecutor.StreamChunk{Payload: []byte("[DONE]")}:
					case <-ctx.Done():
						return
					}
				}
				continue
			}
			// Auto-detect upstream format from first data frame.
			if upstreamFormat == "" {
				if gjson.GetBytes(payload, "object").String() == "chat.completion.chunk" {
					upstreamFormat = "chat"
				} else {
					upstreamFormat = "responses"
				}
			}
			if upstreamFormat == "chat" {
				// Upstream already emits OpenAI Chat Completions SSE — forward the
				// bare payload; the openai handler adds the "data: " frame prefix.
				if responseFormat == to || responseFormat.String() == "" {
					chunk := bytes.Clone(payload)
					select {
					case out <- cliproxyexecutor.StreamChunk{Payload: chunk}:
					case <-ctx.Done():
						return
					}
				} else {
					streamLine := append([]byte("data: "), payload...)
					outChunks := helps.TranslateStreamWithClaudeInputTokens(ctx, to, responseFormat, req.Model, opts.OriginalRequest, req.Payload, streamLine, &claudeParam, claudeInputTokens)
					for _, oc := range outChunks {
						select {
						case out <- cliproxyexecutor.StreamChunk{Payload: oc}:
						case <-ctx.Done():
							return
						}
					}
				}
			} else {
				// Responses API format — translate through codex converter.
				chunks := codexopenai.ConvertCodexResponseToOpenAI(ctx, req.Model, opts.OriginalRequest, req.Payload, trimmed, &convParam)
				for _, chunk := range chunks {
					if responseFormat == to || responseFormat.String() == "" {
						chunkCopy := bytes.Clone(chunk)
						select {
						case out <- cliproxyexecutor.StreamChunk{Payload: chunkCopy}:
						case <-ctx.Done():
							return
						}
					} else {
						streamLine := append([]byte("data: "), chunk...)
						outChunks := helps.TranslateStreamWithClaudeInputTokens(ctx, to, responseFormat, req.Model, opts.OriginalRequest, req.Payload, streamLine, &claudeParam, claudeInputTokens)
						for _, oc := range outChunks {
							select {
							case out <- cliproxyexecutor.StreamChunk{Payload: oc}:
							case <-ctx.Done():
								return
							}
						}
					}
				}
			}
		}
	}()

	return &cliproxyexecutor.StreamResult{
		Chunks:  openCodeRestoreStreamChunks(ctx, out, toolNames),
		Headers: httpResp.Header.Clone(),
	}, nil
}

// applyOpenCodeHTTPHeaders sets common headers for direct OpenCode HTTP requests.
func (e *OpenCodeExecutor) applyOpenCodeHTTPHeaders(req *http.Request, auth *cliproxyauth.Auth) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer public")
	req.Header.Set("User-Agent", openCodeUA)

	if auth != nil && auth.Attributes != nil {
		for k, v := range auth.Attributes {
			if strings.HasPrefix(k, "header:") {
				name := strings.TrimPrefix(k, "header:")
				if name != "" && v != "" {
					req.Header.Set(name, v)
				}
			}
		}
	}
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
