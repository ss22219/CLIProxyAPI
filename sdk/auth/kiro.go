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

const (
	kiroLoginModeMetadataKey = "kiro_login_mode"
	kiroLoginModeImport      = "import"
)

// kiroRefreshLead is the duration before token expiry when refresh should occur.
var kiroRefreshLead = 5 * time.Minute

// KiroAuthenticator implements the Authenticator interface for Kiro (AWS Q).
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

// Login performs Kiro authentication. Default is SSO device code flow.
// Set metadata key "kiro_login_mode" = "import" to import from kiro-cli SQLite instead.
func (a KiroAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	if shouldUseKiroImport(opts) {
		return a.loginFromSQLite(cfg, opts)
	}
	return a.loginWithDeviceCode(ctx, cfg, opts)
}

// loginWithDeviceCode performs the AWS SSO OIDC device code flow.
func (a KiroAuthenticator) loginWithDeviceCode(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	authSvc := kiro.NewKiroAuth(cfg)

	fmt.Println("Starting Kiro SSO authentication...")

	result, err := authSvc.LoginWithDeviceCode(ctx, kiro.DefaultRegion, opts.NoBrowser)
	if err != nil {
		return nil, fmt.Errorf("kiro: SSO login failed: %w", err)
	}

	expiresAt := ""
	if result.ExpiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(result.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
	}

	metadata := map[string]any{
		"type":          "kiro",
		"access_token":  result.AccessToken,
		"refresh_token": result.RefreshToken,
		"auth_method":   "oidc",
		"region":        result.Region,
		"client_id":     result.ClientID,
		"client_secret": result.ClientSecret,
		"timestamp":     time.Now().UnixMilli(),
	}
	if expiresAt != "" {
		metadata["expires_at"] = expiresAt
	}

	tokenStorage := &kiro.KiroTokenStorage{
		AccessToken:  result.AccessToken,
		RefreshToken: result.RefreshToken,
		AuthMethod:   "oidc",
		Region:       result.Region,
		ClientID:     result.ClientID,
		ClientSecret: result.ClientSecret,
		ExpiresAt:    expiresAt,
		Type:         "kiro",
	}

	fileName := fmt.Sprintf("kiro-sso.json")

	log.Debugf("kiro: SSO login successful (region=%s)", result.Region)
	fmt.Println("\nKiro SSO authentication successful!")

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Label:    "Kiro (SSO)",
		Storage:  tokenStorage,
		Metadata: metadata,
	}, nil
}

// loginFromSQLite imports credentials from the kiro-cli SQLite database.
func (a KiroAuthenticator) loginFromSQLite(cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	fmt.Println("Importing Kiro credentials from kiro-cli...")

	creds, err := kiro.ReadKiroCliCredentials()
	if err != nil {
		return nil, fmt.Errorf("kiro: failed to read kiro-cli credentials: %w", err)
	}

	authMethod := creds.AuthMethod
	if authMethod == "" {
		// Fallback: infer by presence of OIDC client material.
		authMethod = "social"
		if strings.TrimSpace(creds.ClientID) != "" && strings.TrimSpace(creds.ClientSecret) != "" {
			authMethod = "oidc"
		}
	}

	tokenStorage := &kiro.KiroTokenStorage{
		AccessToken:  creds.AccessToken,
		RefreshToken: creds.RefreshToken,
		AuthMethod:   authMethod,
		Region:       creds.Region,
		ClientID:     creds.ClientID,
		ClientSecret: creds.ClientSecret,
		ExpiresAt:    creds.ExpiresAt,
		ProfileArn:   creds.ProfileArn,
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
	if creds.ProfileArn != "" {
		metadata["profile_arn"] = creds.ProfileArn
	}
	if creds.Provider != "" {
		metadata["provider"] = creds.Provider
	}

	fileName := fmt.Sprintf("kiro-%d.json", time.Now().UnixMilli())

	log.Debugf("kiro: imported credentials (auth_method=%s, region=%s, profile_arn_present=%t)", authMethod, creds.Region, creds.ProfileArn != "")
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

func shouldUseKiroImport(opts *LoginOptions) bool {
	if opts == nil || opts.Metadata == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(opts.Metadata[kiroLoginModeMetadataKey]), kiroLoginModeImport)
}
