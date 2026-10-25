package executor

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// OpenCodeReasoningInjectorConfig holds the configuration for reasoning_content injection.
// Some thinking-mode providers (DeepSeek, Kimi, MiniMax, ...) require reasoning_content
// to be echoed back on assistant messages. Clients in OpenAI format don't send it,
// so we inject a non-empty placeholder to satisfy upstream validation.
type OpenCodeReasoningInjectorConfig struct {
	// Provider-level rules derive from registry transport.reasoningInject
	ProviderRules map[string]string // provider -> scope ("all" or "toolCalls")
	// Model-level rules: matched by regex against model ID
	ModelRules []OpenCodeReasoningModelRule
}

type OpenCodeReasoningModelRule struct {
	Pattern string // regex pattern
	Scope   string // "all" or "toolCalls"
}

// DefaultOpenCodeReasoningInjectorConfig returns the default configuration
// matching 9router's open-sse/utils/reasoningContentInjector.js.
//
// The OpenCode registry entry declares reasoningInject:{scope:"all"} for the
// whole free lane (not only DeepSeek/Kimi), so every OpenCode free model gets
// the placeholder. The model rules remain for direct-provider lanes that share
// this helper (DeepSeek "all", Kimi only on tool-call turns).
func DefaultOpenCodeReasoningInjectorConfig() *OpenCodeReasoningInjectorConfig {
	return &OpenCodeReasoningInjectorConfig{
		ProviderRules: map[string]string{
			"opencode": "all",
		},
		ModelRules: []OpenCodeReasoningModelRule{
			{Pattern: `(?i)^kimi-`, Scope: "toolCalls"},
			{Pattern: `(?i)deepseek`, Scope: "all"},
		},
	}
}

const openCodeReasoningPlaceholder = " "

// shouldInjectReasoningContent determines if we should inject reasoning_content
// for the given message and scope.
func shouldInjectReasoningContent(message map[string]any, scope string) bool {
	role, _ := message["role"].(string)
	if role != "assistant" {
		return false
	}

	rc := message["reasoning_content"]
	if rcStr, ok := rc.(string); ok && len(rcStr) > 0 {
		return false // already has reasoning_content
	}

	switch scope {
	case "toolCalls":
		if calls, ok := message["tool_calls"].([]any); ok {
			return len(calls) > 0
		}
		return false
	case "all":
		return true
	default:
		return true
	}
}

// injectReasoningContentIntoBody injects reasoning_content placeholder into assistant messages
// that need it based on the configured rules.
func injectReasoningContentIntoBody(body []byte, model string, provider string, cfg *OpenCodeReasoningInjectorConfig) []byte {
	if len(body) == 0 || cfg == nil {
		return body
	}

	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return body
	}

	messages, ok := req["messages"].([]any)
	if !ok {
		return body
	}

	// Determine which rule applies
	var scope string
	if providerRule, ok := cfg.ProviderRules[strings.ToLower(provider)]; ok {
		scope = providerRule
	} else {
		for _, rule := range cfg.ModelRules {
			if matched, _ := regexp.MatchString(rule.Pattern, model); matched {
				scope = rule.Scope
				break
			}
		}
	}

	if scope == "" {
		return body // no rule matches
	}

	modified := false
	newMessages := make([]any, len(messages))
	for i, msg := range messages {
		msgMap, ok := msg.(map[string]any)
		if !ok {
			newMessages[i] = msg
			continue
		}

		if shouldInjectReasoningContent(msgMap, scope) {
			newMsg := make(map[string]any, len(msgMap)+1)
			for k, v := range msgMap {
				newMsg[k] = v
			}
			newMsg["reasoning_content"] = openCodeReasoningPlaceholder
			newMessages[i] = newMsg
			modified = true
		} else {
			newMessages[i] = msg
		}
	}

	if !modified {
		return body
	}

	req["messages"] = newMessages
	out, err := json.Marshal(req)
	if err != nil {
		return body
	}
	return out
}

// OpenCodeMaybeInjectReasoningContent injects reasoning_content placeholder for the
// OpenCode free-tier lane when the upstream provider requires it.
// This mirrors 9router's reasoningContentInjector.js behavior.
func OpenCodeMaybeInjectReasoningContent(cfg *config.Config, provider string, model string, body []byte) []byte {
	injectorCfg := DefaultOpenCodeReasoningInjectorConfig()
	return injectReasoningContentIntoBody(body, model, provider, injectorCfg)
}
