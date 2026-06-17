// Package qwen provides authentication and token management functionality
// for Qwen services. It handles OAuth2 device flow token storage,
// serialization, and retrieval for maintaining authenticated sessions with the Qwen API.
package qwen

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
)

// QwenTokenStorage stores OAuth2 token information for Qwen API authentication.
type QwenTokenStorage struct {
	// AccessToken is the OAuth2 access token used for authenticating API requests.
	AccessToken string `json:"access_token"`
	// RefreshToken is the OAuth2 refresh token used to obtain new access tokens.
	RefreshToken string `json:"refresh_token"`
	// TokenType is the type of token, typically "Bearer".
	TokenType string `json:"token_type"`
	// Scope is the OAuth2 scope granted to the token.
	Scope string `json:"scope,omitempty"`
	// DeviceID is the OAuth device flow identifier used for Qwen requests.
	DeviceID string `json:"device_id,omitempty"`
	// Expired is the RFC3339 timestamp when the access token expires.
	Expired string `json:"expired,omitempty"`
	// Type indicates the authentication provider type, always "qwen" for this storage.
	Type string `json:"type"`

	// Metadata holds arbitrary key-value pairs injected via hooks.
	// It is not exported to JSON directly to allow flattening during serialization.
	Metadata map[string]any `json:"-"`
}

// SetMetadata allows external callers to inject metadata into the storage before saving.
func (ts *QwenTokenStorage) SetMetadata(meta map[string]any) {
	ts.Metadata = meta
}

// QwenTokenData holds the raw OAuth token response from Qwen.
type QwenTokenData struct {
	// AccessToken is the OAuth2 access token.
	AccessToken string `json:"access_token"`
	// RefreshToken is the OAuth2 refresh token.
	RefreshToken string `json:"refresh_token"`
	// TokenType is the type of token, typically "Bearer".
	TokenType string `json:"token_type"`
	// ExpiresAt is the Unix timestamp when the token expires.
	ExpiresAt int64 `json:"expires_at"`
	// Scope is the OAuth2 scope granted to the token.
	Scope string `json:"scope"`
}

// QwenAuthBundle bundles authentication data for storage.
type QwenAuthBundle struct {
	// TokenData contains the OAuth token information.
	TokenData *QwenTokenData
	// DeviceID is the device identifier used during OAuth device flow.
	DeviceID string
}

// DeviceCodeResponse represents Qwen's device code response.
type DeviceCodeResponse struct {
	// DeviceCode is the device verification code.
	DeviceCode string `json:"device_code"`
	// UserCode is the code the user must enter at the verification URI.
	UserCode string `json:"user_code"`
	// VerificationURI is the URL where the user should enter the code.
	VerificationURI string `json:"verification_uri,omitempty"`
	// VerificationURIComplete is the URL with the code pre-filled.
	VerificationURIComplete string `json:"verification_uri_complete"`
	// ExpiresIn is the number of seconds until the device code expires.
	ExpiresIn int `json:"expires_in"`
	// Interval is the minimum number of seconds to wait between polling requests.
	Interval int `json:"interval"`
}

// SaveTokenToFile serializes the Qwen token storage to a JSON file.
func (ts *QwenTokenStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	ts.Type = "qwen"

	if err := os.MkdirAll(filepath.Dir(authFilePath), 0700); err != nil {
		return fmt.Errorf("failed to create directory: %v", err)
	}

	f, err := os.Create(authFilePath)
	if err != nil {
		return fmt.Errorf("failed to create token file: %w", err)
	}
	defer func() {
		_ = f.Close()
	}()

	// Merge metadata using helper
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
func (ts *QwenTokenStorage) IsExpired() bool {
	if ts.Expired == "" {
		return false // No expiry set, assume valid
	}
	t, err := time.Parse(time.RFC3339, ts.Expired)
	if err != nil {
		return true // Has expiry string but can't parse
	}
	// Consider expired if within refresh threshold
	return time.Now().Add(time.Duration(refreshThresholdSeconds) * time.Second).After(t)
}

// NeedsRefresh checks if the token should be refreshed.
func (ts *QwenTokenStorage) NeedsRefresh() bool {
	if ts.RefreshToken == "" {
		return false // Can't refresh without refresh token
	}
	return ts.IsExpired()
}
