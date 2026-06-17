package mimofree

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
)

// MimoFreeTokenStorage stores auto-bootstrapped MiMo Free credentials.
type MimoFreeTokenStorage struct {
	AccessToken  string         `json:"access_token"`
	RefreshToken string         `json:"refresh_token"`
	TokenType    string         `json:"token_type"`
	Expired      string         `json:"expired,omitempty"`
	Type         string         `json:"type"`
	BaseURL      string         `json:"base_url,omitempty"`
	DeviceID     string         `json:"device_id,omitempty"`
	Metadata     map[string]any `json:"-"`
}

// SetMetadata allows external callers to inject metadata into the storage before saving.
func (ts *MimoFreeTokenStorage) SetMetadata(meta map[string]any) { ts.Metadata = meta }

// SaveTokenToFile serializes the token storage to a JSON file.
func (ts *MimoFreeTokenStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	ts.Type = "mimo-free"
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

// IsExpired checks if the token has expired.
func (ts *MimoFreeTokenStorage) IsExpired() bool {
	if ts.Expired == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, ts.Expired)
	if err != nil {
		return true
	}
	return time.Now().Add(5 * time.Minute).After(t)
}

// NeedsRefresh checks if the token should be refreshed.
func (ts *MimoFreeTokenStorage) NeedsRefresh() bool {
	if ts.RefreshToken == "" {
		return false
	}
	return ts.IsExpired()
}
