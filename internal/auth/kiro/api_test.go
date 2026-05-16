package kiro

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestFetchProfileArn_URLAndHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify target
		target := r.Header.Get("x-amz-target")
		if target != "AmazonCodeWhispererService.GetProfile" {
			t.Errorf("x-amz-target = %q, want AmazonCodeWhispererService.GetProfile", target)
		}
		// Verify Content-Type has no charset
		ct := r.Header.Get("Content-Type")
		if ct != "application/x-amz-json-1.0" {
			t.Errorf("Content-Type = %q, want application/x-amz-json-1.0", ct)
		}
		// Verify Authorization
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			t.Errorf("Authorization = %q, want Bearer prefix", auth)
		}
		// Verify runtime UA
		if !strings.Contains(r.Header.Get("User-Agent"), "codewhispererruntime") {
			t.Errorf("User-Agent missing codewhispererruntime")
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"profile":{"arn":"arn:aws:q:us-east-1:123:profile/test"}`))
	}))
	defer srv.Close()

	// We can't easily override the URL in FetchProfileArn since it's hardcoded,
	// so we test the header-setting logic directly instead.
	req, _ := http.NewRequest(http.MethodPost, srv.URL, nil)
	SetRuntimeHeaders(req, "test-token", "AmazonCodeWhispererService.GetProfile")

	if req.Header.Get("x-amz-target") != "AmazonCodeWhispererService.GetProfile" {
		t.Error("GetProfile target not set correctly")
	}
}

func TestFetchModels_HeadersWiring(t *testing.T) {
	// Verify that FetchModels would use SetModelsHeaders by checking the header pattern
	req, _ := http.NewRequest(http.MethodPost, "https://q.us-east-1.amazonaws.com/", nil)
	SetModelsHeaders(req, "test-token")

	target := req.Header.Get("x-amz-target")
	if target != "AmazonCodeWhispererService.ListAvailableModels" {
		t.Errorf("x-amz-target = %q, want AmazonCodeWhispererService.ListAvailableModels", target)
	}

	ua := req.Header.Get("x-amz-user-agent")
	if !strings.Contains(ua, "m/F,C") {
		t.Errorf("x-amz-user-agent missing m/F,C: %s", ua)
	}

	if req.Header.Get("Accept") != "*/*" {
		t.Errorf("Accept = %q, want */*", req.Header.Get("Accept"))
	}
	if req.Header.Get("Accept-Encoding") != "gzip" {
		t.Errorf("Accept-Encoding = %q, want gzip", req.Header.Get("Accept-Encoding"))
	}
	if req.Header.Get("amz-sdk-invocation-id") == "" {
		t.Error("amz-sdk-invocation-id must be set")
	}
}

func TestQHeadersMatchKiroCLI23Shape(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "https://q.us-east-1.amazonaws.com/", nil)
	SetStreamingHeaders(req, "ksk_test")

	userAgent := req.Header.Get("User-Agent")
	if !strings.Contains(userAgent, "aws-sdk-rust/1.3.15") {
		t.Fatalf("User-Agent = %q, want aws-sdk-rust/1.3.15", userAgent)
	}
	if !strings.Contains(userAgent, "md/appVersion-2.3.0") {
		t.Fatalf("User-Agent = %q, want appVersion-2.3.0", userAgent)
	}
	if strings.Contains(userAgent, "exec-env/") || strings.Contains(userAgent, "Version/2.2.2") {
		t.Fatalf("User-Agent = %q, still has legacy 2.2.2 markers", userAgent)
	}

	amzUserAgent := req.Header.Get("x-amz-user-agent")
	if !strings.Contains(amzUserAgent, "aws-sdk-rust/1.3.15") {
		t.Fatalf("x-amz-user-agent = %q, want aws-sdk-rust/1.3.15", amzUserAgent)
	}
	if !strings.Contains(amzUserAgent, " m/F app/AmazonQ-For-CLI") {
		t.Fatalf("x-amz-user-agent = %q, want m/F suffix", amzUserAgent)
	}
	if strings.Contains(amzUserAgent, "exec-env/") || strings.Contains(amzUserAgent, "Version/2.2.2") {
		t.Fatalf("x-amz-user-agent = %q, still has legacy 2.2.2 markers", amzUserAgent)
	}
	if got := req.Header.Get("tokenType"); got != "API_KEY" {
		t.Fatalf("tokenType = %q, want API_KEY for Kiro API key", got)
	}
}

func TestFetchModels_AllowsEmptyProfileArnForAPIKey(t *testing.T) {
	svc := NewKiroAuth(nil)
	_, err := svc.FetchModels(context.Background(), "test-token", "")
	if err == nil || strings.Contains(err.Error(), "profileArn required") {
		t.Fatalf("FetchModels error = %v, want no profileArn validation error", err)
	}
}

func TestListModelsResponseParsesTokenLimits(t *testing.T) {
	var result listModelsResponse
	err := json.Unmarshal([]byte(`{"models":[{"modelId":"claude-opus-4.7","tokenLimits":{"maxInputTokens":1000000,"maxOutputTokens":64000},"additionalModelRequestFieldsSchema":{"type":"object","properties":{"output_config":{"type":"object","properties":{"effort":{"type":"string","enum":["low","medium","high","xhigh","max"]}}}}}}]}`), &result)
	if err != nil {
		t.Fatalf("unmarshal listModelsResponse: %v", err)
	}
	if len(result.Models) != 1 {
		t.Fatalf("models length = %d, want 1", len(result.Models))
	}
	if got := result.Models[0].TokenLimits.MaxInputTokens; got != 1000000 {
		t.Fatalf("maxInputTokens = %d, want 1000000", got)
	}
	if got := result.Models[0].TokenLimits.MaxOutputTokens; got != 64000 {
		t.Fatalf("maxOutputTokens = %d, want 64000", got)
	}
	if len(result.Models[0].AdditionalModelRequestFieldsSchema) == 0 {
		t.Fatal("additionalModelRequestFieldsSchema should be parsed")
	}
}

func TestUsageLimitsCreditExhaustedIncludesOverageCap(t *testing.T) {
	var result UsageLimitsResponse
	err := json.Unmarshal([]byte(`{
		"nextDateReset": 1780272000,
		"overageConfiguration": {"overageStatus": "ENABLED"},
		"usageBreakdownList": [{
			"resourceType": "CREDIT",
			"currentUsageWithPrecision": 11005.46,
			"usageLimitWithPrecision": 1000.0,
			"overageCapWithPrecision": 10000.0,
			"nextDateReset": 1780272000
		}]
	}`), &result)
	if err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	exhausted, resetAt, msg := result.CreditExhausted(time.Unix(1778810000, 0))
	if !exhausted {
		t.Fatal("CreditExhausted = false, want true")
	}
	if resetAt.Unix() != 1780272000 {
		t.Fatalf("resetAt = %d, want 1780272000", resetAt.Unix())
	}
	if !strings.Contains(msg, "11005.46/11000.00") {
		t.Fatalf("message = %q, want usage/cap", msg)
	}
}

func TestUsageLimitsParsesScientificNextDateReset(t *testing.T) {
	var result UsageLimitsResponse
	err := json.Unmarshal([]byte(`{
		"nextDateReset": 1.780272E9,
		"overageConfiguration": {"overageStatus": "ENABLED"},
		"subscriptionInfo": {
			"subscriptionTitle": "KIRO PRO",
			"type": "Q_DEVELOPER_STANDALONE_PRO"
		},
		"usageBreakdownList": [{
			"resourceType": "CREDIT",
			"currentUsageWithPrecision": 5788.06,
			"usageLimitWithPrecision": 1000.0,
			"overageCapWithPrecision": 10000.0,
			"currency": "USD",
			"nextDateReset": 1.780272E9
		}]
	}`), &result)
	if err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if got := int64(result.NextDateReset); got != 1780272000 {
		t.Fatalf("root nextDateReset = %d, want 1780272000", got)
	}

	summary, ok := result.CreditUsage(time.Unix(1778810000, 0))
	if !ok {
		t.Fatal("CreditUsage() missing CREDIT bucket")
	}
	if got := summary.NextReset.Unix(); got != 1780272000 {
		t.Fatalf("summary.NextReset = %d, want 1780272000", got)
	}
	if summary.Current != 5788.06 {
		t.Fatalf("summary.Current = %.2f, want 5788.06", summary.Current)
	}
}

func TestUsageLimitsCreditNotExhaustedBelowOverageCap(t *testing.T) {
	result := &UsageLimitsResponse{
		UsageBreakdownList: []UsageBreakdown{{
			ResourceType:              "CREDIT",
			CurrentUsageWithPrecision: 10999.99,
			UsageLimitWithPrecision:   1000,
			OverageCapWithPrecision:   10000,
		}},
	}
	result.OverageConfiguration.OverageStatus = "ENABLED"

	exhausted, _, _ := result.CreditExhausted(time.Now())
	if exhausted {
		t.Fatal("CreditExhausted = true, want false")
	}
}

func TestListAvailableModelsURLOmitsEmptyProfileArn(t *testing.T) {
	got := listAvailableModelsURL(DefaultRegion, "")
	if strings.Contains(got, "profileArn=") {
		t.Fatalf("url = %q, want no profileArn query", got)
	}
	if !strings.Contains(got, "origin=KIRO_CLI") {
		t.Fatalf("url = %q, want origin query", got)
	}
}

func TestProfileArnOrDefault(t *testing.T) {
	if got := profileArnOrDefault("", "oidc"); got != DefaultBuilderIDProfileArn {
		t.Fatalf("oidc default profileArn = %q", got)
	}
	if got := profileArnOrDefault(" arn:test ", "oidc"); got != "arn:test" {
		t.Fatalf("explicit profileArn = %q", got)
	}
	if got := profileArnOrDefault("", "social"); got != "" {
		t.Fatalf("social default profileArn = %q", got)
	}
}

func TestRefreshSocialUsesSocialProfileArnOnly(t *testing.T) {
	var capturedURL string
	var capturedBody string
	svc := &KiroAuth{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		capturedURL = req.URL.String()
		body, _ := io.ReadAll(req.Body)
		capturedBody = string(body)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{
				"accessToken":"access-social",
				"refreshToken":"refresh-social-new",
				"expiresIn":3600,
				"profileArn":"arn:aws:codewhisperer:us-east-1:111:profile/social"
			}`)),
			Request: req,
		}, nil
	})}}

	result, err := svc.RefreshToken(context.Background(), &KiroTokenStorage{
		RefreshToken: "refresh-social",
		AuthMethod:   "social",
		Region:       "us-east-1",
	})
	if err != nil {
		t.Fatalf("RefreshToken social failed: %v", err)
	}
	if !strings.Contains(capturedURL, "prod.us-east-1.auth.desktop.kiro.dev/refreshToken") {
		t.Fatalf("social refresh URL = %q", capturedURL)
	}
	if capturedBody != `{"refreshToken":"refresh-social"}` {
		t.Fatalf("social refresh body = %s", capturedBody)
	}
	if result.ProfileArn != "arn:aws:codewhisperer:us-east-1:111:profile/social" {
		t.Fatalf("social profileArn = %q", result.ProfileArn)
	}
	if result.ProfileArn == DefaultBuilderIDProfileArn {
		t.Fatal("social refresh must not use BuilderId default profileArn")
	}
}

