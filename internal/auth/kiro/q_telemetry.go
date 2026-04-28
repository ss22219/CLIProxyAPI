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

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
)

// SendQTelemetryEvent sends a chatAddMessage telemetry event via the Q API
// (Bearer token, AmazonCodeWhispererService.SendTelemetryEvent).
// clientID should be a stable UUID from GetTelemetryClientID().
func SendQTelemetryEvent(ctx context.Context, httpClient *http.Client, accessToken, conversationID, modelID, profileArn string, responseLength int, timeToFirstChunkMs float64, timeBetweenChunks []float64) error {
	if strings.TrimSpace(accessToken) == "" {
		return fmt.Errorf("kiro: access token required for SendTelemetryEvent")
	}

	osUpper := strings.ToUpper(KiroOSTag())
	clientID := GetTelemetryClientID()

	body := map[string]any{
		"clientToken": uuid.New().String(),
		"telemetryEvent": map[string]any{
			"chatAddMessageEvent": map[string]any{
				"conversationId":               conversationID,
				"messageId":                    uuid.New().String(),
				"timeToFirstChunkMilliseconds": timeToFirstChunkMs,
				"timeBetweenChunks":            timeBetweenChunks,
				"responseLength":               responseLength,
			},
		},
		"optOutPreference": "OPTIN",
		"userContext": map[string]any{
			"ideCategory":     "CLI",
			"operatingSystem": osUpper,
			"product":         "CodeWhisperer",
			"clientId":        clientID,
			"ideVersion":      "2.0.1",
		},
		"modelId": modelID,
	}
	if strings.TrimSpace(profileArn) != "" {
		body["profileArn"] = profileArn
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("kiro: marshal SendTelemetryEvent: %w", err)
	}

	url := fmt.Sprintf("https://q.%s.amazonaws.com/", DefaultRegion)
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("kiro: create SendTelemetryEvent request: %w", err)
	}
	SetRuntimeHeaders(req, accessToken, "AmazonCodeWhispererService.SendTelemetryEvent")

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("kiro: SendTelemetryEvent request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		log.Debugf("kiro: SendTelemetryEvent failed (status %d): %s", resp.StatusCode, string(b))
		return fmt.Errorf("kiro: SendTelemetryEvent failed (status %d)", resp.StatusCode)
	}
	return nil
}
