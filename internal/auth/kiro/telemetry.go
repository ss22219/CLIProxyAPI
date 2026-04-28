package kiro

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
)

const (
	telemetryEndpoint = "https://client-telemetry.us-east-1.amazonaws.com/metrics"
	cognitoPoolID     = "us-east-1:820fd6d1-95c0-4ca4-bffb-3f01d32da842"
	cognitoRegion     = "us-east-1"
	telemetryProduct  = "CodeWhisperer for Terminal"
	telemetryVersion  = "1.25.0"
)

// --- Cognito credential cache (process-wide) ---

var (
	cognitoMu     sync.Mutex
	cognitoCreds  *awsCreds
	cognitoExpiry time.Time
	cognitoID     string
)

type awsCreds struct {
	AccessKeyID  string
	SecretKey    string
	SessionToken string
}

// --- Telemetry payload types ---

type telemetryPayload struct {
	AWSProduct        string         `json:"AWSProduct"`
	AWSProductVersion string         `json:"AWSProductVersion"`
	ClientID          string         `json:"ClientID"`
	MetricData        []MetricDatum  `json:"MetricData"`
	OS                string         `json:"OS"`
	OSArchitecture    string         `json:"OSArchitecture"`
	OSVersion         string         `json:"OSVersion"`
}

// MetricDatum is a single telemetry metric.
type MetricDatum struct {
	MetricName     string          `json:"MetricName"`
	EpochTimestamp int64           `json:"EpochTimestamp"`
	Unit           string          `json:"Unit"`
	Value          float64         `json:"Value"`
	Metadata       []MetadataEntry `json:"Metadata"`
}

// MetadataEntry is a key-value pair in a metric.
type MetadataEntry struct {
	Key   string `json:"Key"`
	Value string `json:"Value"`
}

// --- Metric constructors ---

// LoginMetric creates a userLoggedIn metric.
func LoginMetric(startURL string) MetricDatum {
	return MetricDatum{
		MetricName: "codewhispererterminal_userLoggedIn", EpochTimestamp: nowMs(), Unit: "None", Value: 1,
		Metadata: []MetadataEntry{
			{Key: "credentialStartUrl", Value: startURL},
			{Key: "codewhispererterminal_inCloudshell", Value: "false"},
		},
	}
}

// SubcommandMetric creates a cliSubcommandExecuted metric.
func SubcommandMetric(subcommand, startURL string) MetricDatum {
	return MetricDatum{
		MetricName: "codewhispererterminal_cliSubcommandExecuted", EpochTimestamp: nowMs(), Unit: "None", Value: 1,
		Metadata: []MetadataEntry{
			{Key: "credentialStartUrl", Value: startURL},
			{Key: "codewhispererterminal_subcommand", Value: subcommand},
			{Key: "codewhispererterminal_inCloudshell", Value: ""},
			{Key: "codewhispererterminal_clientApplication", Value: ""},
		},
	}
}

// AgentConfigInitMetric creates an agentConfigInit metric.
func AgentConfigInitMetric(startURL string) MetricDatum {
	return MetricDatum{
		MetricName: "codewhispererterminal_agentConfigInit", EpochTimestamp: nowMs(), Unit: "None", Value: 1,
		Metadata: []MetadataEntry{
			{Key: "credentialStartUrl", Value: startURL},
			{Key: "amazonqConversationId", Value: uuid.New().String()},
			{Key: "codewhispererterminal_agentsLoadedCount", Value: "0"},
			{Key: "codewhispererterminal_agentsFailedToLoadCount", Value: "0"},
			{Key: "codewhispererterminal_legacyProfileMigrationExecuted", Value: "false"},
			{Key: "codewhispererterminal_legacyProfileMigratedCount", Value: "0"},
			{Key: "codewhispererterminal_launchedAgent", Value: "kiro_default"},
		},
	}
}

func nowMs() int64 { return time.Now().UnixMilli() }

// --- Send ---

