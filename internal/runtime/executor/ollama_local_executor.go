package executor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const ollamaLocalDefaultBaseURL = "http://localhost:11434"

// OllamaLocalExecutor translates OpenAI requests to Ollama's native /api/chat
// format and converts Ollama's NDJSON responses back to OpenAI SSE.
type OllamaLocalExecutor struct {
	cfg *config.Config
}

// NewOllamaLocalExecutor creates an executor for self-hosted Ollama servers.
func NewOllamaLocalExecutor(cfg *config.Config) *OllamaLocalExecutor {
	return &OllamaLocalExecutor{cfg: cfg}
}

// Identifier implements cliproxyauth.ProviderExecutor.
func (e *OllamaLocalExecutor) Identifier() string { return "ollama-local" }

func ollamaLocalBaseURL(auth *cliproxyauth.Auth) string {
	if auth != nil && auth.Attributes != nil {
		if base := strings.TrimSpace(auth.Attributes["base_url"]); base != "" {
			return strings.TrimRight(base, "/")
		}
	}
	return ollamaLocalDefaultBaseURL
}

func (e *OllamaLocalExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	var resp cliproxyexecutor.Response

	ollamaBody := convertOpenAIToOllama(req.Model, req.Payload, false)

	baseURL := ollamaLocalBaseURL(auth)
	url := baseURL + "/api/chat"

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(ollamaBody))
	if err != nil {
		return resp, fmt.Errorf("ollama local executor: failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	var authID, authLabel string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      ollamaBody,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, fmt.Errorf("ollama local executor: request failed: %w", err)
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("ollama local executor: close response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		return resp, statusErr{code: httpResp.StatusCode, msg: string(b)}
	}

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, fmt.Errorf("ollama local executor: failed to read response: %w", err)
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, body)

	var contentParts []string
	var ollamaModel string
	var promptTokens, completionTokens int64

	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		chunk := gjson.ParseBytes(line)
		if ollamaModel == "" {
			ollamaModel = chunk.Get("model").String()
		}
		if msgContent := chunk.Get("message.content").String(); msgContent != "" {
			contentParts = append(contentParts, msgContent)
		}
		if chunk.Get("done").Bool() {
			promptTokens = chunk.Get("prompt_eval_count").Int()
			completionTokens = chunk.Get("eval_count").Int()
		}
	}
	if errScan := scanner.Err(); errScan != nil {
		return resp, fmt.Errorf("ollama local executor: scan error: %w", errScan)
	}

	completedContent := strings.Join(contentParts, "")
	totalTokens := promptTokens + completionTokens

	model := req.Model
	if ollamaModel != "" {
		model = ollamaModel
	}

	chatResp := map[string]interface{}{
		"id":      "chatcmpl-ollama-" + ollamaLocalRandomID(12),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []interface{}{
			map[string]interface{}{
				"index":         0,
				"message":       map[string]interface{}{"role": "assistant", "content": completedContent},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]interface{}{
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
			"total_tokens":      totalTokens,
		},
	}

	respPayload, err := json.Marshal(chatResp)
	if err != nil {
		return resp, fmt.Errorf("ollama local executor: failed to marshal response: %w", err)
	}
	resp.Payload = respPayload
	resp.Headers = httpResp.Header.Clone()
	return resp, nil
}

