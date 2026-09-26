package registry

import (
	"context"
	"testing"
)

func resetAPIKeyProviderModelsStoreForTest(t *testing.T) {
	t.Helper()
	apiKeyStore.mu.Lock()
	prevEnabled := apiKeyStore.enabled
	prevModels := apiKeyStore.models
	prevKeys := apiKeyStore.keys
	prevInitialized := apiKeyStore.keysInitialized
	prevHook := apiKeyStore.postFetchHook
	apiKeyStore.models = make(map[string][]*ModelInfo)
	apiKeyStore.enabled = false
	apiKeyStore.keys = apiKeyProviderModelsKeys{}
	apiKeyStore.keysInitialized = false
	apiKeyStore.postFetchHook = nil
	apiKeyStore.mu.Unlock()
	t.Cleanup(func() {
		apiKeyStore.mu.Lock()
		apiKeyStore.enabled = prevEnabled
		apiKeyStore.models = prevModels
		apiKeyStore.keys = prevKeys
		apiKeyStore.keysInitialized = prevInitialized
		apiKeyStore.postFetchHook = prevHook
		apiKeyStore.mu.Unlock()
	})
}

func TestAPIKeyProviderModelKeysEqual(t *testing.T) {
	base := apiKeyProviderModelsKeys{
		nvidia:     []APIKeyEntry{{APIKey: "n"}},
		cloudflare: []APIKeyEntry{{APIKey: "c", AccountID: "acct"}},
		openrouter: []APIKeyEntry{{APIKey: "o"}},
		openCodeGo: []APIKeyEntry{{APIKey: "k"}},
	}
	same := apiKeyProviderModelsKeys{
		nvidia:     []APIKeyEntry{{APIKey: "n"}},
		cloudflare: []APIKeyEntry{{APIKey: "c", AccountID: "acct"}},
		openrouter: []APIKeyEntry{{APIKey: "o"}},
		openCodeGo: []APIKeyEntry{{APIKey: "k"}},
	}
	if !base.equal(same) {
		t.Fatal("equal keys reported as different")
	}
	if base.equal(apiKeyProviderModelsKeys{}) {
		t.Fatal("empty keys reported equal to populated keys")
	}
	changed := same
	changed.openCodeGo = []APIKeyEntry{{APIKey: "k2"}}
	if base.equal(changed) {
		t.Fatal("changed openCodeGo key reported equal")
	}
	empty := apiKeyProviderModelsKeys{}
	if empty.hasAny() {
		t.Fatal("empty keys should not hasAny")
	}
	if !base.hasAny() {
		t.Fatal("populated keys should hasAny")
	}
}

func TestApplyAPIKeyProviderModelKeysSkipsWhenDisabled(t *testing.T) {
	resetAPIKeyProviderModelsStoreForTest(t)
	SetAPIKeyModelsEnabled(false)
	if errApply := ApplyAPIKeyProviderModelKeys(context.Background(), []APIKeyEntry{{APIKey: "n"}}, nil, nil, nil); errApply != nil {
		t.Fatalf("ApplyAPIKeyProviderModelKeys() error = %v", errApply)
	}
	apiKeyStore.mu.RLock()
	defer apiKeyStore.mu.RUnlock()
	if apiKeyStore.keysInitialized {
		t.Fatal("keysInitialized = true when disabled, want false")
	}
}

func TestApplyAPIKeyProviderModelKeysStoresEmptyAndSkipsUnchanged(t *testing.T) {
	resetAPIKeyProviderModelsStoreForTest(t)
	SetAPIKeyModelsEnabled(true)
	if errFirst := ApplyAPIKeyProviderModelKeys(context.Background(), nil, nil, nil, nil); errFirst != nil {
		t.Fatalf("first ApplyAPIKeyProviderModelKeys() error = %v", errFirst)
	}
	apiKeyStore.mu.Lock()
	apiKeyStore.models = map[string][]*ModelInfo{"keep": {{ID: "keep"}}}
	apiKeyStore.mu.Unlock()

	if errSecond := ApplyAPIKeyProviderModelKeys(context.Background(), nil, nil, nil, nil); errSecond != nil {
		t.Fatalf("second ApplyAPIKeyProviderModelKeys() error = %v", errSecond)
	}
	apiKeyStore.mu.RLock()
	defer apiKeyStore.mu.RUnlock()
	if !apiKeyStore.keysInitialized {
		t.Fatal("keysInitialized = false, want true")
	}
	if _, ok := apiKeyStore.models["keep"]; !ok {
		t.Fatal("unchanged Apply replaced models map, want no-op")
	}
}

func TestApplyAPIKeyProviderModelKeysRefetchesOnChange(t *testing.T) {
	resetAPIKeyProviderModelsStoreForTest(t)
	SetAPIKeyModelsEnabled(true)
	if errInit := ApplyAPIKeyProviderModelKeys(context.Background(), nil, nil, nil, nil); errInit != nil {
		t.Fatalf("initial ApplyAPIKeyProviderModelKeys() error = %v", errInit)
	}
	apiKeyStore.mu.Lock()
	apiKeyStore.models = map[string][]*ModelInfo{"keep": {{ID: "keep"}}}
	apiKeyStore.mu.Unlock()

	// Cloudflare entries with empty AccountID are stored as a change but skipped by Fetch (no network).
	if errChange := ApplyAPIKeyProviderModelKeys(context.Background(), nil, []APIKeyEntry{{APIKey: "cf"}}, nil, nil); errChange != nil {
		t.Fatalf("changed ApplyAPIKeyProviderModelKeys() error = %v", errChange)
	}
	apiKeyStore.mu.RLock()
	defer apiKeyStore.mu.RUnlock()
	if _, ok := apiKeyStore.models["keep"]; ok {
		t.Fatal("changed Apply left models map untouched, want refetch")
	}
	want := apiKeyProviderModelsKeys{cloudflare: []APIKeyEntry{{APIKey: "cf"}}}
	if !apiKeyStore.keys.equal(want) {
		t.Fatal("stored keys do not match applied keys")
	}
}
