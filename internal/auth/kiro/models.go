package kiro

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// KiroModel represents a model returned by the Kiro ListAvailableModels API.
type KiroModel struct {
	ModelID     string `json:"modelId"`
	ModelName   string `json:"modelName"`
	Description string `json:"description"`
}

// listModelsResponse is the response from ListAvailableModels.
type listModelsResponse struct {
	DefaultModel *struct {
		ModelID string `json:"modelId"`
	} `json:"defaultModel"`
	Models []KiroModel `json:"models"`
}

// FetchModels calls the Kiro ListAvailableModels API and returns the available models.
func (k *KiroAuth) FetchModels(ctx context.Context, accessToken, profileArn string) ([]KiroModel, error) {
	if strings.TrimSpace(accessToken) == "" {
		return nil, fmt.Errorf("kiro: access token required for ListAvailableModels")
	}

	region := DefaultRegion
	endpointURL := listAvailableModelsURL(region, profileArn)

	body := map[string]string{"origin": "KIRO_CLI"}
	if strings.TrimSpace(profileArn) != "" {
		body["profileArn"] = profileArn
	}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("kiro: marshal ListAvailableModels request: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpointURL, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("kiro: create ListAvailableModels request: %w", err)
	}
	SetModelsHeaders(req, accessToken)

	resp, err := k.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kiro: ListAvailableModels request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("kiro: read ListAvailableModels response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		log.Debugf("kiro: ListAvailableModels failed (status %d): %s", resp.StatusCode, string(respBody))
		return nil, fmt.Errorf("kiro: ListAvailableModels failed (status %d)", resp.StatusCode)
	}

	var result listModelsResponse
	if err = json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("kiro: parse ListAvailableModels response: %w", err)
	}

	return result.Models, nil
}

func listAvailableModelsURL(region, profileArn string) string {
	if strings.TrimSpace(region) == "" {
		region = DefaultRegion
	}
	u := url.URL{
		Scheme: "https",
		Host:   fmt.Sprintf("q.%s.amazonaws.com", region),
		Path:   "/",
	}
	q := u.Query()
	q.Set("origin", "KIRO_CLI")
	if trimmedArn := strings.TrimSpace(profileArn); trimmedArn != "" {
		q.Set("profileArn", trimmedArn)
	}
	u.RawQuery = q.Encode()
	return u.String()
}
