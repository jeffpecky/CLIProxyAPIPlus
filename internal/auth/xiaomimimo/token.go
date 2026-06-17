package xiaomimimo

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
)

type XiaomiMimoTokenStorage struct {
	AccessToken  string         `json:"access_token"`
	RefreshToken string         `json:"refresh_token"`
	TokenType    string         `json:"token_type"`
	Expired      string         `json:"expired,omitempty"`
	Type         string         `json:"type"`
	UID          string         `json:"uid,omitempty"`
	SK           string         `json:"sk,omitempty"`
	BaseURL      string         `json:"base_url,omitempty"`
	Metadata     map[string]any `json:"-"`
}

func (ts *XiaomiMimoTokenStorage) SetMetadata(meta map[string]any) { ts.Metadata = meta }

func (ts *XiaomiMimoTokenStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	ts.Type = "xiaomi-mimo"
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

func (ts *XiaomiMimoTokenStorage) IsExpired() bool {
	if ts.Expired == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, ts.Expired)
	if err != nil {
		return true
	}
	return time.Now().Add(5 * time.Minute).After(t)
}

func (ts *XiaomiMimoTokenStorage) NeedsRefresh() bool {
	if ts.RefreshToken == "" {
		return false
	}
	return ts.IsExpired()
}
