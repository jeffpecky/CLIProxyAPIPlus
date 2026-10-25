package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/sjson"
)

// openCodeAnthropicVersion is the Anthropic Messages API version OpenCode's
// relay expects on the Claude-format lane.
const openCodeAnthropicVersion = "2023-06-01"

// openCodeDefaultMessagesURL is the free-tier Claude-format endpoint. OpenCode v2
// moved the free lane to /inference/v1 (see openCodeBaseURL).
const openCodeDefaultMessagesURL = "https://opencode.ai/inference/v1/messages"

// openCodeMessagesModels are free-tier ids served only by /messages. 9router's
// free executor keeps the same explicit set (MESSAGES_MODELS).
var openCodeMessagesModels = map[string]bool{
	"union-alpha": true,
}

// openCodeClaudeMessagesRe matches Claude-format model families. On the Go lane
// /chat/completions answers ModelProtocolUnsupported for these ids, so they must
// use /messages even when the caller is on the OpenAI wire.
var openCodeClaudeMessagesRe = regexp.MustCompile(`(?i)^claude-`)

// openCodeBaseModelID strips a provider prefix and a thinking suffix so
// capability checks see the upstream id.
func openCodeBaseModelID(model string) string {
	base := strings.TrimSpace(model)
	if i := strings.IndexAny(base, "( "); i >= 0 {
		base = strings.TrimSpace(base[:i])
	}
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	return base
}

// openCodeNeedsMessages reports whether a free-tier model is served by
// /inference/v1/messages.
func openCodeNeedsMessages(model string) bool {
	return openCodeMessagesModels[openCodeBaseModelID(model)]
}

// openCodeGoNeedsMessages reports whether an OpenCode Go model is served by
// /zen/go/v1/messages. Claude-family ids are the only ones that require it.
func openCodeGoNeedsMessages(model string) bool {
	return openCodeClaudeMessagesRe.MatchString(openCodeGoBaseModelID(model))
}

// openCodeMessagesURL resolves the free-tier /messages endpoint. A configured
// base_url (used by tests and self-hosted relays) overrides the default host.
func openCodeMessagesURL(auth *cliproxyauth.Auth) string {
	if auth != nil && auth.Attributes != nil {
		if base := strings.TrimSpace(auth.Attributes["base_url"]); base != "" {
			return strings.TrimRight(base, "/") + "/messages"
		}
	}
	return openCodeDefaultMessagesURL
}

// openCodeGoMessagesURL resolves the Go /messages endpoint from the same base
// URL the chat and responses lanes use.
func openCodeGoMessagesURL(auth *cliproxyauth.Auth) string {
	return strings.TrimRight(openCodeGoBaseURL(auth), "/") + "/messages"
}

// applyOpenCodeMessagesHeaders applies the Claude-format header set. The free
// lane keeps the v2 identity headers and Bearer public; the Go lane swaps the
// bearer token for a raw x-api-key, matching 9router's claude transport.
func (e *OpenCodeExecutor) applyOpenCodeMessagesHeaders(req *http.Request, auth *cliproxyauth.Auth, goLane bool) {
	if req == nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", openCodeAnthropicVersion)
	e.applyOpenCodeHTTPHeaders(req, auth)
	if !goLane {
		return
	}

	applyOpenCodeGoAuthHeaders(req, auth)
	req.Header.Del("Authorization")
	_, apiKey := e.resolveCredentials(auth)
	if strings.TrimSpace(apiKey) == "" && auth != nil && auth.Attributes != nil {
		apiKey = auth.Attributes["api_key"]
	}
	if apiKey = strings.TrimSpace(apiKey); apiKey != "" {
		req.Header.Set("x-api-key", apiKey)
	}
}

// openCodeTranslateMessagesRequest converts the caller's payload to the Claude
// Messages format. It preserves the original request in opts.OriginalRequest so
// the response side can translate back to the client's wire format.
func (e *OpenCodeExecutor) openCodeTranslateMessagesRequest(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, upstreamStream bool) []byte {
	from := opts.SourceFormat
	to := sdktranslator.FromString("claude")
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	isCompat := helps.APIKeyModelIsCompat(req)

	_, body := helps.TranslateRequestPairWithAPIKeyModelCompatibility(
		ctx,
		opts.Headers,
		e.cfg,
		from,
		to,
		baseModel,
		opts.OriginalRequest,
		req.Payload,
		upstreamStream,
		isCompat,
	)
	if len(body) == 0 {
		body = req.Payload
	}
	body = helps.SetStringIfDifferent(body, "model", baseModel)
	if updated, err := sjson.SetBytes(body, "stream", upstreamStream); err == nil {
		body = updated
	}
	body = openCodeApplyMessagesThinkingSuffix(body, req.Model)
	return openCodeNormalizeClaudeAdaptiveThinking(body, baseModel)
}

