// Package kiro provides authentication and token management for Kiro (AWS Q) API.
// It supports both OIDC (SSO) and Social (Google/GitHub) token refresh flows.
package kiro

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
)

// KiroTokenStorage stores OAuth2 token information for Kiro API authentication.
type KiroTokenStorage struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	AuthMethod   string `json:"auth_method"` // "oidc" or "social"
	Region       string `json:"region"`
	ClientID     string `json:"client_id,omitempty"`
	ClientSecret string `json:"client_secret,omitempty"`
	ExpiresAt    string `json:"expires_at,omitempty"`
	Type         string `json:"type"`

	Metadata map[string]any `json:"-"`
}

// SetMetadata allows external callers to inject metadata into the storage before saving.
func (ts *KiroTokenStorage) SetMetadata(meta map[string]any) {
	ts.Metadata = meta
}

// SaveTokenToFile serializes the Kiro token storage to a JSON file.
func (ts *KiroTokenStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	ts.Type = "kiro"

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
func (ts *KiroTokenStorage) IsExpired() bool {
	if ts.ExpiresAt == "" {
		return true
	}
	t, err := time.Parse(time.RFC3339, ts.ExpiresAt)
	if err != nil {
		return true
	}
	return time.Now().Add(5 * time.Minute).After(t)
}

// NeedsRefresh checks if the token should be refreshed.
func (ts *KiroTokenStorage) NeedsRefresh() bool {
	if ts.RefreshToken == "" {
		return false
	}
	return ts.IsExpired()
}
