package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	openCodeModelsURL    = "https://opencode.ai/zen/v1/models"
	openCodeFetchTimeout = 15 * time.Second
)

// knownFreeOpenCodeModels contains known free models without the "-free" suffix
var knownFreeOpenCodeModels = map[string]bool{
	"big-pickle": true,
}

// OpenCode free-tier reasoning levels, mirrored from 9router's thinkingLevels.js
// and capabilities.js. Free models are fetched from the remote catalog, which
// carries no capability metadata, so the levels are attached by model family.
// Without this the thinking pipeline sees Thinking == nil and strips the
// client's reasoning_effort entirely (#4761).
var (
	openCodeLevelsOpenAI     = []string{"none", "minimal", "low", "medium", "high", "xhigh"}
	openCodeLevelsFull       = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}
	openCodeLevelsDeepSeekV4 = []string{
		"none", "low", "medium", "high", "xhigh", "max",
	}
	// Generic deepseek ids (no dotted version) use the hiMax set: low/medium
	// collapse to high and xhigh to max on the wire.
	openCodeLevelsDeepSeekBase = []string{"none", "high", "max"}
	// The free v4.1-flash id publishes low|high|max and forwards the level
	// verbatim (no low→high collapse) (#313559f).
	openCodeLevelsDeepSeekV41Free = []string{"low", "high", "max"}
	openCodeLevelsKimi            = []string{"none", "low", "medium", "high", "max"}
	// glm-5.3-flash cannot disable thinking, so its OpenAI enum loses "none".
	openCodeLevelsGLMFlash = []string{"minimal", "low", "medium", "high", "xhigh"}
	// mimo v2.5-pro rejects reasoning_effort "max" (probed live).
	openCodeLevelsMimoV25Pro = []string{"none", "low", "medium", "high", "xhigh"}
	// Generic reasoning ids without a format-specific level table (base set).
	openCodeLevelsBase = []string{"none", "low", "medium", "high"}
)

var openCodeThinkingRules = []struct {
	pattern *regexp.Regexp
	levels  []string
}{
	{regexp.MustCompile(`(?i)mimo.*v2\.6`), openCodeLevelsFull},
	{regexp.MustCompile(`(?i)mimo.*v2\.5-pro`), openCodeLevelsMimoV25Pro},
	{regexp.MustCompile(`(?i)deepseek-v4\.1-flash-free`), openCodeLevelsDeepSeekV41Free},
	{regexp.MustCompile(`(?i)deepseek-v4\.`), openCodeLevelsDeepSeekV4},
	{regexp.MustCompile(`(?i)^deepseek`), openCodeLevelsDeepSeekBase},
	{regexp.MustCompile(`(?i)^glm-5\.3-flash`), openCodeLevelsGLMFlash},
	{regexp.MustCompile(`(?i)^kimi-`), openCodeLevelsKimi},
	// Reasoning-capable free ids whose capabilities declare the generic OpenAI
	// effort enum ("space-bunny & co", #4761).
	{regexp.MustCompile(`(?i)^space-bunny`), openCodeLevelsOpenAI},
	{regexp.MustCompile(`(?i)^big-pickle$`), openCodeLevelsOpenAI},
	{regexp.MustCompile(`(?i)^exo-free$`), openCodeLevelsOpenAI},
	{regexp.MustCompile(`(?i)^fledge-alpha-free$`), openCodeLevelsOpenAI},
	{regexp.MustCompile(`(?i)^longcat-`), openCodeLevelsOpenAI},
	{regexp.MustCompile(`(?i)^north-mini-code-free$`), openCodeLevelsOpenAI},
	{regexp.MustCompile(`(?i)^ring-2\.6-1t-free$`), openCodeLevelsOpenAI},
	{regexp.MustCompile(`(?i)^x-preview-f-free$`), openCodeLevelsOpenAI},
	{regexp.MustCompile(`(?i)^muse-spark`), openCodeLevelsOpenAI},
	{regexp.MustCompile(`(?i)^mimo.*v2\.5-free$`), openCodeLevelsOpenAI},
	{regexp.MustCompile(`(?i)^(?:ling|nemotron)-.*-free$`), openCodeLevelsBase},
}