// openCodeApplyMessagesThinkingSuffix applies a model-name thinking suffix to
// the translated Claude body. The chat lane normally gets this from
// ApplyRequestThinking, which the /messages lane bypasses.
func openCodeApplyMessagesThinkingSuffix(body []byte, model string) []byte {
	parsed := thinking.ParseSuffix(model)
	if !parsed.HasSuffix || !openCodeClaudeMessagesRe.MatchString(openCodeBaseModelID(parsed.ModelName)) {
		return body
	}

	budget := 0
	switch {
	case strings.EqualFold(parsed.RawSuffix, "none"):
		budget = 0
	case strings.EqualFold(parsed.RawSuffix, "auto"), parsed.RawSuffix == "-1":
		budget = -1
	default:
		if parsedBudget, ok := thinking.ParseNumericSuffix(parsed.RawSuffix); ok {
			budget = parsedBudget
		} else if level, okLevel := thinking.ParseLevelSuffix(parsed.RawSuffix); okLevel {
			mapped, _ := thinking.ConvertLevelToBudget(string(level))
			budget = mapped
		} else {
			return body
		}
	}

	var root map[string]any
	if json.Unmarshal(body, &root) != nil {
		return body
	}
	thinkingConfig, _ := root["thinking"].(map[string]any)
	if thinkingConfig == nil {
		thinkingConfig = map[string]any{}
	}
	thinkingConfig["type"] = "enabled"
	thinkingConfig["budget_tokens"] = float64(budget)
	root["thinking"] = thinkingConfig
	out, err := json.Marshal(root)
	if err != nil {
		return body
	}
	return out
}

// openCodeNormalizeClaudeAdaptiveThinking rewrites legacy budget thinking into
// the adaptive shape OpenCode's Claude-format models require. OpenCode Go's
// claude-haiku-5-5 rejects thinking.type "enabled" with 400 and asks for
// thinking.type "adaptive" + output_config.effort (9router provider override).
func openCodeNormalizeClaudeAdaptiveThinking(body []byte, model string) []byte {
	if len(body) == 0 || !openCodeClaudeMessagesRe.MatchString(openCodeBaseModelID(model)) {
		return body
	}
	var root map[string]any
	if json.Unmarshal(body, &root) != nil {
		return body
	}
	thinkingConfig, ok := root["thinking"].(map[string]any)
	if !ok {
		return body
	}
	thinkingType, _ := thinkingConfig["type"].(string)
	switch {
	case strings.EqualFold(thinkingType, "enabled"):
		budget, hasBudget := thinkingConfig["budget_tokens"].(float64)
		delete(thinkingConfig, "budget_tokens")
		if !hasBudget {
			// No budget means adaptive with upstream-chosen effort.
			thinkingConfig["type"] = "adaptive"
			break
		}
		level, okLevel := thinking.ConvertBudgetToLevel(int(budget))
		if !okLevel {
			level = "high"
		}
		switch level {
		case string(thinking.LevelNone):
			// budget_tokens 0 means thinking is off, not adaptive.
			thinkingConfig["type"] = "disabled"
			if outputConfig, ok := root["output_config"].(map[string]any); ok {
				delete(outputConfig, "effort")
				if len(outputConfig) == 0 {
					delete(root, "output_config")
				}
			}
		case string(thinking.LevelAuto):
			// Adaptive with no explicit effort lets upstream choose.
			thinkingConfig["type"] = "adaptive"
		default:
			thinkingConfig["type"] = "adaptive"
			effort, okEffort := thinking.MapToClaudeEffort(level, true)
			if !okEffort {
				effort = "high"
			}
			outputConfig, _ := root["output_config"].(map[string]any)
			if outputConfig == nil {
				outputConfig = map[string]any{}
			}
			outputConfig["effort"] = effort
			root["output_config"] = outputConfig
		}
	case strings.EqualFold(thinkingType, "adaptive"):
		// Adaptive thinking is already the required shape. Drop a stale budget
		// if a caller supplied both.
		delete(thinkingConfig, "budget_tokens")
	default:
		return body
	}
	root["thinking"] = thinkingConfig
	out, err := json.Marshal(root)
	if err != nil {
		return body
	}
	return out
}

