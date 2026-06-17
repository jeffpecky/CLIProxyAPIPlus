// Package cline provides authentication and token management functionality
// for Cline services. It handles OAuth2 token storage, serialization,
// and retrieval for maintaining authenticated sessions with the Cline API.
package cline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
)

// ClineTokenStorage stores OAuth2 token information for Cline API authentication.
type ClineTokenStorage struct {
	// AccessToken is the OAuth2 access token used for authenticating API requests.
	AccessToken string `json:"access_token"`
	// RefreshToken is the OAuth2 refresh token used to obtain new access tokens.
	RefreshToken string `json:"refresh_token"`
	// TokenType is the type of token, typically "Bearer".
	TokenType string `json:"token_type"`
	// Email is the email address associated with the Cline account.
	Email string `json:"email"`
	// Expired is the RFC3339 timestamp when the access token expires.
	Expired string `json:"expired,omitempty"`
	// Type indicates the authentication provider type, always "cline" for this storage.
	Type string `json:"type"`

	// Metadata holds arbitrary key-value pairs injected via hooks.
	// It is not exported to JSON directly to allow flattening during serialization.
	Metadata map[string]any `json:"-"`
}

// SetMetadata allows external callers to inject metadata into the storage before saving.
func (ts *ClineTokenStorage) SetMetadata(meta map[string]any) {
	ts.Metadata = meta
}

// ClineTokenData holds the raw OAuth token response from Cline.
type ClineTokenData struct {
	// AccessToken is the OAuth2 access token.
	AccessToken string `json:"access_token"`
	// RefreshToken is the OAuth2 refresh token.
	RefreshToken string `json:"refresh_token"`
	// TokenType is the type of token, typically "Bearer".
	TokenType string `json:"token_type"`
	// Email is the email address associated with the account.
	Email string `json:"email"`
	// ExpiresAt is the Unix timestamp when the token expires.
	ExpiresAt int64 `json:"expires_at"`
}

// ClineAuthBundle bundles authentication data for storage.
type ClineAuthBundle struct {
	// TokenData contains the OAuth token information.
	TokenData *ClineTokenData
}

// SaveTokenToFile serializes the Cline token storage to a JSON file.
func (ts *ClineTokenStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	ts.Type = "cline"

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
// Cline tokens do not have an expiry field, so they are considered never expired.
func (ts *ClineTokenStorage) IsExpired() bool {
	return false
}

// NeedsRefresh checks if the token should be refreshed.
// Cline tokens do not have refresh capability, so they never need refresh.
func (ts *ClineTokenStorage) NeedsRefresh() bool {
	return false
}
