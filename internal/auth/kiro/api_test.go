package kiro

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
		w.WriteHeader(200)
	}))
	defer srv.Close()

	// Direct header verification (SendQTelemetryEvent uses hardcoded URL)
	req, _ := http.NewRequest(http.MethodPost, srv.URL, nil)
	SetRuntimeHeaders(req, "test-tok", "AmazonCodeWhispererService.SendTelemetryEvent")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	// Also verify the function doesn't error with empty token
	err = SendQTelemetryEvent(context.Background(), http.DefaultClient, "", "conv", "model", "arn", 100, 50, nil)
	if err == nil || !strings.Contains(err.Error(), "access token required") {
		t.Errorf("expected access token error, got: %v", err)
	}
}
