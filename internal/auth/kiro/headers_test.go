package kiro

import (
	"net/http"
	"strings"
	"testing"
)

func TestSetStreamingHeaders(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "https://q.us-east-1.amazonaws.com/", nil)
	SetStreamingHeaders(req, "tok123")

	assertHeader(t, req, "x-amz-target", "AmazonCodeWhispererStreamingService.GenerateAssistantResponse")
	assertHeader(t, req, "Content-Type", "application/x-amz-json-1.0")
	assertHeader(t, req, "Authorization", "Bearer tok123")
	assertHeader(t, req, "Accept", "*/*")
	assertHeader(t, req, "Accept-Encoding", "gzip")
	assertHeader(t, req, "amz-sdk-request", "attempt=1; max=3")

	if strings.Contains(req.Header.Get("Content-Type"), "charset") {
		t.Error("Content-Type must not contain charset")
	}
	if req.Header.Get("amz-sdk-invocation-id") == "" {
		t.Error("amz-sdk-invocation-id must be set")
	}

	assertHeaderContains(t, req, "User-Agent", "codewhispererstreaming")
	assertHeaderContains(t, req, "x-amz-user-agent", "codewhispererstreaming")
	assertHeaderContains(t, req, "x-amz-user-agent", "m/F")
}

func TestSetModelsHeaders_MFC(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "https://q.us-east-1.amazonaws.com/", nil)
	SetModelsHeaders(req, "tok456")

	assertHeader(t, req, "x-amz-target", "AmazonCodeWhispererService.ListAvailableModels")
	assertHeader(t, req, "Content-Type", "application/x-amz-json-1.0")
	assertHeader(t, req, "Authorization", "Bearer tok456")

	ua := req.Header.Get("x-amz-user-agent")
	if !strings.Contains(ua, "m/F,C") {
		t.Errorf("x-amz-user-agent must contain m/F,C, got: %s", ua)
	}
}

func TestSetRuntimeHeaders_GetProfile(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "https://q.us-east-1.amazonaws.com/", nil)
	SetRuntimeHeaders(req, "tok789", "AmazonCodeWhispererService.GetProfile")

	assertHeader(t, req, "x-amz-target", "AmazonCodeWhispererService.GetProfile")
	assertHeader(t, req, "Content-Type", "application/x-amz-json-1.0")
	assertHeaderContains(t, req, "User-Agent", "codewhispererruntime")
	assertHeaderContains(t, req, "x-amz-user-agent", "codewhispererruntime")
}

func TestSetRuntimeHeaders_SendTelemetryEvent(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "https://q.us-east-1.amazonaws.com/", nil)
	SetRuntimeHeaders(req, "tokABC", "AmazonCodeWhispererService.SendTelemetryEvent")

	assertHeader(t, req, "x-amz-target", "AmazonCodeWhispererService.SendTelemetryEvent")
	assertHeader(t, req, "Authorization", "Bearer tokABC")
	assertHeaderContains(t, req, "x-amz-user-agent", "m/F")
	if strings.Contains(req.Header.Get("x-amz-user-agent"), "m/F,C") {
		t.Error("runtime headers should use m/F, not m/F,C")
	}
}

func TestKiroOSTag(t *testing.T) {
	tag := KiroOSTag()
	valid := map[string]bool{"windows": true, "linux": true, "macos": true}
	if !valid[tag] {
		t.Errorf("KiroOSTag() = %q, want windows/linux/macos", tag)
	}
}

func TestContentTypeNoCharset_AllHeaders(t *testing.T) {
	setters := map[string]func(*http.Request){
		"Streaming": func(r *http.Request) { SetStreamingHeaders(r, "t") },
		"Models":    func(r *http.Request) { SetModelsHeaders(r, "t") },
		"Runtime":   func(r *http.Request) { SetRuntimeHeaders(r, "t", "X") },
	}
	for name, set := range setters {
		t.Run(name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, "http://x", nil)
			set(req)
			ct := req.Header.Get("Content-Type")
			if ct != "application/x-amz-json-1.0" {
				t.Errorf("Content-Type = %q, want application/x-amz-json-1.0", ct)
			}
		})
	}
}

func TestUserAgentVersionStrings(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "http://x", nil)
	SetStreamingHeaders(req, "t")

	ua := req.Header.Get("User-Agent")
	for _, want := range []string{"1.3.15", "2.3.0", "0.1.14474"} {
		if !strings.Contains(ua, want) {
			t.Errorf("User-Agent missing version %s, got: %s", want, ua)
		}
	}
}

func TestOIDCHeaders(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "https://oidc.us-east-1.amazonaws.com/token", nil)
	SetOIDCHeaders(req)

	assertHeader(t, req, "Accept", "*/*")
	assertHeader(t, req, "Accept-Encoding", "gzip")
	assertHeaderContains(t, req, "User-Agent", "aws-sdk-rust/1.3.15")
	assertHeaderContains(t, req, "x-amz-user-agent", "api/ssooidc/1.100.0")
	assertHeaderContains(t, req, "x-amz-user-agent", "m/E,N")
	if strings.Contains(req.Header.Get("x-amz-user-agent"), "exec-env/") {
		t.Error("OIDC x-amz-user-agent should not contain legacy exec-env marker")
	}
}

func TestCognitoIdentityHeaders(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "https://cognito-identity.us-east-1.amazonaws.com/", nil)
	SetCognitoIdentityHeaders(req)

	assertHeaderContains(t, req, "User-Agent", "aws-sdk-rust/1.3.15")
	assertHeaderContains(t, req, "x-amz-user-agent", "api/cognitoidentity/1.99.0")
	assertHeaderContains(t, req, "x-amz-user-agent", "md/http#hyper-1.x")
	if strings.Contains(req.Header.Get("x-amz-user-agent"), "Version/2.2.2") {
		t.Error("Cognito x-amz-user-agent should not contain legacy 2.2.2 marker")
	}
}

func assertHeader(t *testing.T, req *http.Request, key, want string) {
	t.Helper()
	if got := req.Header.Get(key); got != want {
		t.Errorf("header %s = %q, want %q", key, got, want)
	}
}

func assertHeaderContains(t *testing.T, req *http.Request, key, substr string) {
	t.Helper()
	if got := req.Header.Get(key); !strings.Contains(got, substr) {
		t.Errorf("header %s = %q, want to contain %q", key, got, substr)
	}
}
