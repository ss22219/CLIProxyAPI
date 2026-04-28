package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	kiroauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/kiro"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

func TestBuildKiroRequestBody_ReturnsConversationID(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	body, convID := buildKiroRequestBody(payload, "auto", "arn:test")
	if convID == "" {
		t.Fatal("expected non-empty conversationId")
	}
	bodyConvID := gjson.GetBytes(body, "conversationState.conversationId").String()
	if bodyConvID != convID {
		t.Errorf("body conversationId %q != returned %q", bodyConvID, convID)
	}
}

func TestBuildKiroRequestBody_CurrentImagesAreUserInputMessageField(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":"data:image/png;base64,aGVsbG8="}}]}]}`)
	body, _ := buildKiroRequestBody(payload, "auto", "arn:test")

	if got := gjson.GetBytes(body, "conversationState.currentMessage.userInputMessage.images.0.source.bytes").String(); got != "aGVsbG8=" {
		t.Fatalf("current image bytes = %q, want aGVsbG8=; body=%s", got, string(body))
	}
	if gjson.GetBytes(body, "conversationState.currentMessage.userInputMessage.userInputMessageContext.images").Exists() {
		t.Fatalf("images must not be nested under userInputMessageContext; body=%s", string(body))
	}
}

func TestBuildKiroRequestBody_AnthropicToolSchema(t *testing.T) {
	payload := []byte(`{
		"tools":[{
			"name":"Read",
			"description":"Read a file",
			"input_schema":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}
		}],
		"messages":[{"role":"user","content":"hello"}]
	}`)
	body, _ := buildKiroRequestBody(payload, "auto", "arn:test")

	prefix := "conversationState.currentMessage.userInputMessage.userInputMessageContext.tools.0.toolSpecification"
	if got := gjson.GetBytes(body, prefix+".name").String(); got != "Read" {
		t.Fatalf("tool name = %q, want Read; body=%s", got, string(body))
	}
	if got := gjson.GetBytes(body, prefix+".inputSchema.json.properties.path.type").String(); got != "string" {
		t.Fatalf("tool schema path type = %q, want string; body=%s", got, string(body))
	}
}

func TestBuildKiroRequestBody_SkipsMalformedTools(t *testing.T) {
	payload := []byte(`{"tools":[{"description":"missing name","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hello"}]}`)
	body, _ := buildKiroRequestBody(payload, "auto", "arn:test")

	if got := gjson.GetBytes(body, "conversationState.currentMessage.userInputMessage.userInputMessageContext.tools.0.toolSpecification.name").String(); got != "no_tool_available" {
		t.Fatalf("tool name = %q, want no_tool_available; body=%s", got, string(body))
	}
}

func TestBuildKiroRequestBody_AnthropicToolUseAndResult(t *testing.T) {
	payload := []byte(`{
		"messages":[
			{"role":"user","content":"read it"},
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"README.md"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"file contents"}]}
		]
	}`)
	body, _ := buildKiroRequestBody(payload, "auto", "arn:test")

	if got := gjson.GetBytes(body, "conversationState.history.1.assistantResponseMessage.toolUses.0.name").String(); got != "Read" {
		t.Fatalf("tool use name = %q, want Read; body=%s", got, string(body))
	}
	if got := gjson.GetBytes(body, "conversationState.history.1.assistantResponseMessage.toolUses.0.input.path").String(); got != "README.md" {
		t.Fatalf("tool use input path = %q, want README.md; body=%s", got, string(body))
	}
	if got := gjson.GetBytes(body, "conversationState.currentMessage.userInputMessage.userInputMessageContext.toolResults.0.toolUseId").String(); got != "toolu_1" {
		t.Fatalf("tool result id = %q, want toolu_1; body=%s", got, string(body))
	}
	if got := gjson.GetBytes(body, "conversationState.currentMessage.userInputMessage.userInputMessageContext.toolResults.0.content.0.text").String(); got != "file contents" {
		t.Fatalf("tool result text = %q, want file contents; body=%s", got, string(body))
	}
	if got := gjson.GetBytes(body, "conversationState.currentMessage.userInputMessage.content").String(); got != "" {
		t.Fatalf("current content = %q, want empty for tool result; body=%s", got, string(body))
	}
}

