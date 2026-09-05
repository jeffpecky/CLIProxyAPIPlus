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
	apiKeyFetchTimeout   = 15 * time.Second
)

// apiKeyProviderModelsStore stores dynamically fetched models for API-key providers
type apiKeyProviderModelsStore struct {
	mu            sync.RWMutex
	models        map[string][]*ModelInfo // keyed by provider ("nvidia", "cloudflare", "openrouter")
	enabled       bool
	postFetchHook func()
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

// FetchAPIKeyProviderModels fetches models for all API-key providers using the given API keys
func FetchAPIKeyProviderModels(ctx context.Context, nvidiaKeys, cloudflareKeys, openrouterKeys []APIKeyEntry) error {
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

// StartAPIKeyModelsUpdater starts a background updater for API-key provider models
func StartAPIKeyModelsUpdater(ctx context.Context, nvidiaKeys, cloudflareKeys, openrouterKeys []APIKeyEntry) {
	go func() {
		// Initial fetch
		if err := FetchAPIKeyProviderModels(ctx, nvidiaKeys, cloudflareKeys, openrouterKeys); err != nil {
			log.Warnf("failed to fetch API-key provider models on startup: %v", err)
		}

		// Periodic refresh every 3 hours
		ticker := time.NewTicker(3 * time.Hour)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := FetchAPIKeyProviderModels(ctx, nvidiaKeys, cloudflareKeys, openrouterKeys); err != nil {
					log.Warnf("failed to refresh API-key provider models: %v", err)
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
