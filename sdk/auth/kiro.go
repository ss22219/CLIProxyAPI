package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// kiroRefreshLead is the duration before token expiry when refresh should occur.
var kiroRefreshLead = 5 * time.Minute

// KiroAuthenticator implements the Authenticator interface for Kiro (AWS Q).
// It imports credentials from the kiro-cli SQLite database and supports
// both OIDC (SSO) and Social (Google/GitHub) token refresh.
type KiroAuthenticator struct{}

// NewKiroAuthenticator constructs a new Kiro authenticator.
func NewKiroAuthenticator() Authenticator {
	return &KiroAuthenticator{}
}

// Provider returns the provider key.
func (KiroAuthenticator) Provider() string {
	return "kiro"
}

// RefreshLead returns the duration before token expiry when refresh should occur.
func (KiroAuthenticator) RefreshLead() *time.Duration {
	return &kiroRefreshLead
}

// Login imports Kiro credentials from the kiro-cli SQLite database.
func (a KiroAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	fmt.Println("Importing Kiro credentials from kiro-cli...")

	creds, err := kiro.ReadKiroCliCredentials()
	if err != nil {
		return nil, fmt.Errorf("kiro: failed to read kiro-cli credentials: %w", err)
	}

	// Determine auth method based on presence of client_id/client_secret
	authMethod := "social"
	if strings.TrimSpace(creds.ClientID) != "" && strings.TrimSpace(creds.ClientSecret) != "" {
		authMethod = "oidc"
	}

	tokenStorage := &kiro.KiroTokenStorage{
		AccessToken:  creds.AccessToken,
		RefreshToken: creds.RefreshToken,
		AuthMethod:   authMethod,
		Region:       creds.Region,
		ClientID:     creds.ClientID,
		ClientSecret: creds.ClientSecret,
		ExpiresAt:    creds.ExpiresAt,
		Type:         "kiro",
	}

	metadata := map[string]any{
		"type":          "kiro",
		"access_token":  creds.AccessToken,
		"refresh_token": creds.RefreshToken,
		"auth_method":   authMethod,
		"region":        creds.Region,
		"timestamp":     time.Now().UnixMilli(),
	}
	if creds.ClientID != "" {
		metadata["client_id"] = creds.ClientID
	}
	if creds.ClientSecret != "" {
		metadata["client_secret"] = creds.ClientSecret
	}
	if creds.ExpiresAt != "" {
		metadata["expires_at"] = creds.ExpiresAt
	}

	fileName := fmt.Sprintf("kiro-%d.json", time.Now().UnixMilli())

	log.Debugf("kiro: imported credentials (auth_method=%s, region=%s)", authMethod, creds.Region)
	fmt.Printf("Kiro authentication imported successfully (method: %s)\n", authMethod)

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Label:    fmt.Sprintf("Kiro (%s)", authMethod),
		Storage:  tokenStorage,
		Metadata: metadata,
	}, nil
}