func TestBuildKiroRequestBody_AnthropicSystemArrayAndImage(t *testing.T) {
	payload := []byte(`{
		"system":[{"type":"text","text":"alpha"},{"type":"text","text":" beta","cache_control":{"type":"ephemeral"}}],
		"messages":[{"role":"user","content":[
			{"type":"text","text":"describe"},
			{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"YWJj"}}
		]}]
	}`)
	body, _ := buildKiroRequestBody(payload, "auto", "arn:test")

	if got := gjson.GetBytes(body, "conversationState.currentMessage.userInputMessage.content").String(); !strings.HasPrefix(got, "alpha beta\n\ndescribe") {
		t.Fatalf("current content = %q, want system text prepended; body=%s", got, string(body))
	}
	if got := gjson.GetBytes(body, "conversationState.currentMessage.userInputMessage.images.0.format").String(); got != "jpeg" {
		t.Fatalf("image format = %q, want jpeg; body=%s", got, string(body))
	}
	if got := gjson.GetBytes(body, "conversationState.currentMessage.userInputMessage.images.0.source.bytes").String(); got != "YWJj" {
		t.Fatalf("image bytes = %q, want YWJj; body=%s", got, string(body))
	}
}

func TestKiroMetadataStorageReadsAndUpdatesNestedToken(t *testing.T) {
	metadata := map[string]any{
		"auth_method": "sso",
		"idc_region":  "ap-southeast-1",
		"token": map[string]any{
			"access_token":  "old-access",
			"refresh_token": "nested-refresh",
			"expires_at":    "2026-04-28T10:20:57Z",
		},
	}

	storage := metadataToKiroStorage(metadata)
	if storage.RefreshToken != "nested-refresh" {
		t.Fatalf("RefreshToken = %q, want nested-refresh", storage.RefreshToken)
	}
	if storage.AuthMethod != "oidc" {
		t.Fatalf("AuthMethod = %q, want oidc", storage.AuthMethod)
	}
	if storage.AccessToken != "old-access" {
		t.Fatalf("AccessToken = %q, want old-access", storage.AccessToken)
	}
	if storage.Region != "ap-southeast-1" {
		t.Fatalf("Region = %q, want ap-southeast-1", storage.Region)
	}

	updateNestedKiroToken(metadata, &kiroauth.KiroTokenStorage{
		AccessToken:  "new-access",
		RefreshToken: "new-refresh",
		ExpiresAt:    "2026-04-28T11:20:57Z",
	})

	tokenMap, ok := metadata["token"].(map[string]any)
	if !ok {
		t.Fatal("metadata token map missing")
	}
	if tokenMap["access_token"] != "new-access" || tokenMap["refresh_token"] != "new-refresh" || tokenMap["expires_at"] != "2026-04-28T11:20:57Z" {
		t.Fatalf("nested token not updated: %#v", tokenMap)
	}
}

func TestShouldRefreshKiroAuthUsesNestedTokenExpiry(t *testing.T) {
	now := time.Date(2026, time.April, 28, 11, 0, 0, 0, time.UTC)
	auth := &cliproxyauth.Auth{
		Provider: "kiro",
		Metadata: map[string]any{
			"token": map[string]any{
				"expires_at": now.Add(4 * time.Minute).Format(time.RFC3339),
			},
		},
	}

	if !shouldRefreshKiroAuth(auth, now) {
		t.Fatal("expected nested expiry inside refresh lead to require refresh")
	}

	auth.Metadata["token"].(map[string]any)["expires_at"] = now.Add(10 * time.Minute).Format(time.RFC3339)
	if shouldRefreshKiroAuth(auth, now) {
		t.Fatal("did not expect refresh when nested expiry is outside refresh lead")
	}
}

