package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func resetKiloFreeModelsStore() {
	kiloFreeStore.mu.Lock()
	kiloFreeStore.models = nil
	kiloFreeStore.enabled = false
	kiloFreeStore.loaded = false
	kiloFreeStore.postFetchHook = nil
	kiloFreeStore.mu.Unlock()
}

func TestFetchKiloFreeModelsFiltersFreeRowsWithoutCredentials(t *testing.T) {
	resetKiloFreeModelsStore()
	t.Cleanup(resetKiloFreeModelsStore)
	var gotAuthorization string
	var gotUserAgent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/models" {
			t.Errorf("request = %s %s, want GET /models", r.Method, r.URL.Path)
		}
		gotAuthorization = r.Header.Get("Authorization")
		gotUserAgent = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"id":"vendor/free-one","name":"Free One","isFree":true,"context_length":32768,"supported_parameters":["reasoning"],"architecture":{"input_modalities":["text","image"]},"top_provider":{"max_completion_tokens":8192}},
			{"id":"vendor/paid","name":"Paid","isFree":false},
			{"id":"vendor/free-one","name":"Duplicate","isFree":true}
		]}`))
	}))
	defer server.Close()

	oldURL := kiloFreeModelsURL
	kiloFreeModelsURL = server.URL + "/models"
	defer func() { kiloFreeModelsURL = oldURL }()
	SetKiloFreeModelsEnabled(true)
	if err := FetchKiloFreeModels(context.Background()); err != nil {
		t.Fatalf("FetchKiloFreeModels error: %v", err)
	}
	if gotAuthorization != "" {
		t.Errorf("model listing sent Authorization %q, want none", gotAuthorization)
	}
	if gotUserAgent != "cli-proxy-kilo-free" {
		t.Errorf("model-list User-Agent = %q, want cli-proxy-kilo-free", gotUserAgent)
	}
	models := GetKiloFreeModelsFromRemote()
	if len(models) != 1 {
		t.Fatalf("models = %d, want 1 free model", len(models))
	}
	model := models[0]
	if model.ID != KiloFreeModelPrefix+"vendor/free-one" || model.Type != "kilo-free" || model.OwnedBy != "kilo-free" {
		t.Errorf("unexpected model identity: %+v", model)
	}
	if model.ContextLength != 32768 || model.MaxCompletionTokens != 8192 || model.DisplayName != "Kilo Free: Free One" {
		t.Errorf("model metadata not preserved: %+v", model)
	}
	if len(model.SupportedInputModalities) != 2 || model.SupportedInputModalities[1] != "image" {
		t.Errorf("input modalities = %v, want text and image", model.SupportedInputModalities)
	}
}

func TestFetchKiloFreeModelsRejectsBadResponseAndKeepsCachedRows(t *testing.T) {
	resetKiloFreeModelsStore()
	t.Cleanup(resetKiloFreeModelsStore)
	kiloFreeStore.mu.Lock()
	kiloFreeStore.enabled = true
	kiloFreeStore.loaded = true
	kiloFreeStore.models = []*ModelInfo{{ID: "cached", Type: "kilo-free"}}
	kiloFreeStore.mu.Unlock()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	oldURL := kiloFreeModelsURL
	kiloFreeModelsURL = server.URL
	defer func() { kiloFreeModelsURL = oldURL }()

	if err := FetchKiloFreeModels(context.Background()); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("FetchKiloFreeModels error = %v, want HTTP 503 error", err)
	}
	models := GetKiloFreeModelsFromRemote()
	if len(models) != 1 || models[0].ID != "cached" {
		t.Errorf("cached catalog was not retained after failure: %+v", models)
	}
}
