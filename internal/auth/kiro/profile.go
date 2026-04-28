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

// FetchProfileArn calls ListAvailableProfiles to get the profileArn for SSO auth.
func (k *KiroAuth) FetchProfileArn(ctx context.Context, accessToken string) (string, error) {
	if strings.TrimSpace(accessToken) == "" {
		return "", fmt.Errorf("kiro: access token required for ListAvailableProfiles")
	}

	url := fmt.Sprintf("https://codewhisperer.%s.amazonaws.com/", DefaultRegion)

	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader([]byte("{}")))
	if err != nil {
		return "", fmt.Errorf("kiro: create ListAvailableProfiles request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("x-amz-target", "AmazonCodeWhispererService.ListAvailableProfiles")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("x-amzn-codewhisperer-optout", "false")

	resp, err := k.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("kiro: ListAvailableProfiles request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("kiro: read ListAvailableProfiles response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		log.Debugf("kiro: ListAvailableProfiles failed (status %d): %s", resp.StatusCode, string(body))
		return "", fmt.Errorf("kiro: ListAvailableProfiles failed (status %d)", resp.StatusCode)
	}

	var result struct {
		Profiles []struct {
			Arn string `json:"arn"`
		} `json:"profiles"`
	}
	if err = json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("kiro: parse ListAvailableProfiles response: %w", err)
	}

	if len(result.Profiles) > 0 && result.Profiles[0].Arn != "" {
		return result.Profiles[0].Arn, nil
	}
	return "", nil
}
