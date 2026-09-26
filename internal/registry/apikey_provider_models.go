package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	nvidiaModelsURL      = "https://integrate.api.nvidia.com/v1/models"
	openrouterModelsURL  = "https://openrouter.ai/api/v1/models"
	cloudflareModelsBase = "https://api.cloudflare.com/client/v4"
	openCodeGoModelsURL  = "https://opencode.ai/zen/go/v1/models"
	apiKeyFetchTimeout   = 15 * time.Second
)

// apiKeyProviderModelsKeys is the last-applied set of API-key provider credentials.
type apiKeyProviderModelsKeys struct {
	nvidia     []APIKeyEntry
	cloudflare []APIKeyEntry
	openrouter []APIKeyEntry
	openCodeGo []APIKeyEntry
}

func (k apiKeyProviderModelsKeys) equal(other apiKeyProviderModelsKeys) bool {
	return apiKeyEntriesEqual(k.nvidia, other.nvidia) &&
		apiKeyEntriesEqual(k.cloudflare, other.cloudflare) &&
		apiKeyEntriesEqual(k.openrouter, other.openrouter) &&
		apiKeyEntriesEqual(k.openCodeGo, other.openCodeGo)
}

func (k apiKeyProviderModelsKeys) hasAny() bool {
	return len(k.nvidia) > 0 || len(k.cloudflare) > 0 || len(k.openrouter) > 0 || len(k.openCodeGo) > 0
}

func apiKeyEntriesEqual(a, b []APIKeyEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// apiKeyProviderModelsStore stores dynamically fetched models for API-key providers
type apiKeyProviderModelsStore struct {
	mu              sync.RWMutex
	models          map[string][]*ModelInfo // keyed by provider ("nvidia", "cloudflare", "openrouter")
	enabled         bool
	postFetchHook   func()
	keys            apiKeyProviderModelsKeys
	keysInitialized bool
	fetchMu         sync.Mutex
}

var apiKeyStore = &apiKeyProviderModelsStore{
	models: make(map[string][]*ModelInfo),
}

// SetAPIKeyModelsEnabled enables or disables API-key provider model fetching
func SetAPIKeyModelsEnabled(enabled bool) {
	apiKeyStore.mu.Lock()
	defer apiKeyStore.mu.Unlock()
	apiKeyStore.enabled = enabled
}

// SetAPIKeyModelsPostFetchHook sets a callback invoked after models are fetched successfully
func SetAPIKeyModelsPostFetchHook(hook func()) {
	apiKeyStore.mu.Lock()
	defer apiKeyStore.mu.Unlock()
	apiKeyStore.postFetchHook = hook
}

// GetAPIKeyProviderModels returns the dynamically fetched models for a provider
func GetAPIKeyProviderModels(provider string) []*ModelInfo {
	apiKeyStore.mu.RLock()
	defer apiKeyStore.mu.RUnlock()
	if !apiKeyStore.enabled {
		return nil
	}
	models := apiKeyStore.models[provider]
	result := make([]*ModelInfo, len(models))
	copy(result, models)
	return result
}

// openAIModelsResponse represents the OpenAI-compatible /v1/models response
type openAIModelsResponse struct {
	Object string `json:"object"`
	Data   []struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	} `json:"data"`
}