func TestRefreshOIDCUsesBuilderIDDefaultWhenMissing(t *testing.T) {
	svc := &KiroAuth{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		if !bytes.Contains(body, []byte(`"grantType":"refresh_token"`)) {
			t.Fatalf("OIDC refresh body missing grantType: %s", string(body))
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{
				"accessToken":"access-oidc",
				"refreshToken":"refresh-oidc-new",
				"expiresIn":3600,
				"tokenType":"Bearer"
			}`)),
			Request: req,
		}, nil
	})}}

	result, err := svc.RefreshToken(context.Background(), &KiroTokenStorage{
		RefreshToken: "refresh-oidc",
		AuthMethod:   "oidc",
		Region:       "us-east-1",
		ClientID:     "client-id",
		ClientSecret: "client-secret",
	})
	if err != nil {
		t.Fatalf("RefreshToken oidc failed: %v", err)
	}
	if result.ProfileArn != DefaultBuilderIDProfileArn {
		t.Fatalf("oidc profileArn = %q", result.ProfileArn)
	}
}

func TestListAvailableModelsURLIncludesQueryParameters(t *testing.T) {
	got := listAvailableModelsURL("us-east-1", "arn:aws:codewhisperer:us-east-1:123:profile/A/B")
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	if parsed.Scheme != "https" || parsed.Host != "q.us-east-1.amazonaws.com" || parsed.Path != "/" {
		t.Fatalf("URL = %q, want q.us-east-1.amazonaws.com root endpoint", got)
	}
	query := parsed.Query()
	if query.Get("origin") != "KIRO_CLI" {
		t.Fatalf("origin query = %q, want KIRO_CLI", query.Get("origin"))
	}
	if query.Get("profileArn") != "arn:aws:codewhisperer:us-east-1:123:profile/A/B" {
		t.Fatalf("profileArn query = %q", query.Get("profileArn"))
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestSendQTelemetryEvent_HeadersAndTarget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := r.Header.Get("x-amz-target")
		if target != "AmazonCodeWhispererService.SendTelemetryEvent" {
			t.Errorf("x-amz-target = %q, want AmazonCodeWhispererService.SendTelemetryEvent", target)
		}
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-tok" {
			t.Errorf("Authorization = %q, want Bearer test-tok", auth)
		}
		ct := r.Header.Get("Content-Type")
		if ct != "application/x-amz-json-1.0" {
			t.Errorf("Content-Type = %q, want application/x-amz-json-1.0", ct)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		} else {
			userContext, _ := body["userContext"].(map[string]any)
			if got := userContext["ideVersion"]; got != "2.3.0" {
				t.Errorf("ideVersion = %v, want 2.3.0", got)
			}
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	// Use SendQTelemetryEventTo to actually hit the test server
	ResetTelemetryClientIDCache()
	defer ResetTelemetryClientIDCache()

	err := SendQTelemetryEventTo(context.Background(), srv.Client(), srv.URL,
		"test-tok", "conv", "model", "arn", 100, 50, nil)
	if err != nil {
		t.Fatalf("SendQTelemetryEventTo failed: %v", err)
	}

	// Also verify the function doesn't error with empty token
	err = SendQTelemetryEvent(context.Background(), http.DefaultClient, "", "conv", "model", "arn", 100, 50, nil)
	if err == nil || !strings.Contains(err.Error(), "access token required") {
		t.Errorf("expected access token error, got: %v", err)
	}
}
