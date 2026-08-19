package tokensaver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxHeadroomResponseBytes = 16 << 20

type headroomResult struct {
	applied  bool
	reason   string
	endpoint string
	stats    HeadroomStats
	before   SizeSnapshot
	after    SizeSnapshot
}

func applyHeadroom(root any, opts Options) (any, headroomResult) {
	result := headroomResult{}
	m, ok := root.(map[string]any)
	if !ok {
		result.reason = "root is not object"
		return root, result
	}
	result.before = captureSizeSnapshot(m)
	projection, reason := projectHeadroomMessages(m, opts.Format)
	if projection == nil {
		result.reason = reason
		return root, result
	}
	endpoint := headroomEndpoint(opts.Config.Headroom.URL)
	result.endpoint = maskEndpoint(endpoint)
	if endpoint == "" {
		result.reason = "url missing"
		return root, result
	}
	sourceMessages := cloneMessages(projection.messages)
	scope := headroomPrefixScope{
		Session: opts.Session, Format: opts.Format, Model: opts.Model, Endpoint: endpoint,
		Config: fmt.Sprintf("compress_user_messages=%t", opts.Config.Headroom.CompressUserMessages),
	}
	forwardMessages, frozenCount := defaultHeadroomPrefixCache.reuse(scope, projection.messages)
	payload := map[string]any{
		"messages": forwardMessages,
	}
	if frozenCount > 0 {
		payload["frozen_message_count"] = frozenCount
	}
	if opts.Model != "" {
		payload["model"] = opts.Model
	} else if model, ok := m["model"].(string); ok && model != "" {
		payload["model"] = model
	}
	if opts.Config.Headroom.CompressUserMessages {
		if cfg, ok := payload["config"].(map[string]any); ok {
			cfg["compress_user_messages"] = true
		} else {
			payload["config"] = map[string]any{"compress_user_messages": true}
		}
	}
	reqBody, err := json.Marshal(payload)
	if err != nil {
		result.reason = "marshal request failed"
		return root, result
	}
	timeout := opts.Config.Headroom.TimeoutMS
	if timeout <= 0 {
		timeout = 3000
	}
	ctx := opts.Context
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
	if err != nil {
		result.reason = "build request failed"
		return root, result
	}
	req.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: time.Duration(timeout) * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		result.reason = scrubSensitiveURLText(err.Error())
		return root, result
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		result.reason = fmt.Sprintf("status %d", resp.StatusCode)
		return root, result
	}
	if resp.ContentLength > maxHeadroomResponseBytes {
		result.reason = "response too large"
		return root, result
	}
	var out map[string]any
	limited := io.LimitReader(resp.Body, maxHeadroomResponseBytes+1)
	rawResponse, err := io.ReadAll(limited)
	if err != nil {
		result.reason = "read response failed"
		return root, result
	}
	if len(rawResponse) > maxHeadroomResponseBytes {
		result.reason = "response too large"
		return root, result
	}
	if err := json.Unmarshal(rawResponse, &out); err != nil {
		result.reason = "decode response failed"
		return root, result
	}
	result.stats = parseHeadroomStats(out)
	if result.stats.CompressionSkipped {
		result.reason = result.stats.SkipReason
		if result.reason == "" {
			result.reason = "compression skipped"
		}
		return root, result
	}
	if len(result.stats.CCRHashes) > 0 {
		result.reason = "CCR output unsupported"
		return root, result
	}
	compressed, ok := out["messages"].([]any)
	if !ok || len(compressed) == 0 {
		result.reason = "compressed messages missing"
		return root, result
	}
	next, reason := projection.apply(compressed)
	if next == nil {
		result.reason = reason
		return root, result
	}
	result.applied = true
	result.after = captureSizeSnapshot(next)
	defaultHeadroomPrefixCache.store(scope, sourceMessages, compressed)
	return next, result
}

func headroomEndpoint(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return ""
	}
	parsed, err := url.Parse(base)
	if err == nil && parsed.Scheme != "" && parsed.Host != "" {
		parsed.Path = strings.TrimRight(parsed.Path, "/")
		if !strings.HasSuffix(parsed.Path, "/v1/compress") {
			parsed.Path += "/v1/compress"
		}
		parsed.Fragment = ""
		return parsed.String()
	}
	base = strings.SplitN(base, "#", 2)[0]
	parts := strings.SplitN(base, "?", 2)
	path := strings.TrimRight(parts[0], "/")
	if !strings.HasSuffix(path, "/v1/compress") {
		path += "/v1/compress"
	}
	if len(parts) == 2 {
		return path + "?" + parts[1]
	}
	return path
}

func cloneObject(src map[string]any) map[string]any {
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
