package executor

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	codexopenai "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/codex/openai/chat-completions"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	openCodeGoDefaultBaseURL      = "https://opencode.ai/zen/go/v1"
	openCodeGoDefaultResponsesURL = "https://opencode.ai/zen/go/v1/responses"
)

// openCodeGoResponsesOnlyModels are models served only by /responses on OpenCode Go.
var openCodeGoResponsesOnlyModels = map[string]bool{
	"grok-4.6":                   true,
	"gpt-5.6-luna":               true,
	"muse-spark-1.2-contributor": true,
	"muse-spark-1.3-contributor": true,
}

// OpenCodeGoExecutor executes against OpenCode Go with a real subscription API key.
// It reuses OpenCode session/header identity but does not force free-tier decoy tools.
type OpenCodeGoExecutor struct {
	*OpenCodeExecutor
}

// NewOpenCodeGoExecutor creates an OpenCode Go executor.
func NewOpenCodeGoExecutor(cfg *config.Config) *OpenCodeGoExecutor {
	return &OpenCodeGoExecutor{
		OpenCodeExecutor: &OpenCodeExecutor{
			OpenAICompatExecutor: NewOpenAICompatExecutor("opencode-go", cfg),
			cfg:                  cfg,
		},
	}
}

// Identifier returns the executor provider identifier.
func (e *OpenCodeGoExecutor) Identifier() string { return "opencode-go" }

func openCodeGoBaseURL(auth *cliproxyauth.Auth) string {
	if auth != nil && auth.Attributes != nil {
		if base := strings.TrimSpace(auth.Attributes["base_url"]); base != "" {
			return strings.TrimRight(base, "/")
		}
	}
	return openCodeGoDefaultBaseURL
}

func openCodeGoResponsesURL(auth *cliproxyauth.Auth) string {
	base := openCodeGoBaseURL(auth)
	if base == openCodeGoDefaultBaseURL {
		return openCodeGoDefaultResponsesURL
	}
	return base + "/responses"
}

func openCodeGoNeedsResponses(model string) bool {
	baseModel := strings.TrimSpace(model)
	if i := strings.IndexAny(baseModel, "( "); i >= 0 {
		baseModel = strings.TrimSpace(baseModel[:i])
	}
	baseModel = strings.TrimPrefix(baseModel, "opencode-go/")
	return openCodeGoResponsesOnlyModels[baseModel] || openCodeIsMuseSpark(baseModel)
}

func applyOpenCodeGoAuthHeaders(req *http.Request, auth *cliproxyauth.Auth) {
	if req == nil {
		return
	}
	if auth != nil && auth.Attributes != nil {
		if apiKey := strings.TrimSpace(auth.Attributes["api_key"]); apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", openCodeUA)
	}
	if req.Header.Get("x-opencode-project") == "" {
		req.Header.Set("x-opencode-project", "global")
	}
	if req.Header.Get("x-opencode-client") == "" {
		req.Header.Set("x-opencode-client", "cli")
	}
}

// PrepareRequest injects OpenCode Go credentials and identity headers.
func (e *OpenCodeGoExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	_, apiKey := e.resolveCredentials(auth)
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	if auth != nil {
		util.ApplyCustomHeadersFromAttrs(req, auth.Attributes)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", openCodeUA)
	}

	resolvedSession := openCodeResolveSession(req.Header, nil, auth, "cli")
	if req.Header.Get(openCodeSessionHeader) == "" {
		req.Header.Set(openCodeSessionHeader, resolvedSession)
	}
	if req.Header.Get("x-opencode-request") == "" {
		req.Header.Set("x-opencode-request", openCodeDeriveRequestId(resolvedSession, nil))
	}
	if req.Header.Get("x-opencode-project") == "" {
		req.Header.Set("x-opencode-project", "global")
	}
	if req.Header.Get("x-opencode-client") == "" {
		req.Header.Set("x-opencode-client", "cli")
	}
	return nil
}