// openCodeThinkingSupport returns the reasoning levels for a free OpenCode
// model, or nil when the family is not a known reasoning model.
func openCodeThinkingSupport(modelID string) *ThinkingSupport {
	for _, rule := range openCodeThinkingRules {
		if rule.pattern.MatchString(modelID) {
			return &ThinkingSupport{Levels: append([]string(nil), rule.levels...)}
		}
	}
	return nil
}

// openCodeModelsResponse represents the API response from opencode.ai
type openCodeModelsResponse struct {
	Object string `json:"object"`
	Data   []struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	} `json:"data"`
}

// openCodeModelsStore stores the dynamically fetched OpenCode models
type openCodeModelsStore struct {
	mu            sync.RWMutex
	models        []*ModelInfo
	enabled       bool
	postFetchHook func()
}

var openCodeStore = &openCodeModelsStore{}

// SetOpenCodeModelsEnabled enables or disables OpenCode model fetching
func SetOpenCodeModelsEnabled(enabled bool) {
	openCodeStore.mu.Lock()
	defer openCodeStore.mu.Unlock()
	openCodeStore.enabled = enabled
}

// SetOpenCodePostFetchHook sets a callback invoked after models are fetched successfully.
func SetOpenCodePostFetchHook(hook func()) {
	openCodeStore.mu.Lock()
	defer openCodeStore.mu.Unlock()
	openCodeStore.postFetchHook = hook
}

// GetOpenCodeModelsFromRemote returns the dynamically fetched OpenCode models
func GetOpenCodeModelsFromRemote() []*ModelInfo {
	openCodeStore.mu.RLock()
	defer openCodeStore.mu.RUnlock()
	if !openCodeStore.enabled {
		return nil
	}
	result := make([]*ModelInfo, len(openCodeStore.models))
	copy(result, openCodeStore.models)
	return result
}

// isOpenCodeFreeModel checks if an OpenCode model is free
func isOpenCodeFreeModel(modelID string) bool {
	if strings.HasSuffix(modelID, "-free") {
		return true
	}
	return knownFreeOpenCodeModels[modelID]
}

// FetchOpenCodeModels fetches models from opencode.ai and stores them
func FetchOpenCodeModels(ctx context.Context) error {
	openCodeStore.mu.RLock()
	enabled := openCodeStore.enabled
	openCodeStore.mu.RUnlock()

	if !enabled {
		return nil
	}

	client := &http.Client{Timeout: openCodeFetchTimeout}
	reqCtx, cancel := context.WithTimeout(ctx, openCodeFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, "GET", openCodeModelsURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to fetch models: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	var apiResp openCodeModelsResponse
	if err := json.Unmarshal(data, &apiResp); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	// Filter to only free models (matching 9Router's "opencode-free" filter)
	models := make([]*ModelInfo, 0, len(apiResp.Data))
	for _, item := range apiResp.Data {
		if !isOpenCodeFreeModel(item.ID) {
			continue
		}
		model := &ModelInfo{
			ID:       item.ID,
			Object:   item.Object,
			Created:  item.Created,
			OwnedBy:  item.OwnedBy,
			Type:     "opencode",
			Thinking: openCodeThinkingSupport(item.ID),
		}
		models = append(models, model)
	}

	openCodeStore.mu.Lock()
	openCodeStore.models = models
	hook := openCodeStore.postFetchHook
	openCodeStore.mu.Unlock()

	log.Infof("fetched %d free OpenCode models from remote", len(models))

	if hook != nil {
		go hook()
	}

	return nil
}

// StartOpenCodeModelsUpdater starts a background updater for OpenCode models
func StartOpenCodeModelsUpdater(ctx context.Context) {
	go func() {
		// Initial fetch
		if err := FetchOpenCodeModels(ctx); err != nil {
			log.Warnf("failed to fetch OpenCode models on startup: %v", err)
		}

		// Periodic refresh every 3 hours
		ticker := time.NewTicker(3 * time.Hour)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := FetchOpenCodeModels(ctx); err != nil {
					log.Warnf("failed to refresh OpenCode models: %v", err)
				}
			}
		}
	}()
}