// SendTelemetry sends metrics to the AWS Toolkit Telemetry service (fire-and-forget safe).
func SendTelemetry(ctx context.Context, httpClient *http.Client, metrics []MetricDatum) error {
	creds, err := getOrRefreshCognito(ctx, httpClient)
	if err != nil {
		return fmt.Errorf("kiro telemetry: cognito auth failed: %w", err)
	}

	payload := telemetryPayload{
		AWSProduct: telemetryProduct, AWSProductVersion: telemetryVersion,
		ClientID: uuid.New().String(), MetricData: metrics,
		OS: "linux", OSArchitecture: "x86_64",
		OSVersion: "Linux 6.6.87.2-microsoft-standard-WSL2 - Ubuntu 24.04.3 LTS",
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, telemetryEndpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	signV4(req, body, creds)

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("kiro telemetry: HTTP %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// SendLoginTelemetry sends the standard login + startup telemetry batch.
func SendLoginTelemetry(httpClient *http.Client, startURL string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	metrics := []MetricDatum{
		SubcommandMetric("login", startURL),
		LoginMetric(startURL),
		SubcommandMetric("chat", startURL),
		AgentConfigInitMetric(startURL),
	}
	if err := SendTelemetry(ctx, httpClient, metrics); err != nil {
		log.Debugf("kiro: telemetry send failed (non-fatal): %v", err)
	}
}

// --- Cognito anonymous credentials ---

func getOrRefreshCognito(ctx context.Context, client *http.Client) (*awsCreds, error) {
	cognitoMu.Lock()
	defer cognitoMu.Unlock()

	if cognitoCreds != nil && time.Now().Add(5*time.Minute).Before(cognitoExpiry) {
		return cognitoCreds, nil
	}

	cognitoURL := fmt.Sprintf("https://cognito-identity.%s.amazonaws.com/", cognitoRegion)

	// Step 1: GetId (reuse cached identity)
	if cognitoID == "" {
		id, err := cognitoGetID(ctx, client, cognitoURL)
		if err != nil {
			return nil, err
		}
		cognitoID = id
	}

	// Step 2: GetCredentialsForIdentity
	creds, expiry, err := cognitoGetCreds(ctx, client, cognitoURL, cognitoID)
	if err != nil {
		return nil, err
	}
	cognitoCreds = creds
	cognitoExpiry = expiry
	return creds, nil
}

func cognitoGetID(ctx context.Context, client *http.Client, baseURL string) (string, error) {
	body, _ := json.Marshal(map[string]string{"IdentityPoolId": cognitoPoolID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AWSCognitoIdentityService.GetId")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("cognito GetId: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("cognito GetId %d: %s", resp.StatusCode, string(b))
	}
	var result struct {
		IdentityId string `json:"IdentityId"`
	}
	if err = json.Unmarshal(b, &result); err != nil {
		return "", err
	}
	return result.IdentityId, nil
}

func cognitoGetCreds(ctx context.Context, client *http.Client, baseURL, identityID string) (*awsCreds, time.Time, error) {
	body, _ := json.Marshal(map[string]string{"IdentityId": identityID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AWSCognitoIdentityService.GetCredentialsForIdentity")

	resp, err := client.Do(req)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("cognito GetCredentials: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, time.Time{}, fmt.Errorf("cognito GetCredentials %d: %s", resp.StatusCode, string(b))
	}
	var result struct {
		Credentials struct {
			AccessKeyId  string  `json:"AccessKeyId"`
			SecretKey    string  `json:"SecretKey"`
			SessionToken string  `json:"SessionToken"`
			Expiration   float64 `json:"Expiration"`
		} `json:"Credentials"`
	}
	if err = json.Unmarshal(b, &result); err != nil {
		return nil, time.Time{}, err
	}
	c := &awsCreds{
		AccessKeyID:  result.Credentials.AccessKeyId,
		SecretKey:    result.Credentials.SecretKey,
		SessionToken: result.Credentials.SessionToken,
	}
	expiry := time.Now().Add(1 * time.Hour)
	if result.Credentials.Expiration > 0 {
		expiry = time.Unix(int64(result.Credentials.Expiration), 0)
	}
	return c, expiry, nil
}

// --- AWS Signature V4 ---

func signV4(req *http.Request, payload []byte, creds *awsCreds) {
	now := time.Now().UTC()
	dateStamp := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")
	payloadHash := sha256Hex(payload)

	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-user-agent", "aws-sdk-rust/1.3.11 ua/2.1 api/toolkittelemetry/1.0.0 os/linux lang/rust/1.92.0 app/AmazonQ-For-CLI")
	req.Header.Set("user-agent", "aws-sdk-rust/1.3.11 os/linux lang/rust/1.92.0")
	req.Header.Set("amz-sdk-request", "attempt=1; max=1")
	req.Header.Set("amz-sdk-invocation-id", uuid.New().String())
	if creds.SessionToken != "" {
		req.Header.Set("x-amz-security-token", creds.SessionToken)
	}

	host := "client-telemetry.us-east-1.amazonaws.com"
	contentLen := fmt.Sprintf("%d", len(payload))

	signedHeaders := "content-length;content-type;host;x-amz-date"
	if creds.SessionToken != "" {
		signedHeaders += ";x-amz-security-token"
	}
	signedHeaders += ";x-amz-user-agent"

	canonicalHeaders := "content-length:" + contentLen + "\n" +
		"content-type:application/json\n" +
		"host:" + host + "\n" +
		"x-amz-date:" + amzDate + "\n"
	if creds.SessionToken != "" {
		canonicalHeaders += "x-amz-security-token:" + creds.SessionToken + "\n"
	}
	canonicalHeaders += "x-amz-user-agent:aws-sdk-rust/1.3.11 ua/2.1 api/toolkittelemetry/1.0.0 os/linux lang/rust/1.92.0 app/AmazonQ-For-CLI\n"

	canonicalRequest := strings.Join([]string{
		"POST", "/metrics", "", canonicalHeaders, signedHeaders, payloadHash,
	}, "\n")

	credScope := dateStamp + "/" + cognitoRegion + "/execute-api/aws4_request"
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + credScope + "\n" + sha256Hex([]byte(canonicalRequest))

	sigKey := deriveSigningKey(creds.SecretKey, dateStamp, cognitoRegion, "execute-api")
	signature := hex.EncodeToString(hmacSHA256(sigKey, []byte(stringToSign)))

	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		creds.AccessKeyID, credScope, signedHeaders, signature))
}

func deriveSigningKey(secret, date, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(date))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	return hmacSHA256(kService, []byte("aws4_request"))
}

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
