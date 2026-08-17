package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestActiveChatExecutorsDoNotSilentlyBypassFinalHook(t *testing.T) {
	_, current, _, _ := runtime.Caller(0)
	dir := filepath.Dir(current)
	type coverage struct {
		marker  string
		minimum int
	}
	expected := map[string]coverage{
		"gemini_cli_executor.go":          {"applyFinalHookBody", 2},
		"gemini_vertex_executor.go":       {"applyFinalHookBody", 4},
		"aistudio_executor.go":            {"applyFinalHookBody", 2},
		"antigravity_executor_execute.go": {"applyFinalHookBody", 2},
		"antigravity_executor_stream.go":  {"applyFinalHookBody", 1},
		"kimi_executor.go":                {"applyFinalHookBody", 2},
		"kilo_executor.go":                {"applyFinalHookBody", 2},
		"xai_executor_execute.go":         {"applyFinalHookBody", 1},
		"xai_executor_stream.go":          {"applyFinalHookBody", 1},
		"xai_websockets_executor.go":      {"opts.FinalProviderRequestHook == nil", 2},
		"iflow_executor.go":               {"applyFinalHookBody", 2},
		"github_copilot_executor.go":      {"applyFinalHookBody", 2},
		"gitlab_executor.go":              {"finalProviderHookUnsupported", 2},
		"codebuddy_executor.go":           {"applyFinalHookBody", 2},
		"qoder_executor.go":               {"finalProviderHookUnsupported", 2},
		"cursor_executor.go":              {"finalProviderHookUnsupported", 2},
		"codex_websockets_execute.go":     {"FinalProviderRequestHook", 1},
		"codex_websockets_stream.go":      {"FinalProviderRequestHook", 1},
	}
	for file, want := range expected {
		raw, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Count(string(raw), want.marker); got < want.minimum {
			t.Errorf("%s silently bypasses final hook; %s count=%d want>=%d", file, want.marker, got, want.minimum)
		}
	}
}

func TestUnsupportedFinalHookErrorIsRequestScoped(t *testing.T) {
	err := finalProviderHookUnsupported(cliproxyexecutor.Options{FinalProviderRequestHook: func(context.Context, cliproxyexecutor.FinalProviderRequest) (cliproxyexecutor.FinalProviderRequestResult, error) {
		return cliproxyexecutor.FinalProviderRequestResult{}, nil
	}}, "cursor")
	var scoped cliproxyexecutor.RequestScopedError
	if !errors.As(err, &scoped) || !scoped.IsRequestScoped() {
		t.Fatalf("error is not request scoped: %T %v", err, err)
	}
}

func TestCountTokenImplementationsDoNotSilentlyBypassFinalHook(t *testing.T) {
	_, current, _, _ := runtime.Caller(0)
	dir := filepath.Dir(current)
	expected := map[string]string{
		"codex_executor_tokens.go":       "applyFinalHookBytes",
		"gemini_cli_executor.go":         "applyFinalHookBody",
		"gemini_vertex_executor.go":      "applyFinalHookBody",
		"aistudio_executor.go":           "applyFinalHookBody",
		"antigravity_executor_tokens.go": "applyFinalHookBody",
		"xai_executor_tokens.go":         "applyFinalHookBytes",
		"kiro_executor.go":               "applyFinalHookBytes",
		"github_copilot_executor.go":     "applyFinalHookBytes",
		"kimi_executor.go":               "countTokensUpstream",
		"kilo_executor.go":               "count tokens not supported",
		"iflow_executor.go":              "applyFinalHookBytes",
		"codebuddy_executor.go":          "count tokens not supported",
		"cursor_executor.go":             "finalProviderHookUnsupported",
		"gitlab_executor.go":             "finalProviderHookUnsupported",
		"qoder_executor.go":              "finalProviderHookUnsupported",
	}
	for file, marker := range expected {
		raw, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), marker) {
			t.Errorf("%s CountTokens bypasses final hook; missing %s", file, marker)
		}
	}
}

func TestCodexAutoUsesHTTPWhenFinalHookConfigured(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(mustCallerFile()), "codex_websockets_executor.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "opts.FinalProviderRequestHook == nil") {
		t.Fatal("Codex auto transport does not force HTTP when final hook is configured")
	}
}

func mustCallerFile() string { _, file, _, _ := runtime.Caller(0); return file }