// executeMessages performs a non-streaming Claude-format request. Both lanes
// stream upstream: the free tier rejects non-stream requests outright, and the
// registered Claude→OpenAI non-stream translators consume SSE. Native Claude
// callers get the aggregated message body instead.
func (e *OpenCodeExecutor) executeMessages(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, goLane bool) (cliproxyexecutor.Response, error) {
	if len(opts.OriginalRequest) == 0 {
		opts.OriginalRequest = req.Payload
	}
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	opts.ResponseFormat = responseFormat

	body := e.openCodeTranslateMessagesRequest(ctx, req, opts, true)

	url := openCodeMessagesURL(auth)
	if goLane {
		url = openCodeGoMessagesURL(auth)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	e.applyOpenCodeMessagesHeaders(httpReq, auth, goLane)
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	defer func() {
		_ = httpResp.Body.Close()
	}()
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		return cliproxyexecutor.Response{}, statusErr{code: httpResp.StatusCode, msg: string(b)}
	}

	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	upstream := sdktranslator.FromString("claude")
	if responseFormat == upstream {
		// Native Claude callers need one message object, not SSE frames.
		aggregated, aggErr := openCodeAggregateClaudeSSE(bytes.NewReader(data))
		if aggErr != nil {
			return cliproxyexecutor.Response{}, aggErr
		}
		return cliproxyexecutor.Response{Payload: aggregated, Headers: httpResp.Header.Clone()}, nil
	}

	var param any
	out := sdktranslator.TranslateNonStream(ctx, upstream, responseFormat, req.Model, opts.OriginalRequest, body, data, &param)
	if len(out) == 0 {
		out = data
	}
	return cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}, nil
}

// executeMessagesStream streams a Claude-format response. Native Claude clients
// receive the upstream SSE frames verbatim; every other client is translated
// event by event.
func (e *OpenCodeExecutor) executeMessagesStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, goLane bool) (*cliproxyexecutor.StreamResult, error) {
	if len(opts.OriginalRequest) == 0 {
		opts.OriginalRequest = req.Payload
	}
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	opts.ResponseFormat = responseFormat

	body := e.openCodeTranslateMessagesRequest(ctx, req, opts, true)

	url := openCodeMessagesURL(auth)
	if goLane {
		url = openCodeGoMessagesURL(auth)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	e.applyOpenCodeMessagesHeaders(httpReq, auth, goLane)
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
	upstream := sdktranslator.FromString("claude")
	go func() {
		defer close(out)
		defer func() {
			_ = httpResp.Body.Close()
		}()

		if responseFormat == upstream {
			// Native Claude clients expect the raw event frames.
			buf := make([]byte, 32*1024)
			for {
				n, readErr := httpResp.Body.Read(buf)
				if n > 0 {
					chunk := bytes.Clone(buf[:n])
					select {
					case out <- cliproxyexecutor.StreamChunk{Payload: chunk}:
					case <-ctx.Done():
						return
					}
				}
				if readErr != nil {
					if readErr != io.EOF {
						select {
						case out <- cliproxyexecutor.StreamChunk{Err: readErr}:
						case <-ctx.Done():
						}
					}
					return
				}
			}
		}

		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800)
		var param any
		for scanner.Scan() {
			line := scanner.Bytes()
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) == 0 || !bytes.HasPrefix(trimmed, []byte("data:")) {
				continue
			}
			payload := bytes.TrimSpace(trimmed[len("data:"):])
			if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
				continue
			}
			if !json.Valid(payload) {
				select {
				case out <- cliproxyexecutor.StreamChunk{Err: statusErr{code: http.StatusBadGateway, msg: "invalid Claude SSE payload"}}:
				case <-ctx.Done():
				}
				return
			}
			chunks := helps.TranslateStreamWithClaudeInputTokens(
				ctx,
				upstream,
				responseFormat,
				req.Model,
				opts.OriginalRequest,
				body,
				bytes.Clone(trimmed),
				&param,
				nil,
			)
			for _, chunk := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunk}:
				case <-ctx.Done():
					return
				}
			}
		}
		if scanErr := scanner.Err(); scanErr != nil {
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: scanErr}:
			case <-ctx.Done():
			}
		}
	}()

	return &cliproxyexecutor.StreamResult{
		Chunks:  out,
		Headers: httpResp.Header.Clone(),
	}, nil
}

