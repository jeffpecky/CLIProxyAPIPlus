package mimofree

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"time"

	"github.com/google/uuid"
)

const (
	// PlatformURL is the Xiaomi MiMo platform URL.
	PlatformURL = "https://platform.xiaomimimo.com"
)

// BootstrapResponse represents the response from the bootstrap endpoint.
type BootstrapResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	BaseURL      string `json:"base_url,omitempty"`
}

// GenerateDeviceID creates a unique device identifier.
func GenerateDeviceID() string {
	return fmt.Sprintf("mimo-free-%s", uuid.New().String()[:12])
}

// Bootstrap sends a device fingerprint to the bootstrap endpoint to obtain tokens.
func Bootstrap(ctx context.Context, deviceID string) (*BootstrapResponse, error) {
	if deviceID == "" {
		deviceID = GenerateDeviceID()
	}

	payload := map[string]string{
		"device_id":      deviceID,
		"os":             runtime.GOOS,
		"arch":           runtime.GOARCH,
		"client_type":    "cli",
		"client_version": "1.0.0",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal bootstrap request: %w", err)
	}

	url := fmt.Sprintf("%s/api/free-ai/bootstrap", PlatformURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create bootstrap request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bootstrap request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read bootstrap response: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("bootstrap failed with status %d: %s", resp.StatusCode, string(respBody))
	}

	var result BootstrapResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to parse bootstrap response: %w", err)
	}

	if result.AccessToken == "" {
		return nil, fmt.Errorf("bootstrap returned empty access token")
	}

	return &result, nil
}