func TestSendQTelemetryEvent_UsesStableClientID(t *testing.T) {
	kiroauth.ResetTelemetryClientIDCache()
	defer kiroauth.ResetTelemetryClientIDCache()

	id1 := kiroauth.GetTelemetryClientID()
	id2 := kiroauth.GetTelemetryClientID()
	if id1 != id2 {
		t.Errorf("clientId not stable: %q vs %q", id1, id2)
	}
	if id1 == "" {
		t.Error("clientId should not be empty")
	}
}

// TestSendQTelemetryEventTo_BodyAndHeaders verifies the telemetry HTTP request
// reaches the test server with correct target, headers, conversationId, clientId,
// responseLength, and timeBetweenChunks as an array.
func TestSendQTelemetryEventTo_BodyAndHeaders(t *testing.T) {
	kiroauth.ResetTelemetryClientIDCache()
	defer kiroauth.ResetTelemetryClientIDCache()

	var captured []byte
	var capturedTarget string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedTarget = r.Header.Get("x-amz-target")
		captured, _ = io.ReadAll(r.Body)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	chunks := []float64{0.123, 0.456}
	err := kiroauth.SendQTelemetryEventTo(
		context.Background(), srv.Client(), srv.URL,
		"test-token", "conv-123", "auto", "arn:test",
		42, 150.5, chunks,
	)
	if err != nil {
		t.Fatalf("SendQTelemetryEventTo failed: %v", err)
	}

	if capturedTarget != "AmazonCodeWhispererService.SendTelemetryEvent" {
		t.Errorf("x-amz-target = %q, want SendTelemetryEvent", capturedTarget)
	}

	evt := gjson.GetBytes(captured, "telemetryEvent.chatAddMessageEvent")
	if evt.Get("conversationId").String() != "conv-123" {
		t.Errorf("conversationId = %q, want conv-123", evt.Get("conversationId").String())
	}
	if evt.Get("responseLength").Int() != 42 {
		t.Errorf("responseLength = %d, want 42", evt.Get("responseLength").Int())
	}
	if evt.Get("timeToFirstChunkMilliseconds").Float() != 150.5 {
		t.Errorf("ttfc = %f, want 150.5", evt.Get("timeToFirstChunkMilliseconds").Float())
	}
	// timeBetweenChunks must be an array
	tbc := evt.Get("timeBetweenChunks")
	if !tbc.IsArray() {
		t.Fatalf("timeBetweenChunks should be array, got: %s", tbc.Raw)
	}
	arr := tbc.Array()
	if len(arr) != 2 || arr[0].Float() != 0.123 || arr[1].Float() != 0.456 {
		t.Errorf("timeBetweenChunks = %v, want [0.123, 0.456]", tbc.Raw)
	}

	// clientId must be non-empty
	cid := gjson.GetBytes(captured, "userContext.clientId").String()
	if cid == "" {
		t.Error("clientId should not be empty")
	}
}

