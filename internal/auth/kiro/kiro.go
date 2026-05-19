// Package kiro provides authentication and token management for Kiro (AWS Q) API.
// It supports both OIDC (SSO) and Social (Google/GitHub) token refresh flows,
// and can import credentials from the kiro-cli SQLite database.
package kiro

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	// DefaultRegion is the default AWS region for Kiro.
	DefaultRegion = "us-east-1"
	// DefaultBuilderIDProfileArn is the profile ARN used by kiro-cli for
	// BuilderId/OIDC users when no account-specific profile is cached locally.
	DefaultBuilderIDProfileArn = "arn:aws:codewhisperer:us-east-1:638616132270:profile/AAAACCCCXXXX"
	// oidcTokenURLTemplate is the OIDC token endpoint.
	oidcTokenURLTemplate = "https://oidc.%s.amazonaws.com/token"
	// socialRefreshURLTemplate is the social token refresh endpoint.
	socialRefreshURLTemplate = "https://prod.%s.auth.desktop.kiro.dev/refreshToken"
)

// KiroAuth handles Kiro authentication flows.
type KiroAuth struct {
	httpClient *http.Client
	cfg        *config.Config
}

// NewKiroAuth creates a new KiroAuth service instance.
func NewKiroAuth(cfg *config.Config) *KiroAuth {
	return NewKiroAuthWithProxyURL(cfg, "")
}

// NewKiroAuthWithProxyURL creates a new KiroAuth with an optional proxy override.
func NewKiroAuthWithProxyURL(cfg *config.Config, proxyURL string) *KiroAuth {
	client := &http.Client{Timeout: 30 * time.Second}
	effectiveProxyURL := strings.TrimSpace(proxyURL)
	var sdkCfg config.SDKConfig
	if cfg != nil {
		sdkCfg = cfg.SDKConfig
		if effectiveProxyURL == "" {
			effectiveProxyURL = strings.TrimSpace(cfg.ProxyURL)
		}
	}
	sdkCfg.ProxyURL = effectiveProxyURL
	client = util.SetProxy(&sdkCfg, client)
	return &KiroAuth{httpClient: client, cfg: cfg}
}

// RefreshToken refreshes a Kiro token. It auto-detects the auth method
// (OIDC vs Social) and calls the appropriate endpoint.
func (k *KiroAuth) RefreshToken(ctx context.Context, storage *KiroTokenStorage) (*KiroTokenStorage, error) {
	if storage == nil || strings.TrimSpace(storage.RefreshToken) == "" {
		return nil, fmt.Errorf("kiro: no refresh token available")
	}

	region := storage.Region
	if region == "" {
		region = DefaultRegion
	}

	switch storage.AuthMethod {
	case "oidc":
		return k.refreshOIDC(ctx, storage, region)
	case "social", "":
		result, err := k.refreshSocial(ctx, storage, region)
		if err != nil && isSsoTokenError(err) {
			log.Debug("kiro: social refresh failed with SSO-like error, trying OIDC")
			storage.AuthMethod = "oidc"
			return k.refreshOIDC(ctx, storage, region)
		}
		return result, err
	default:
		return nil, fmt.Errorf("kiro: unknown auth method %q", storage.AuthMethod)
	}
}

