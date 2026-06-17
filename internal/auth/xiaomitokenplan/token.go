package xiaomitokenplan

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
)

// XiaomiTokenPlanStorage stores API key authentication for Xiaomi TokenPlan.
type XiaomiTokenPlanStorage struct {
	APIKey   string         `json:"api_key"`
	Region   string         `json:"region,omitempty"`
	BaseURL  string         `json:"base_url,omitempty"`
	Type     string         `json:"type"`
	Metadata map[string]any `json:"-"`
}

// SetMetadata allows external callers to inject metadata into the storage before saving.
func (ts *XiaomiTokenPlanStorage) SetMetadata(meta map[string]any) { ts.Metadata = meta }

// SaveTokenToFile serializes the token storage to a JSON file.
func (ts *XiaomiTokenPlanStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	ts.Type = "xiaomi-tokenplan"
	if err := os.MkdirAll(filepath.Dir(authFilePath), 0700); err != nil {
		return fmt.Errorf("failed to create directory: %v", err)
	}
	f, err := os.Create(authFilePath)
	if err != nil {
		return fmt.Errorf("failed to create token file: %w", err)
	}
	defer func() { _ = f.Close() }()
	data, errMerge := misc.MergeMetadata(ts, ts.Metadata)
	if errMerge != nil {
		return fmt.Errorf("failed to merge metadata: %w", errMerge)
	}
	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(data); err != nil {
		return fmt.Errorf("failed to write token to file: %w", err)
	}
	return nil
}