// openCodeAggregateClaudeSSE folds a Claude Messages SSE stream into a single
// non-streaming message body. The free tier rejects non-stream requests, so a
// non-stream client is served by requesting SSE and aggregating it here.
func openCodeAggregateClaudeSSE(reader io.Reader) ([]byte, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(nil, 52_428_800)

	var message map[string]any
	blocks := map[int]map[string]any{}
	toolJSON := map[int]*strings.Builder{}
	usage := map[string]any{}
	var stopReason any
	var stopSequence any

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(line[len("data:"):])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		var event map[string]any
		if json.Unmarshal(payload, &event) != nil {
			continue
		}
		eventType, _ := event["type"].(string)
		switch eventType {
		case "message_start":
			if msg, ok := event["message"].(map[string]any); ok {
				message = msg
				if u, ok := msg["usage"].(map[string]any); ok {
					openCodeMergeUsage(usage, u)
				}
				if content, ok := msg["content"].([]any); ok {
					for i, raw := range content {
						if block, ok := raw.(map[string]any); ok {
							blocks[i] = block
						}
					}
				}
			}
		case "content_block_start":
			idx, ok := openCodeEventIndex(event["index"])
			if !ok {
				continue
			}
			if block, ok := event["content_block"].(map[string]any); ok {
				blocks[idx] = block
			}
		case "content_block_delta":
			idx, ok := openCodeEventIndex(event["index"])
			if !ok {
				continue
			}
			block := blocks[idx]
			delta, _ := event["delta"].(map[string]any)
			if block == nil || delta == nil {
				continue
			}
			deltaType, _ := delta["type"].(string)
			switch deltaType {
			case "text_delta":
				text, _ := delta["text"].(string)
				block["text"] = openCodeStringValue(block["text"]) + text
			case "thinking_delta":
				text, _ := delta["thinking"].(string)
				block["thinking"] = openCodeStringValue(block["thinking"]) + text
			case "signature_delta":
				if signature, ok := delta["signature"].(string); ok {
					block["signature"] = signature
				}
			case "input_json_delta":
				partial, _ := delta["partial_json"].(string)
				builder := toolJSON[idx]
				if builder == nil {
					builder = &strings.Builder{}
					toolJSON[idx] = builder
				}
				builder.WriteString(partial)
			}
		case "content_block_stop":
			idx, ok := openCodeEventIndex(event["index"])
			if !ok {
				continue
			}
			block := blocks[idx]
			builder := toolJSON[idx]
			if block == nil || builder == nil || builder.Len() == 0 {
				continue
			}
			var input any
			if json.Unmarshal([]byte(builder.String()), &input) == nil {
				block["input"] = input
			}
		case "message_delta":
			if delta, ok := event["delta"].(map[string]any); ok {
				if v, has := delta["stop_reason"]; has {
					stopReason = v
				}
				if v, has := delta["stop_sequence"]; has {
					stopSequence = v
				}
			}
			if u, ok := event["usage"].(map[string]any); ok {
				openCodeMergeUsage(usage, u)
			}
		case "error":
			if errorData, ok := event["error"].(map[string]any); ok {
				msg, _ := errorData["message"].(string)
				return nil, statusErr{code: http.StatusBadGateway, msg: msg}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	indices := make([]int, 0, len(blocks))
	for idx := range blocks {
		indices = append(indices, idx)
	}
	sort.Ints(indices)
	content := make([]any, 0, len(indices))
	for _, idx := range indices {
		content = append(content, blocks[idx])
	}

	result := map[string]any{}
	for key, value := range message {
		result[key] = value
	}
	result["type"] = "message"
	result["role"] = "assistant"
	result["content"] = content
	if stopReason != nil {
		result["stop_reason"] = stopReason
	}
	if stopSequence != nil {
		result["stop_sequence"] = stopSequence
	}
	if len(usage) > 0 {
		result["usage"] = usage
	}
	out, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func openCodeMergeUsage(dst map[string]any, src map[string]any) {
	for key, value := range src {
		dst[key] = value
	}
}

func openCodeEventIndex(value any) (int, bool) {
	switch v := value.(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case json.Number:
		n, err := v.Int64()
		return int(n), err == nil
	default:
		return 0, false
	}
}

func openCodeStringValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}