func fetchModelsFromEndpoint(ctx context.Context, url, apiKey, provider string) ([]*ModelInfo, error) {
	client := &http.Client{Timeout: apiKeyFetchTimeout}
	reqCtx, cancel := context.WithTimeout(ctx, apiKeyFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch models: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var apiResp openAIModelsResponse
	if err := json.Unmarshal(data, &apiResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	models := make([]*ModelInfo, 0, len(apiResp.Data))
	for _, item := range apiResp.Data {
		model := &ModelInfo{
			ID:      item.ID,
			Object:  item.Object,
			Created: item.Created,
			OwnedBy: provider,
			Type:    "openai",
		}
		models = append(models, model)
	}

	return models, nil
}

// ApplyAPIKeyProviderModelKeys stores the given API-key provider credentials and
// fetches remote model catalogs when the credentials changed. Unchanged keys are a no-op.
func ApplyAPIKeyProviderModelKeys(ctx context.Context, nvidiaKeys, cloudflareKeys, openrouterKeys, openCodeGoKeys []APIKeyEntry) error {
	next := apiKeyProviderModelsKeys{
		nvidia:     nvidiaKeys,
		cloudflare: cloudflareKeys,
		openrouter: openrouterKeys,
		openCodeGo: openCodeGoKeys,
	}
	apiKeyStore.mu.Lock()
	if !apiKeyStore.enabled {
		apiKeyStore.mu.Unlock()
		return nil
	}
	if apiKeyStore.keysInitialized && apiKeyStore.keys.equal(next) {
		apiKeyStore.mu.Unlock()
		return nil
	}
	apiKeyStore.keys = next
	apiKeyStore.keysInitialized = true
	apiKeyStore.mu.Unlock()
	return FetchAPIKeyProviderModels(ctx, nvidiaKeys, cloudflareKeys, openrouterKeys, openCodeGoKeys)
}

// currentAPIKeyProviderModelKeys returns a copy of the last-applied API-key provider credentials.
func currentAPIKeyProviderModelKeys() apiKeyProviderModelsKeys {
	apiKeyStore.mu.RLock()
	defer apiKeyStore.mu.RUnlock()
	return apiKeyStore.keys
}

// FetchAPIKeyProviderModels fetches models for all API-key providers using the given API keys
func FetchAPIKeyProviderModels(ctx context.Context, nvidiaKeys, cloudflareKeys, openrouterKeys, openCodeGoKeys []APIKeyEntry) error {
	apiKeyStore.fetchMu.Lock()
	defer apiKeyStore.fetchMu.Unlock()
	if !apiKeyStore.enabled {
		return nil
	}

	allModels := make(map[string][]*ModelInfo)

	// Fetch NVIDIA models (use first valid key)
	for _, key := range nvidiaKeys {
		if key.APIKey == "" {
			continue
		}
		models, err := fetchModelsFromEndpoint(ctx, nvidiaModelsURL, key.APIKey, "nvidia")
		if err != nil {
			log.Warnf("failed to fetch NVIDIA models: %v", err)
			continue
		}
		allModels["nvidia"] = models
		log.Infof("fetched %d NVIDIA models from remote", len(models))
		break
	}

	// Fetch OpenRouter models (use first valid key)
	for _, key := range openrouterKeys {
		if key.APIKey == "" {
			continue
		}
		models, err := fetchModelsFromEndpoint(ctx, openrouterModelsURL, key.APIKey, "openrouter")
		if err != nil {
			log.Warnf("failed to fetch OpenRouter models: %v", err)
			continue
		}
		allModels["openrouter"] = models
		log.Infof("fetched %d OpenRouter models from remote", len(models))
		break
	}

	// Fetch Cloudflare models (needs account-id in URL)
	for _, key := range cloudflareKeys {
		if key.APIKey == "" || key.AccountID == "" {
			continue
		}
		url := fmt.Sprintf("%s/accounts/%s/ai/models/search", cloudflareModelsBase, key.AccountID)
		models, err := fetchModelsFromEndpoint(ctx, url, key.APIKey, "cloudflare")
		if err != nil {
			log.Warnf("failed to fetch Cloudflare models: %v", err)
			continue
		}
		allModels["cloudflare"] = models
		log.Infof("fetched %d Cloudflare models from remote", len(models))
		break
	}

	// Fetch OpenCode Go models (use first valid key)
	for _, key := range openCodeGoKeys {
		if key.APIKey == "" {
			continue
		}
		models, err := fetchModelsFromEndpoint(ctx, openCodeGoModelsURL, key.APIKey, "opencode-go")
		if err != nil {
			log.Warnf("failed to fetch OpenCode Go models: %v", err)
			continue
		}
		allModels["opencode-go"] = models
		log.Infof("fetched %d OpenCode Go models from remote", len(models))
		break
	}

	// Ensure opencode-go always has a usable model list when remote fetch fails.
	if len(allModels["opencode-go"]) == 0 && len(openCodeGoKeys) > 0 {
		allModels["opencode-go"] = openCodeGoFallbackModels()
		log.Infof("using %d static OpenCode Go fallback models", len(allModels["opencode-go"]))
	}

	apiKeyStore.mu.Lock()
	apiKeyStore.models = allModels
	hook := apiKeyStore.postFetchHook
	apiKeyStore.mu.Unlock()

	if hook != nil {
		go hook()
	}

	return nil
}

// APIKeyEntry holds the API key and optional account ID for model fetching
type APIKeyEntry struct {
	APIKey    string
	AccountID string
}

// openCodeGoFallbackModels is a static catalog used when /zen/go/v1/models is unreachable.
func openCodeGoFallbackModels() []*ModelInfo {
	ids := []string{
		"deepseek-flash",
		"glm-5.3-flash", "glm-5.3", "glm-5.2", "glm-5.1",
		"kimi-k2.7-code", "kimi-k2.6", "kimi-k3",
		"deepseek-v4-pro", "deepseek-v4-flash", "deepseek-v4-flash-vision-exp",
		"longcat-2.0", "mimo-v2.5", "mimo-v2.5-pro",
		"minimax-m3", "minimax-m2.7", "minimax-m2.5",
		"qwen3.8-max", "qwen3.8-flash", "qwen3.7-max", "qwen3.7-plus", "qwen3.6-plus",
		"hy4-preview", "hy3",
		"grok-4.6", "gpt-5.6-luna",
		"muse-spark-1.2-contributor", "muse-spark-1.3-contributor",
	}
	models := make([]*ModelInfo, 0, len(ids))
	for _, id := range ids {
		models = append(models, &ModelInfo{
			ID:      id,
			Object:  "model",
			OwnedBy: "opencode-go",
			Type:    "openai",
		})
	}
	return models
}

// StartAPIKeyModelsUpdater starts a background updater for API-key provider models.
// The ticker re-reads credentials stored by ApplyAPIKeyProviderModelKeys so keys
// added or removed after boot are picked up without a process restart.
func StartAPIKeyModelsUpdater(ctx context.Context, nvidiaKeys, cloudflareKeys, openrouterKeys, openCodeGoKeys []APIKeyEntry) {
	go func() {
		if errApply := ApplyAPIKeyProviderModelKeys(ctx, nvidiaKeys, cloudflareKeys, openrouterKeys, openCodeGoKeys); errApply != nil {
			log.Warnf("failed to fetch API-key provider models on startup: %v", errApply)
		}

		ticker := time.NewTicker(3 * time.Hour)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				keys := currentAPIKeyProviderModelKeys()
				if !keys.hasAny() {
					continue
				}
				if errFetch := FetchAPIKeyProviderModels(ctx, keys.nvidia, keys.cloudflare, keys.openrouter, keys.openCodeGo); errFetch != nil {
					log.Warnf("failed to refresh API-key provider models: %v", errFetch)
				}
			}
		}
	}()
}

// ParseAccountID extracts account-id from a Cloudflare API key entry's attributes
func ParseAccountID(attributes map[string]string) string {
	if attributes == nil {
		return ""
	}
	return strings.TrimSpace(attributes["account_id"])
}