func (e *OllamaLocalExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	ollamaBody := convertOpenAIToOllama(req.Model, req.Payload, true)

	baseURL := ollamaLocalBaseURL(auth)
	url := baseURL + "/api/chat"

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(ollamaBody))
	if err != nil {
		return nil, fmt.Errorf("ollama local executor: failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	var authID, authLabel string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      ollamaBody,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, fmt.Errorf("ollama local executor: request failed: %w", err)
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		httpResp.Body.Close()
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		return nil, statusErr{code: httpResp.StatusCode, msg: string(b)}
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

	out := make(chan cliproxyexecutor.StreamChunk, 32)
	scanner := bufio.NewScanner(httpResp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	go func() {
		defer close(out)
		defer httpResp.Body.Close()

		ollamaNDJSONToOpenAIStream(ctx, scanner, out)

		if errScan := scanner.Err(); errScan != nil {
			log.Errorf("ollama local executor: stream scan error: %v", errScan)
		}
	}()

	return &cliproxyexecutor.StreamResult{
		Chunks:  out,
		Headers: httpResp.Header.Clone(),
	}, nil
}

func (e *OllamaLocalExecutor) Refresh(_ context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	return auth, nil
}

func (e *OllamaLocalExecutor) CountTokens(_ context.Context, _ *cliproxyauth.Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, statusErr{code: http.StatusNotImplemented, msg: "token counting not supported for ollama-local"}
}

func (e *OllamaLocalExecutor) HttpRequest(_ context.Context, _ *cliproxyauth.Auth, _ *http.Request) (*http.Response, error) {
	return nil, statusErr{code: http.StatusNotImplemented, msg: "HTTP proxy not supported for ollama-local"}
}

// FetchOllamaLocalModels retrieves the installed model list from the Ollama
// server's /api/tags endpoint. It returns nil when the server cannot be
// reached or reports an error so callers can fall back to configured models.
func FetchOllamaLocalModels(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config) []*registry.ModelInfo {
	if ctx == nil {
		ctx = context.Background()
	}
	tagsURL := strings.TrimRight(ollamaLocalBaseURL(auth), "/") + "/api/tags"

	// Bound discovery latency so a slow or unreachable server cannot stall
	// model registration (mirrors the other dynamic model fetchers).
	fetchCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, errReq := http.NewRequestWithContext(fetchCtx, http.MethodGet, tagsURL, nil)
	if errReq != nil {
		log.Warnf("ollama local: build model list request: %v", errReq)
		return nil
	}
	req.Header.Set("Accept", "application/json")

	httpClient := helps.NewProxyAwareHTTPClient(fetchCtx, cfg, auth, 0)
	resp, errDo := httpClient.Do(req)
	if errDo != nil {
		log.Warnf("ollama local: model list fetch failed: %v", errDo)
		return nil
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Warnf("ollama local: close model list response: %v", errClose)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		log.Warnf("ollama local: model list returned %d: %s", resp.StatusCode, truncate(string(b), 300))
		return nil
	}
	body, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		log.Warnf("ollama local: read model list response: %v", errRead)
		return nil
	}

	entries := gjson.GetBytes(body, "models")
	if !entries.IsArray() {
		log.Warn("ollama local: model list response missing models array")
		return nil
	}
	now := time.Now().Unix()
	list := entries.Array()
	models := make([]*registry.ModelInfo, 0, len(list))
	seen := make(map[string]struct{}, len(list))
	for _, entry := range list {
		name := strings.TrimSpace(entry.Get("name").String())
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		models = append(models, &registry.ModelInfo{
			ID:          name,
			Object:      "model",
			Created:     now,
			OwnedBy:     "ollama-local",
			Type:        "openai",
			DisplayName: name,
		})
	}
	if len(models) == 0 {
		return nil
	}
	return models
}

func convertOpenAIToOllama(modelName string, rawJSON []byte, stream bool) []byte {
	root := gjson.ParseBytes(rawJSON)
	out := []byte(`{}`)
	out, _ = sjson.SetBytes(out, "model", modelName)
	out, _ = sjson.SetBytes(out, "stream", stream)

	if msgs := root.Get("messages"); msgs.Exists() {
		var ollamaMsgs []map[string]interface{}
		for _, msg := range msgs.Array() {
			m := map[string]interface{}{
				"role": msg.Get("role").String(),
			}
			content := msg.Get("content")
			if content.IsArray() {
				var parts []string
				var images []string
				for _, p := range content.Array() {
					switch p.Get("type").String() {
					case "text":
						parts = append(parts, p.Get("text").String())
					case "image_url":
						url := p.Get("image_url.url").String()
						if strings.HasPrefix(url, "data:") {
							if idx := strings.Index(url, ","); idx >= 0 {
								images = append(images, url[idx+1:])
							}
						}
					}
				}
				m["content"] = strings.Join(parts, "\n")
				if len(images) > 0 {
					m["images"] = images
				}
			} else {
				m["content"] = content.String()
			}
			ollamaMsgs = append(ollamaMsgs, m)
		}
		msgsJSON, _ := json.Marshal(ollamaMsgs)
		out, _ = sjson.SetRawBytes(out, "messages", msgsJSON)
	}

	options := map[string]interface{}{}
	if v := root.Get("max_tokens"); v.Exists() {
		options["num_predict"] = v.Int()
	}
	if v := root.Get("temperature"); v.Exists() {
		options["temperature"] = v.Float()
	}
	if v := root.Get("top_p"); v.Exists() {
		options["top_p"] = v.Float()
	}
	if v := root.Get("stop"); v.Exists() {
		options["stop"] = v.Value()
	}
	if len(options) > 0 {
		optsJSON, _ := json.Marshal(options)
		out, _ = sjson.SetRawBytes(out, "options", optsJSON)
	}

	if tools := root.Get("tools"); tools.Exists() {
		out, _ = sjson.SetRawBytes(out, "tools", []byte(tools.Raw))
	}

	return out
}

func ollamaNDJSONToOpenAIStream(ctx context.Context, scanner *bufio.Scanner, out chan<- cliproxyexecutor.StreamChunk) {
	id := "chatcmpl-ollama-" + ollamaLocalRandomID(12)
	created := time.Now().Unix()

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		chunk := gjson.ParseBytes(line)

		if chunk.Get("done").Bool() {
			usage := map[string]interface{}{
				"prompt_tokens":     chunk.Get("prompt_eval_count").Int(),
				"completion_tokens": chunk.Get("eval_count").Int(),
			}
			pts := usage["prompt_tokens"].(int64)
			cts := usage["completion_tokens"].(int64)
			usage["total_tokens"] = pts + cts

			donePayload := map[string]interface{}{
				"id":      id,
				"object":  "chat.completion.chunk",
				"created": created,
				"model":   chunk.Get("model").String(),
				"choices": []interface{}{},
				"usage":   usage,
			}
			payload, _ := json.Marshal(donePayload)
			// The handler frames each chunk as "data: %s" and appends
			// "data: [DONE]" when the chunk channel closes; emit raw JSON only.
			out <- cliproxyexecutor.StreamChunk{Payload: append(payload, '\n', '\n')}
			return
		}

		msg := chunk.Get("message")
		content := msg.Get("content").String()
		if content == "" {
			continue
		}

		delta := map[string]interface{}{
			"content": content,
		}
		role := msg.Get("role").String()
		if role != "" {
			delta["role"] = role
		}
		chatChunk := map[string]interface{}{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   chunk.Get("model").String(),
			"choices": []interface{}{
				map[string]interface{}{
					"index":         0,
					"delta":         delta,
					"finish_reason": nil,
				},
			},
		}
		payload, _ := json.Marshal(chatChunk)
		out <- cliproxyexecutor.StreamChunk{Payload: append(payload, '\n', '\n')}
	}
}

func ollamaLocalRandomID(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	var out strings.Builder
	const chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	for i := 0; i < n; i++ {
		out.WriteByte(chars[int(b[i])%len(chars)])
	}
	return out.String()
}
