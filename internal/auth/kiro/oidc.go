package kiro

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// AWS SSO OIDC constants matching amazon-q-developer-cli/crates/chat-cli/src/auth/consts.rs
const (
	oidcClientName    = "Kiro CLI"
	oidcClientType    = "public"
	oidcStartURL      = "https://view.awsapps.com/start"
	oidcDeviceGrant   = "urn:ietf:params:oauth:grant-type:device_code"
	oidcDefaultPoll   = 5 * time.Second
	oidcDeviceTimeout = 5 * time.Minute
)

var oidcScopes = []string{
	"codewhisperer:completions",
	"codewhisperer:analysis",
	"codewhisperer:conversations",
}

// RegisterClientResponse is the response from OIDC /client/register.
type RegisterClientResponse struct {
	ClientID              string `json:"clientId"`
	ClientSecret          string `json:"clientSecret"`
	ClientSecretExpiresAt int64  `json:"clientSecretExpiresAt"`
}

// StartDeviceAuthResponse is the response from OIDC /device_authorization.
type StartDeviceAuthResponse struct {
	DeviceCode              string `json:"deviceCode"`
	UserCode                string `json:"userCode"`
	VerificationURI         string `json:"verificationUri"`
	VerificationURIComplete string `json:"verificationUriComplete"`
	ExpiresIn               int    `json:"expiresIn"`
	Interval                int    `json:"interval"`
}

// CreateTokenResponse is the response from OIDC /token.
type CreateTokenResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresIn    int    `json:"expiresIn"`
	TokenType    string `json:"tokenType"`
}

// SSOLoginResult holds the result of a successful SSO device code login.
type SSOLoginResult struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
	Region       string
	ClientID     string
	ClientSecret string
}

// oidcBaseURL returns the OIDC base URL for a region.
func oidcBaseURL(region string) string {
	return fmt.Sprintf("https://oidc.%s.amazonaws.com", region)
}

// oidcPost sends a JSON POST to the OIDC endpoint and decodes the response.
func (k *KiroAuth) oidcPost(ctx context.Context, url string, body any, result any) error {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("kiro oidc: marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("kiro oidc: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := k.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("kiro oidc: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("kiro oidc: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("kiro oidc: status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	if err = json.Unmarshal(respBody, result); err != nil {
		return fmt.Errorf("kiro oidc: parse response: %w", err)
	}
	return nil
}

// RegisterClient registers an OIDC client for the device code flow.
func (k *KiroAuth) RegisterClient(ctx context.Context, region string) (*RegisterClientResponse, error) {
	url := oidcBaseURL(region) + "/client/register"
	body := map[string]any{
		"clientName": oidcClientName,
		"clientType": oidcClientType,
		"scopes":     oidcScopes,
		"grantTypes": []string{oidcDeviceGrant},
	}
	var resp RegisterClientResponse
	if err := k.oidcPost(ctx, url, body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// StartDeviceAuthorization starts the device authorization flow.
func (k *KiroAuth) StartDeviceAuthorization(ctx context.Context, region, clientID, clientSecret string) (*StartDeviceAuthResponse, error) {
	url := oidcBaseURL(region) + "/device_authorization"
	body := map[string]string{
		"clientId":     clientID,
		"clientSecret": clientSecret,
		"startUrl":     oidcStartURL,
	}
	var resp StartDeviceAuthResponse
	if err := k.oidcPost(ctx, url, body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// PollForToken polls the OIDC token endpoint until authorization completes or times out.
func (k *KiroAuth) PollForToken(ctx context.Context, region string, reg *RegisterClientResponse, deviceAuth *StartDeviceAuthResponse) (*CreateTokenResponse, error) {
	url := oidcBaseURL(region) + "/token"
	interval := time.Duration(max(deviceAuth.Interval, 5)) * time.Second
	deadline := time.Now().Add(oidcDeviceTimeout)

	body := map[string]string{
		"clientId":     reg.ClientID,
		"clientSecret": reg.ClientSecret,
		"grantType":    oidcDeviceGrant,
		"deviceCode":   deviceAuth.DeviceCode,
	}

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}

		var resp CreateTokenResponse
		err := k.oidcPost(ctx, url, body, &resp)
		if err == nil && resp.AccessToken != "" {
			return &resp, nil
		}
		if err != nil {
			errStr := err.Error()
			if strings.Contains(errStr, "authorization_pending") {
				continue
			}
			if strings.Contains(errStr, "slow_down") {
				interval += 5 * time.Second
				continue
			}
			return nil, err
		}
	}
	return nil, fmt.Errorf("kiro: device authorization timed out")
}

// LoginWithDeviceCode performs the full SSO device code login flow.
func (k *KiroAuth) LoginWithDeviceCode(ctx context.Context, region string, noBrowser bool) (*SSOLoginResult, error) {
	if region == "" {
		region = DefaultRegion
	}

	// Step 1: Register OIDC client
	reg, err := k.RegisterClient(ctx, region)
	if err != nil {
		return nil, fmt.Errorf("kiro: register client failed: %w", err)
	}
	log.Debugf("kiro: registered OIDC client %s", reg.ClientID[:min(20, len(reg.ClientID))])

	// Step 2: Start device authorization
	deviceAuth, err := k.StartDeviceAuthorization(ctx, region, reg.ClientID, reg.ClientSecret)
	if err != nil {
		return nil, fmt.Errorf("kiro: start device authorization failed: %w", err)
	}

	verificationURL := deviceAuth.VerificationURIComplete
	if verificationURL == "" {
		verificationURL = deviceAuth.VerificationURI
	}

	fmt.Printf("\nTo authenticate with Kiro, please visit:\n%s\n\n", verificationURL)
	if deviceAuth.UserCode != "" {
		fmt.Printf("User code: %s\n\n", deviceAuth.UserCode)
	}
	fmt.Println("Waiting for authorization...")

	// Step 3: Poll for token
	tokenResp, err := k.PollForToken(ctx, region, reg, deviceAuth)
	if err != nil {
		return nil, err
	}

	return &SSOLoginResult{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ExpiresIn:    tokenResp.ExpiresIn,
		Region:       region,
		ClientID:     reg.ClientID,
		ClientSecret: reg.ClientSecret,
	}, nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
