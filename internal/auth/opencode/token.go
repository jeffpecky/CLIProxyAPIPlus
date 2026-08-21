// Package opencode provides a no-operation token storage implementation for the
// opencode provider. Since opencode is a free service that doesn't require
// authentication, this handler simply satisfies the provider interface.
package opencode

import "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/empty"

// TokenStorage is a no-op implementation for opencode's free tier.
// OpenCode doesn't require authentication tokens.
type TokenStorage struct {
	empty.EmptyStorage
}

// NewTokenStorage creates a new opencode token storage instance.
func NewTokenStorage() *TokenStorage {
	return &TokenStorage{}
}