// TestSendQTelemetryEventTo_NilChunksBecomesEmptyArray verifies nil timeBetweenChunks
// is serialized as [] not null.
func TestSendQTelemetryEventTo_NilChunksBecomesEmptyArray(t *testing.T) {
	kiroauth.ResetTelemetryClientIDCache()
	defer kiroauth.ResetTelemetryClientIDCache()

	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	err := kiroauth.SendQTelemetryEventTo(
		context.Background(), srv.Client(), srv.URL,
		"test-token", "conv-1", "auto", "",
		0, 0, nil, // nil timeBetweenChunks
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tbc := gjson.GetBytes(captured, "telemetryEvent.chatAddMessageEvent.timeBetweenChunks")
	if !tbc.IsArray() {
		t.Fatalf("timeBetweenChunks should be array, got: %s", tbc.Raw)
	}
	if len(tbc.Array()) != 0 {
		t.Errorf("timeBetweenChunks should be empty array, got: %s", tbc.Raw)
	}
}

// TestStreamKiroToOpenAISSE_CollectsTimingStats verifies that streaming collects
// responseLength, ttfc, and timeBetweenChunks from content events.
func TestStreamKiroToOpenAISSE_CollectsTimingStats(t *testing.T) {
	eventData := `{"content":"Hello"}` + "\n" + `{"content":" World"}` + "\n" + `{"content":"!"}` + "\n"
	reader := strings.NewReader(eventData)
	out := make(chan cliproxyexecutor.StreamChunk, 100)

	t0 := time.Now()
	stats := streamKiroToOpenAISSE(context.Background(), nil, reader, "test-model", t0, out)

	// "Hello" + " World" + "!" = 12
	if stats.ResponseLength != 12 {
		t.Errorf("ResponseLength = %d, want 12", stats.ResponseLength)
	}
	// TTFC should be >= 0 (measured from t0)
	if stats.TTFCMs < 0 {
		t.Errorf("TTFCMs = %f, should be >= 0", stats.TTFCMs)
	}
	// 3 content events → 2 timeBetweenChunks entries
	if len(stats.TimeBetweenChunks) != 2 {
		t.Errorf("TimeBetweenChunks length = %d, want 2", len(stats.TimeBetweenChunks))
	}
	for i, v := range stats.TimeBetweenChunks {
		if v < 0 {
			t.Errorf("TimeBetweenChunks[%d] = %f, should be >= 0", i, v)
		}
	}
}

// TestStreamKiroToOpenAISSE_SingleContent_EmptyChunks verifies that a single content
// event produces empty (not nil) timeBetweenChunks.
func TestStreamKiroToOpenAISSE_SingleContent_EmptyChunks(t *testing.T) {
	reader := strings.NewReader(`{"content":"Hi"}` + "\n")
	out := make(chan cliproxyexecutor.StreamChunk, 100)

	stats := streamKiroToOpenAISSE(context.Background(), nil, reader, "m", time.Now(), out)

	if stats.ResponseLength != 2 {
		t.Errorf("ResponseLength = %d, want 2", stats.ResponseLength)
	}
	if stats.TimeBetweenChunks == nil {
		t.Fatal("TimeBetweenChunks should not be nil")
	}
	if len(stats.TimeBetweenChunks) != 0 {
		t.Errorf("TimeBetweenChunks length = %d, want 0", len(stats.TimeBetweenChunks))
	}
}

// capturedTelemetry holds data captured by the mock telemetry sender.
type capturedTelemetry struct {
	mu              sync.Mutex
	calls           []telemetryStats
	conversationIDs []string
}

func (c *capturedTelemetry) sender(token, conversationID, modelID, profileArn string, stats telemetryStats) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, stats)
	c.conversationIDs = append(c.conversationIDs, conversationID)
}

