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

// FetchProfileArn cannot discover a profileArn by calling GetProfile without
// one. Kiro stores it in the social token or api.codewhisperer.profile state.
func (k *KiroAuth) FetchProfileArn(ctx context.Context, accessToken string) (string, error) {
	if strings.TrimSpace(accessToken) == "" {
		return "", fmt.Errorf("kiro: access token required for GetProfile")
	}
	return "", fmt.Errorf("kiro: profileArn is required; GetProfile cannot discover it")
}

// FetchProfile calls GetProfile on the Q API for a known profileArn.
func (k *KiroAuth) FetchProfile(ctx context.Context, accessToken, profileArn string) (string, error) {
	if strings.TrimSpace(accessToken) == "" {
		return "", fmt.Errorf("kiro: access token required for GetProfile")
	}
	trimmedProfileArn := strings.TrimSpace(profileArn)
	if trimmedProfileArn == "" {
		return "", fmt.Errorf("kiro: profileArn required for GetProfile")
	}

	url := fmt.Sprintf("https://q.%s.amazonaws.com/", DefaultRegion)

	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	jsonBody, err := json.Marshal(map[string]string{"profileArn": trimmedProfileArn})
	if err != nil {
		return "", fmt.Errorf("kiro: marshal GetProfile request: %w", err)
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(jsonBody))
	if err != nil {
		return "", fmt.Errorf("kiro: create GetProfile request: %w", err)
	}
	SetRuntimeHeaders(req, accessToken, "AmazonCodeWhispererService.GetProfile")

	resp, err := k.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("kiro: GetProfile request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("kiro: read GetProfile response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		log.Debugf("kiro: GetProfile failed (status %d): %s", resp.StatusCode, string(body))
		return "", fmt.Errorf("kiro: GetProfile failed (status %d)", resp.StatusCode)
	}

	var result struct {
		Profile struct {
			Arn string `json:"arn"`
		} `json:"profile"`
	}
	if err = json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("kiro: parse GetProfile response: %w", err)
	}

	return result.Profile.Arn, nil
}
