package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	kiloFreeModelsURLDefault = "https://api.kilo.ai/api/gateway/models"
	kiloFreeFetchTimeout     = 15 * time.Second
	kiloFreeRefreshInterval  = 3 * time.Hour
	KiloFreeModelPrefix      = "kilo-free/"
)

var kiloFreeModelsURL = kiloFreeModelsURLDefault

type kiloFreeModelsResponse struct {
	Data []struct {
		ID                  string   `json:"id"`
		Name                string   `json:"name"`
		IsFree              bool     `json:"isFree"`
		ContextLength       int      `json:"context_length"`
		SupportedParameters []string `json:"supported_parameters"`
		Architecture        struct {
			InputModalities []string `json:"input_modalities"`
		} `json:"architecture"`
		TopProvider struct {
			ContextLength       int `json:"context_length"`
			MaxCompletionTokens int `json:"max_completion_tokens"`
		} `json:"top_provider"`
	} `json:"data"`
}

type kiloFreeModelsStore struct {
	mu            sync.RWMutex
	models        []*ModelInfo
	enabled       bool
	loaded        bool
	postFetchHook func()
}

var kiloFreeStore = &kiloFreeModelsStore{}

// SetKiloFreeModelsEnabled enables or disables the public Kilo free-pool catalog.
func SetKiloFreeModelsEnabled(enabled bool) {
	kiloFreeStore.mu.Lock()
	defer kiloFreeStore.mu.Unlock()
	kiloFreeStore.enabled = enabled
}

// SetKiloFreePostFetchHook sets the callback invoked after a successful refresh.
func SetKiloFreePostFetchHook(hook func()) {
	kiloFreeStore.mu.Lock()
	defer kiloFreeStore.mu.Unlock()
	kiloFreeStore.postFetchHook = hook
}

// GetKiloFreeModelsFromRemote returns the last successfully fetched free catalog.
func GetKiloFreeModelsFromRemote() []*ModelInfo {
	kiloFreeStore.mu.RLock()
	defer kiloFreeStore.mu.RUnlock()
	if !kiloFreeStore.enabled || !kiloFreeStore.loaded {
		return nil
	}
	models := make([]*ModelInfo, len(kiloFreeStore.models))
	for i, model := range kiloFreeStore.models {
		copy := *model
		models[i] = &copy
	}
	return models
}

// FetchKiloFreeModels fetches and stores the gateway's explicitly free model rows.
func FetchKiloFreeModels(ctx context.Context) error {
	kiloFreeStore.mu.RLock()
	enabled := kiloFreeStore.enabled
	kiloFreeStore.mu.RUnlock()
	if !enabled {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	reqCtx, cancel := context.WithTimeout(ctx, kiloFreeFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, kiloFreeModelsURL, nil)
	if err != nil {
		return fmt.Errorf("kilo free: create model-list request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "cli-proxy-kilo-free")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("kilo free: fetch model list: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("kilo free: model list returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("kilo free: read model list: %w", err)
	}
	var payload kiloFreeModelsResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return fmt.Errorf("kilo free: parse model list: %w", err)
	}
	if payload.Data == nil {
		return fmt.Errorf("kilo free: model list has no data array")
	}

	now := time.Now().Unix()
	models := make([]*ModelInfo, 0, len(payload.Data))
	seen := make(map[string]struct{}, len(payload.Data))
	for _, row := range payload.Data {
		if !row.IsFree || row.ID == "" {
			continue
		}
		if _, exists := seen[row.ID]; exists {
			continue
		}
		seen[row.ID] = struct{}{}
		displayName := row.Name
		if displayName == "" {
			displayName = row.ID
		}
		contextLength := row.ContextLength
		if contextLength <= 0 {
			contextLength = row.TopProvider.ContextLength
		}
		if contextLength <= 0 {
			contextLength = 131072
		}
		maxCompletionTokens := row.TopProvider.MaxCompletionTokens
		if maxCompletionTokens <= 0 {
			maxCompletionTokens = 32768
		}
		model := &ModelInfo{
			ID:                  KiloFreeModelPrefix + row.ID,
			Object:              "model",
			Created:             now,
			OwnedBy:             "kilo-free",
			Type:                "kilo-free",
			DisplayName:         "Kilo Free: " + displayName,
			ContextLength:       contextLength,
			MaxCompletionTokens: maxCompletionTokens,
			SupportedEndpoints:  []string{"/chat/completions"},
		}
		if len(row.Architecture.InputModalities) > 0 {
			model.SupportedInputModalities = append([]string(nil), row.Architecture.InputModalities...)
		}
		if len(row.SupportedParameters) > 0 {
			model.SupportedParameters = append([]string(nil), row.SupportedParameters...)
		}
		models = append(models, model)
	}

	kiloFreeStore.mu.Lock()
	kiloFreeStore.models = models
	kiloFreeStore.loaded = true
	hook := kiloFreeStore.postFetchHook
	kiloFreeStore.mu.Unlock()
	log.Infof("fetched %d free Kilo models from remote", len(models))
	if hook != nil {
		go hook()
	}
	return nil
}

// StartKiloFreeModelsUpdater refreshes the public Kilo free-pool catalog.
func StartKiloFreeModelsUpdater(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		if err := FetchKiloFreeModels(ctx); err != nil {
			log.Warnf("failed to fetch free Kilo models on startup: %v", err)
		}
		ticker := time.NewTicker(kiloFreeRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := FetchKiloFreeModels(ctx); err != nil {
					log.Warnf("failed to refresh free Kilo models: %v", err)
				}
			}
		}
	}()
}