// TestExecute_FiresTelemetryWithStats verifies that Execute() calls the telemetry
// sender with correct conversationId, responseLength, and empty timeBetweenChunks.
func TestExecute_FiresTelemetryWithStats(t *testing.T) {
	// Mock Kiro API server
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"content":"Hello!"}`))
	}))
	defer srv.Close()

	// Inject mock telemetry sender
	cap := &capturedTelemetry{}
	origSender := telemetrySender
	// Use synchronous sender so we can assert immediately
	telemetrySender = cap.sender
	defer func() { telemetrySender = origSender }()

	payload := []byte(`{"messages":[{"role":"user","content":"hi"}],"model":"auto"}`)
	body, convID := buildKiroRequestBody(payload, "auto", "arn:test")
	if convID == "" {
		t.Fatal("convID empty")
	}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, strings.NewReader(string(body)))
	kiroauth.SetStreamingHeaders(req, "tok")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	content, _ := parseKiroEventStream(respBody)
	if content != "Hello!" {
		t.Errorf("content = %q, want Hello!", content)
	}

	// Simulate what Execute does: fire telemetry
	fireQTelemetry("tok", convID, "auto", "arn:test", telemetryStats{
		ResponseLength:    len(content),
		TTFCMs:            100.0,
		TimeBetweenChunks: []float64{},
	})

	cap.mu.Lock()
	defer cap.mu.Unlock()
	if len(cap.calls) != 1 {
		t.Fatalf("expected 1 telemetry call, got %d", len(cap.calls))
	}
	if cap.calls[0].ResponseLength != 6 {
		t.Errorf("ResponseLength = %d, want 6", cap.calls[0].ResponseLength)
	}
	if cap.calls[0].TimeBetweenChunks == nil {
		t.Error("TimeBetweenChunks should not be nil")
	}
	if len(cap.calls[0].TimeBetweenChunks) != 0 {
		t.Error("TimeBetweenChunks should be empty for non-streaming")
	}
	if cap.conversationIDs[0] != convID {
		t.Errorf("conversationId = %q, want %q", cap.conversationIDs[0], convID)
	}
}

// TestExecuteStream_FiresTelemetryWithChunkTiming verifies that the streaming path
// collects real timeBetweenChunks and fires telemetry with them.
func TestExecuteStream_FiresTelemetryWithChunkTiming(t *testing.T) {
	cap := &capturedTelemetry{}
	origSender := telemetrySender
	telemetrySender = cap.sender
	defer func() { telemetrySender = origSender }()

	// Simulate streaming: 3 content events
	eventData := `{"content":"A"}` + "\n" + `{"content":"B"}` + "\n" + `{"content":"C"}` + "\n"
	reader := strings.NewReader(eventData)
	out := make(chan cliproxyexecutor.StreamChunk, 100)

	t0 := time.Now()
	stats := streamKiroToOpenAISSE(context.Background(), nil, reader, "m", t0, out)

	// Fire telemetry like ExecuteStream does
	fireQTelemetry("tok", "conv-stream", "auto", "", stats)

	cap.mu.Lock()
	defer cap.mu.Unlock()
	if len(cap.calls) != 1 {
		t.Fatalf("expected 1 telemetry call, got %d", len(cap.calls))
	}
	s := cap.calls[0]
	if s.ResponseLength != 3 { // "A" + "B" + "C"
		t.Errorf("ResponseLength = %d, want 3", s.ResponseLength)
	}
	if s.TTFCMs < 0 {
		t.Errorf("TTFCMs = %f, should be >= 0", s.TTFCMs)
	}
	// 3 content events → 2 timeBetweenChunks
	if len(s.TimeBetweenChunks) != 2 {
		t.Errorf("TimeBetweenChunks length = %d, want 2", len(s.TimeBetweenChunks))
	}
	if cap.conversationIDs[0] != "conv-stream" {
		t.Errorf("conversationId = %q, want conv-stream", cap.conversationIDs[0])
	}
}

func TestFireQTelemetry_NonFatalOnError(t *testing.T) {
	// fireQTelemetry should not panic even with invalid token
	fireQTelemetry("", "conv-id", "auto", "", telemetryStats{
		ResponseLength:    10,
		TTFCMs:            100.0,
		TimeBetweenChunks: []float64{},
	})
}

func TestTelemetryExportsAvailable(t *testing.T) {
	_ = kiroauth.SendQTelemetryEvent
	_ = kiroauth.SendQTelemetryEventTo
	_ = kiroauth.GetTelemetryClientID
	_ = kiroauth.ResetTelemetryClientIDCache

	auth := &cliproxyauth.Auth{
		Metadata: map[string]any{
			"access_token": "test",
			"profile_arn":  "arn:test",
		},
	}
	token := kiroAccessToken(auth)
	if token != "test" {
		t.Errorf("kiroAccessToken = %q, want test", token)
	}
	arn := kiroProfileArn(auth)
	if arn != "arn:test" {
		t.Errorf("kiroProfileArn = %q, want arn:test", arn)
	}
}