// refreshSocial refreshes a token using the Social (Google/GitHub) endpoint.
func (k *KiroAuth) refreshSocial(ctx context.Context, storage *KiroTokenStorage, region string) (*KiroTokenStorage, error) {
	url := fmt.Sprintf(socialRefreshURLTemplate, region)

	body := map[string]string{"refreshToken": storage.RefreshToken}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("kiro: marshal social refresh request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("kiro: create social refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", KiroIDEUserAgent(storage.RefreshToken))
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Encoding", "gzip")
	req.Close = true

	resp, err := k.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kiro: social refresh request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respReader := io.Reader(resp.Body)
	if strings.EqualFold(strings.TrimSpace(resp.Header.Get("Content-Encoding")), "gzip") {
		gzipReader, errGzip := gzip.NewReader(resp.Body)
		if errGzip != nil {
			return nil, fmt.Errorf("kiro: decode social refresh gzip response: %w", errGzip)
		}
		defer func() { _ = gzipReader.Close() }()
		respReader = gzipReader
	}

	respBody, err := io.ReadAll(respReader)
	if err != nil {
		return nil, fmt.Errorf("kiro: read social refresh response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		errStr := string(respBody)
		if strings.Contains(errStr, "invalid_grant") &&
			(strings.Contains(errStr, "Public client") || strings.Contains(errStr, "PresignedUrl")) {
			return nil, &ssoTokenError{msg: errStr}
		}
		return nil, fmt.Errorf("kiro: social refresh failed (status %d): %s", resp.StatusCode, errStr)
	}

	var tokenResp struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresIn    int    `json:"expiresIn"`
		ProfileArn   string `json:"profileArn"`
	}
	if err = json.Unmarshal(respBody, &tokenResp); err != nil {
		return nil, fmt.Errorf("kiro: parse social refresh response: %w", err)
	}
	if tokenResp.AccessToken == "" {
		return nil, fmt.Errorf("kiro: empty access token in social refresh response")
	}

	result := &KiroTokenStorage{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: storage.RefreshToken,
		AuthMethod:   "social",
		Region:       region,
		ClientID:     storage.ClientID,
		ClientSecret: storage.ClientSecret,
		ProfileArn:   strings.TrimSpace(tokenResp.ProfileArn),
		Type:         "kiro",
	}
	if result.ProfileArn == "" {
		result.ProfileArn = storage.ProfileArn
	}
	if tokenResp.RefreshToken != "" {
		result.RefreshToken = tokenResp.RefreshToken
	}
	if tokenResp.ExpiresIn > 0 {
		result.ExpiresAt = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
	}
	return result, nil
}

// refreshOIDC refreshes a token using the OIDC (SSO) endpoint.
func (k *KiroAuth) refreshOIDC(ctx context.Context, storage *KiroTokenStorage, region string) (*KiroTokenStorage, error) {
	if strings.TrimSpace(storage.ClientID) == "" || strings.TrimSpace(storage.ClientSecret) == "" {
		return nil, fmt.Errorf("kiro: OIDC refresh requires client_id and client_secret")
	}

	url := fmt.Sprintf(oidcTokenURLTemplate, region)

	body := map[string]string{
		"clientId":     storage.ClientID,
		"clientSecret": storage.ClientSecret,
		"grantType":    "refresh_token",
		"refreshToken": storage.RefreshToken,
	}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("kiro: marshal OIDC refresh request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("kiro: create OIDC refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("amz-sdk-invocation-id", uuid.New().String())
	req.Header.Set("amz-sdk-request", "attempt=1; max=4")

	resp, err := k.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kiro: OIDC refresh request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("kiro: read OIDC refresh response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kiro: OIDC refresh failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	var tokenResp struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresIn    int    `json:"expiresIn"`
		TokenType    string `json:"tokenType"`
	}
	if err = json.Unmarshal(respBody, &tokenResp); err != nil {
		return nil, fmt.Errorf("kiro: parse OIDC refresh response: %w", err)
	}
	if tokenResp.AccessToken == "" {
		return nil, fmt.Errorf("kiro: empty access token in OIDC refresh response")
	}

	result := &KiroTokenStorage{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: storage.RefreshToken,
		AuthMethod:   "oidc",
		Region:       region,
		ClientID:     storage.ClientID,
		ClientSecret: storage.ClientSecret,
		ProfileArn:   profileArnOrDefault(storage.ProfileArn, "oidc"),
		Type:         "kiro",
	}
	if tokenResp.RefreshToken != "" {
		result.RefreshToken = tokenResp.RefreshToken
	}
	if tokenResp.ExpiresIn > 0 {
		result.ExpiresAt = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
	}
	return result, nil
}

// ssoTokenError indicates the social refresh endpoint detected an SSO token.
type ssoTokenError struct{ msg string }

func (e *ssoTokenError) Error() string { return e.msg }

func isSsoTokenError(err error) bool {
	_, ok := err.(*ssoTokenError)
	return ok
}

func profileArnOrDefault(profileArn, authMethod string) string {
	trimmed := strings.TrimSpace(profileArn)
	if trimmed != "" {
		return trimmed
	}
	switch strings.ToLower(strings.TrimSpace(authMethod)) {
	case "oidc", "sso", "builderid", "builder_id":
		return DefaultBuilderIDProfileArn
	default:
		return ""
	}
}
