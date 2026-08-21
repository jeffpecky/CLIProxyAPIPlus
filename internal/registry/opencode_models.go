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
	openCodeModelsURL    = "https://opencode.ai/zen/v1/models"
	openCodeFetchTimeout = 15 * time.Second
)

// knownFreeOpenCodeModels contains known free models without the "-free" suffix
var knownFreeOpenCodeModels = map[string]bool{
	"big-pickle": true,
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
			ID:      item.ID,
			Object:  item.Object,
			Created: item.Created,
			OwnedBy: item.OwnedBy,
			Type:    "opencode",
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