// HttpRequest prepares and executes the upstream request.
func (e *OpenCodeGoExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
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

func injectOpenCodeGoHeaders(auth *cliproxyauth.Auth, headers http.Header, payload []byte, clientTool string) {
	if auth == nil {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	if auth.Attributes["base_url"] == "" {
		auth.Attributes["base_url"] = openCodeGoDefaultBaseURL
	}

	apiKey := strings.TrimSpace(auth.Attributes["api_key"])
	if apiKey != "" {
		auth.Attributes["header:Authorization"] = "Bearer " + apiKey
	}
	if auth.Attributes["header:User-Agent"] == "" {
		auth.Attributes["header:User-Agent"] = openCodeUA
	}

	if auth.Attributes["header:"+openCodeSessionHeader] == "" {
		resolvedSession := openCodeResolveSession(headers, payload, auth, clientTool)
		auth.Attributes["header:"+openCodeSessionHeader] = resolvedSession
	}
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

// Execute performs a non-streaming chat completion request to OpenCode Go.
func (e *OpenCodeGoExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	clientTool := extractClientTool(req.Metadata)
	injectOpenCodeGoHeaders(auth, opts.Headers, req.Payload, clientTool)

	responseFormat := e.normalizeToOpenAI(ctx, &req, &opts)

	if openCodeGoNeedsResponses(req.Model) {
		return e.executeResponses(ctx, auth, req, opts, responseFormat)
	}
	return e.OpenAICompatExecutor.Execute(ctx, auth, req, opts)
}

// ExecuteStream performs a streaming chat completion request to OpenCode Go.
func (e *OpenCodeGoExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	clientTool := extractClientTool(req.Metadata)
	injectOpenCodeGoHeaders(auth, opts.Headers, req.Payload, clientTool)

	responseFormat := e.normalizeToOpenAI(ctx, &req, &opts)

	if openCodeGoNeedsResponses(req.Model) {
		return e.executeResponsesStream(ctx, auth, req, opts, responseFormat)
	}
	return e.OpenAICompatExecutor.ExecuteStream(ctx, auth, req, opts)
}

// executeResponses posts to the Go /responses endpoint for responses-only models.
func (e *OpenCodeGoExecutor) executeResponses(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, responseFormat sdktranslator.Format) (cliproxyexecutor.Response, error) {
	body := openCodeChatToResponses(req.Payload)
	if body == nil {
		body = req.Payload
	}

	url := openCodeGoResponsesURL(auth)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	e.applyOpenCodeHTTPHeaders(httpReq, auth)
	applyOpenCodeGoAuthHeaders(httpReq, auth)
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("opencode-go executor: close response body error: %v", errClose)
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
			return cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}, nil
		}
	}
	return cliproxyexecutor.Response{}, statusErr{code: http.StatusBadGateway, msg: "upstream responses stream completed without result"}
}

// executeResponsesStream streams from the Go /responses endpoint.
func (e *OpenCodeGoExecutor) executeResponsesStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, responseFormat sdktranslator.Format) (*cliproxyexecutor.StreamResult, error) {
	body := openCodeChatToResponses(req.Payload)
	if body == nil {
		body = req.Payload
	}

	url := openCodeGoResponsesURL(auth)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	e.applyOpenCodeHTTPHeaders(httpReq, auth)
	applyOpenCodeGoAuthHeaders(httpReq, auth)
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
				log.Errorf("opencode-go executor: close stream body error: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800)
		var convParam any
		var claudeParam any
		claudeInputTokens := helps.NewClaudeInputTokenState(to, responseFormat, responseFormat, opts.OriginalRequest)
		var upstreamFormat string
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
					case out <- cliproxyexecutor.StreamChunk{Payload: []byte("data: [DONE]\n\n")}:
					case <-ctx.Done():
						return
					}
				}
				continue
			}
			if upstreamFormat == "" {
				if gjson.GetBytes(payload, "object").String() == "chat.completion.chunk" {
					upstreamFormat = "chat"
				} else {
					upstreamFormat = "responses"
				}
			}
			if upstreamFormat == "chat" {
				if responseFormat == to || responseFormat.String() == "" {
					sse := append(append([]byte("data: "), payload...), '\n', '\n')
					select {
					case out <- cliproxyexecutor.StreamChunk{Payload: sse}:
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
				chunks := codexopenai.ConvertCodexResponseToOpenAI(ctx, req.Model, opts.OriginalRequest, req.Payload, trimmed, &convParam)
				for _, chunk := range chunks {
					if responseFormat == to || responseFormat.String() == "" {
						sse := append(append([]byte("data: "), chunk...), '\n', '\n')
						select {
						case out <- cliproxyexecutor.StreamChunk{Payload: sse}:
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
		Chunks:  out,
		Headers: httpResp.Header.Clone(),
	}, nil
}
