package kiro

import (
	"fmt"
	"net/http"
	"runtime"
	"strings"

	"github.com/google/uuid"
)

// KiroOSTag returns the OS tag for Kiro UA headers: windows/linux/macos.
func KiroOSTag() string {
	switch runtime.GOOS {
	case "windows":
		return "windows"
	case "darwin":
		return "macos"
	default:
		return "linux"
	}
}

const (
	kiroCLIAppVersion = "2.3.0"
	kiroSDKVersion    = "1.3.15"
)

// SetCommonQHeaders sets the shared headers for all Q API requests per the Kiro API spec.
// apiTag is "codewhispererstreaming" or "codewhispererruntime".
// suffix is the x-amz-user-agent tail: "m/F" for most, "m/F,C" for ListAvailableModels.
func SetCommonQHeaders(req *http.Request, token, target, apiTag, uaSuffix string) {
	os := KiroOSTag()
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("Authorization", "Bearer "+token)
	if strings.HasPrefix(strings.TrimSpace(token), "ksk_") {
		req.Header.Set("tokenType", "API_KEY")
	}
	req.Header.Set("x-amz-target", target)
	req.Header.Set("x-amzn-codewhisperer-optout", "false")
	req.Header.Set("amz-sdk-invocation-id", uuid.New().String())
	req.Header.Set("amz-sdk-request", "attempt=1; max=3")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("User-Agent",
		fmt.Sprintf("aws-sdk-rust/%s ua/2.1 api/%s/0.1.14474 os/%s lang/rust/1.92.0 md/appVersion-%s app/AmazonQ-For-CLI", kiroSDKVersion, apiTag, os, kiroCLIAppVersion))
	req.Header.Set("x-amz-user-agent",
		fmt.Sprintf("aws-sdk-rust/%s ua/2.1 api/%s/0.1.14474 os/%s lang/rust/1.92.0 %s app/AmazonQ-For-CLI", kiroSDKVersion, apiTag, os, uaSuffix))
}

// SetStreamingHeaders sets headers for GenerateAssistantResponse.
func SetStreamingHeaders(req *http.Request, token string) {
	SetCommonQHeaders(req, token,
		"AmazonCodeWhispererStreamingService.GenerateAssistantResponse",
		"codewhispererstreaming", "m/F")
}

// SetRuntimeHeaders sets headers for runtime APIs (GetProfile, SendTelemetryEvent).
func SetRuntimeHeaders(req *http.Request, token, target string) {
	SetCommonQHeaders(req, token, target, "codewhispererruntime", "m/F")
}

// SetModelsHeaders sets headers for ListAvailableModels (x-amz-user-agent ends with m/F,C).
func SetModelsHeaders(req *http.Request, token string) {
	SetCommonQHeaders(req, token,
		"AmazonCodeWhispererService.ListAvailableModels",
		"codewhispererruntime", "m/F,C")
}
