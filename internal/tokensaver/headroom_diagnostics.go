package tokensaver

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
)

var sensitiveURLText = regexp.MustCompile(`(?i)(https?://)([^/@\s]+@)?([^\s?#]+)(?:[?#][^\s)]*)?`)

func captureSizeSnapshot(root map[string]any) SizeSnapshot {
	messages, _, _ := firstArray(root, "messages", "input")
	toolHistory := make([]any, 0)
	for _, item := range messages {
		message, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if message["role"] == "tool" || message["role"] == "function" || message["tool_calls"] != nil || containsToolBlock(message["content"]) {
			toolHistory = append(toolHistory, message)
		}
	}
	return SizeSnapshot{
		BodyBytes:        jsonSize(root),
		MessageBytes:     jsonSize(messages),
		ToolSchemaBytes:  jsonSize(root["tools"]),
		ToolHistoryBytes: jsonSize(toolHistory),
	}
}

func containsToolBlock(content any) bool {
	parts, ok := content.([]any)
	if !ok {
		return false
	}
	for _, part := range parts {
		block, ok := part.(map[string]any)
		if ok && (block["type"] == "tool_use" || block["type"] == "tool_result") {
			return true
		}
	}
	return false
}

func jsonSize(value any) int {
	raw, err := json.Marshal(value)
	if err != nil {
		return 0
	}
	return len(raw)
}

func maskEndpoint(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return scrubSensitiveURLText(endpoint)
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "?")
}

func scrubSensitiveURLText(text string) string {
	return sensitiveURLText.ReplaceAllString(text, "$1$3")
}

func parseHeadroomStats(root map[string]any) HeadroomStats {
	stats := HeadroomStats{
		TokensBefore: intNumber(root["tokens_before"]),
		TokensAfter:  intNumber(root["tokens_after"]),
		TokensSaved:  intNumber(root["tokens_saved"]),
		Ratio:        floatNumber(root["compression_ratio"]),
	}
	if stats.Ratio == 0 {
		stats.Ratio = floatNumber(root["ratio"])
	}
	stats.CompressionSkipped, _ = root["compression_skipped"].(bool)
	stats.SkipReason, _ = root["skip_reason"].(string)
	for _, key := range []string{"transforms", "transforms_applied"} {
		if values, ok := root[key].([]any); ok {
			for _, value := range values {
				if text, ok := value.(string); ok {
					stats.Transforms = append(stats.Transforms, text)
				}
			}
			break
		}
	}
	if summary, ok := root["transforms_summary"].(map[string]any); ok {
		stats.TransformSummary = make(map[string]int, len(summary))
		for name, count := range summary {
			stats.TransformSummary[name] = intNumber(count)
		}
	}
	if hashes, ok := root["ccr_hashes"].([]any); ok {
		for _, hash := range hashes {
			if text, ok := hash.(string); ok {
				stats.CCRHashes = append(stats.CCRHashes, text)
			}
		}
	}
	return stats
}

func floatNumber(value any) float64 {
	switch v := value.(type) {
	case json.Number:
		n, _ := v.Float64()
		return n
	case float64:
		return v
	default:
		return 0
	}
}

func intNumber(value any) int {
	switch v := value.(type) {
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	case float64:
		return int(v)
	case int:
		return v
	default:
		return 0
	}
}

func isPhantomHeadroomSavings(stats HeadroomStats, before, after SizeSnapshot) bool {
	return stats.TokensSaved > 0 && before.BodyBytes > 0 && after.BodyBytes > 0 && float64(after.BodyBytes) >= float64(before.BodyBytes)*0.95
}
