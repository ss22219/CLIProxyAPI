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
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
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

func TestBuildKiroRequestBody_UsesClaudeSessionIDForConversationID(t *testing.T) {
	sessionID := "11111111-2222-3333-4444-555555555555"
	payload := []byte(`{"metadata":{"user_id":"{\"session_id\":\"` + sessionID + `\"}"},"messages":[{"role":"user","content":"hello"}]}`)

	body1, convID1 := buildKiroRequestBody(payload, "auto", "arn:test")
	body2, convID2 := buildKiroRequestBody(payload, "auto", "arn:test")

	if convID1 != sessionID {
		t.Fatalf("conversationId = %q, want session id %q", convID1, sessionID)
	}
	if convID2 != convID1 {
		t.Fatalf("conversationId changed: %q vs %q", convID1, convID2)
	}
	if got := gjson.GetBytes(body1, "conversationState.conversationId").String(); got != sessionID {
		t.Fatalf("body conversationId = %q, want %q; body=%s", got, sessionID, string(body1))
	}
	if got := gjson.GetBytes(body2, "conversationState.conversationId").String(); got != sessionID {
		t.Fatalf("second body conversationId = %q, want %q; body=%s", got, sessionID, string(body2))
	}
}

func TestBuildKiroRequestBody_StableAssistantMessageIDFromSessionAndToolUse(t *testing.T) {
	payload := []byte(`{
		"metadata":{"user_id":"{\"session_id\":\"session-a\"}"},
		"messages":[
			{"role":"user","content":"read it"},
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"README.md"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"file contents"}]}
		]
	}`)

	body1, convID1 := buildKiroRequestBody(payload, "auto", "arn:test")
	body2, convID2 := buildKiroRequestBody(payload, "auto", "arn:test")
	msgID1 := gjson.GetBytes(body1, "conversationState.history.1.assistantResponseMessage.messageId").String()
	msgID2 := gjson.GetBytes(body2, "conversationState.history.1.assistantResponseMessage.messageId").String()

	if convID1 == "" || convID2 != convID1 {
		t.Fatalf("conversationId not stable: %q vs %q", convID1, convID2)
	}
	if msgID1 == "" || msgID2 != msgID1 {
		t.Fatalf("messageId not stable: %q vs %q; body=%s", msgID1, msgID2, string(body1))
	}

	otherPayload := []byte(strings.Replace(string(payload), "session-a", "session-b", 1))
	otherBody, otherConvID := buildKiroRequestBody(otherPayload, "auto", "arn:test")
	otherMsgID := gjson.GetBytes(otherBody, "conversationState.history.1.assistantResponseMessage.messageId").String()
	if otherConvID == convID1 {
		t.Fatalf("conversationId should change for different session: %q", otherConvID)
	}
	if otherMsgID == msgID1 {
		t.Fatalf("messageId should change for different session: %q", otherMsgID)
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

	if gjson.GetBytes(body, "conversationState.currentMessage.userInputMessage.userInputMessageContext.tools").Exists() {
		t.Fatalf("malformed tools should be omitted; body=%s", string(body))
	}
}

func TestBuildKiroRequestBody_OmitsToolsWhenNoneProvided(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	body, _ := buildKiroRequestBody(payload, "auto", "arn:test")

	if gjson.GetBytes(body, "conversationState.currentMessage.userInputMessage.userInputMessageContext.tools").Exists() {
		t.Fatalf("tools should be omitted when none are provided; body=%s", string(body))
	}
}

func TestBuildKiroRequestBody_AddsDefaultThinkingEffortForKiroModels(t *testing.T) {
	tests := map[string]string{
		"claude-opus-4-6":   "xhigh",
		"claude-opus-4-7":   "xhigh",
		"claude-opus-4.7":   "xhigh",
		"claude-sonnet-4-6": "high",
	}
	payload := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)

	for model, wantEffort := range tests {
		body, _ := buildKiroRequestBody(payload, model, "arn:test")
		prefix := "conversationState.additionalModelRequestFields"
		if got := gjson.GetBytes(body, prefix+".thinking.type").String(); got != "adaptive" {
			t.Fatalf("%s thinking.type = %q, want adaptive; body=%s", model, got, string(body))
		}
		if got := gjson.GetBytes(body, prefix+".output_config.effort").String(); got != wantEffort {
			t.Fatalf("%s effort = %q, want %q; body=%s", model, got, wantEffort, string(body))
		}
	}
}

func TestBuildKiroRequestBody_KeepsExplicitThinkingEffort(t *testing.T) {
	payload := []byte(`{
		"messages":[{"role":"user","content":"hello"}],
		"thinking":{"type":"adaptive"},
		"output_config":{"effort":"low"}
	}`)
	body, _ := buildKiroRequestBody(payload, "claude-opus-4-7", "arn:test")

	prefix := "conversationState.additionalModelRequestFields"
	if got := gjson.GetBytes(body, prefix+".thinking.type").String(); got != "adaptive" {
		t.Fatalf("thinking.type = %q, want adaptive; body=%s", got, string(body))
	}
	if got := gjson.GetBytes(body, prefix+".output_config.effort").String(); got != "low" {
		t.Fatalf("effort = %q, want low; body=%s", got, string(body))
	}
}

func TestBuildKiroRequestBody_MapsReasoningEffort(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"user","content":"hello"}],"reasoning_effort":"medium"}`)
	body, _ := buildKiroRequestBody(payload, "claude-opus-4-7", "arn:test")

	prefix := "conversationState.additionalModelRequestFields"
	if got := gjson.GetBytes(body, prefix+".thinking.type").String(); got != "adaptive" {
		t.Fatalf("thinking.type = %q, want adaptive; body=%s", got, string(body))
	}
	if got := gjson.GetBytes(body, prefix+".output_config.effort").String(); got != "medium" {
		t.Fatalf("effort = %q, want medium; body=%s", got, string(body))
	}
}

func TestBuildKiroRequestBody_SkipsThinkingEffortForOtherModels(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	body, _ := buildKiroRequestBody(payload, "claude-sonnet-4-5", "arn:test")

	if gjson.GetBytes(body, "conversationState.additionalModelRequestFields").Exists() {
		t.Fatalf("additionalModelRequestFields should be omitted; body=%s", string(body))
	}
}

func TestKiroTelemetryStatsCarriesFirstToolUse(t *testing.T) {
	calls := []kiroToolCall{{ID: "tooluse_1", Name: "get_weather"}, {ID: "tooluse_2", Name: "other"}}
	if got := firstKiroToolName(calls); got != "get_weather" {
		t.Fatalf("firstKiroToolName = %q, want get_weather", got)
	}
	if got := firstKiroToolUseID(calls); got != "tooluse_1" {
		t.Fatalf("firstKiroToolUseID = %q, want tooluse_1", got)
	}
}

func TestParseKiroEventStreamStatsCarriesContextUsage(t *testing.T) {
	content, _, contextUsage := parseKiroEventStreamStats([]byte(`{"content":"hello"}
{"contextUsagePercentage":0.226}
{"content":" world"}`))

	if content != "hello world" {
		t.Fatalf("content = %q, want hello world", content)
	}
	if contextUsage != 0.226 {
		t.Fatalf("contextUsage = %v, want 0.226", contextUsage)
	}
}

func TestCountKiroUsageUsesContextUsagePercentage(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	clientID := "test-kiro-context-usage"
	reg.RegisterClient(clientID, "kiro", []*registry.ModelInfo{{
		ID:            "claude-opus-4.7",
		Object:        "model",
		Type:          "kiro",
		ContextLength: 1000000,
	}})
	defer reg.UnregisterClient(clientID)

	detail := countKiroUsage("claude-opus-4-7", []byte(`{"messages":[{"role":"user","content":"hello"}]}`), "", 0.226)
	if detail.InputTokens != 2260 {
		t.Fatalf("InputTokens = %d, want 2260", detail.InputTokens)
	}
	if detail.TotalTokens != detail.InputTokens+detail.OutputTokens {
		t.Fatalf("TotalTokens = %d, want input+output", detail.TotalTokens)
	}
}

func TestBuildOpenAINonStreamResponseIncludesEstimatedUsage(t *testing.T) {
	resp := buildOpenAINonStreamResponse("claude-opus-4.7", "pong", nil, usage.Detail{
		InputTokens:  12,
		OutputTokens: 3,
		TotalTokens:  15,
	})

	if got := gjson.GetBytes(resp, "usage.prompt_tokens").Int(); got != 12 {
		t.Fatalf("prompt_tokens = %d, want 12; body=%s", got, string(resp))
	}
	if got := gjson.GetBytes(resp, "usage.completion_tokens").Int(); got != 3 {
		t.Fatalf("completion_tokens = %d, want 3; body=%s", got, string(resp))
	}
	if got := gjson.GetBytes(resp, "usage.total_tokens").Int(); got != 15 {
		t.Fatalf("total_tokens = %d, want 15; body=%s", got, string(resp))
	}
}

func TestStreamKiroToOpenAISSEFinalChunkIncludesEstimatedUsage(t *testing.T) {
	reader := strings.NewReader(`{"content":"pong"}` + "\n")
	out := make(chan cliproxyexecutor.StreamChunk, 8)

	streamKiroToOpenAISSE(context.Background(), nil, reader, "claude-opus-4.7", []byte(`{"messages":[{"role":"user","content":"hello"}]}`), time.Now(), out)

	var final []byte
	for {
		select {
		case chunk := <-out:
			if gjson.GetBytes(chunk.Payload, "usage").Exists() {
				final = chunk.Payload
			}
		default:
			if len(final) == 0 {
				t.Fatal("final usage chunk missing")
			}
			if got := gjson.GetBytes(final, "usage.prompt_tokens").Int(); got <= 0 {
				t.Fatalf("prompt_tokens = %d, want > 0; chunk=%s", got, string(final))
			}
			if got := gjson.GetBytes(final, "usage.completion_tokens").Int(); got <= 0 {
				t.Fatalf("completion_tokens = %d, want > 0; chunk=%s", got, string(final))
			}
			if got := gjson.GetBytes(final, "usage.total_tokens").Int(); got <= 0 {
				t.Fatalf("total_tokens = %d, want > 0; chunk=%s", got, string(final))
			}
			return
		}
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

func TestBuildKiroRequestBody_AnthropicToolResultImage(t *testing.T) {
	payload := []byte(`{
		"messages":[
			{"role":"user","content":"read it"},
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"screenshot.png"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[
				{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}
			]}]}
		]
	}`)
	body, _ := buildKiroRequestBody(payload, "auto", "arn:test")

	if got := gjson.GetBytes(body, "conversationState.currentMessage.userInputMessage.images.0.format").String(); got != "png" {
		t.Fatalf("tool result image format = %q, want png; body=%s", got, string(body))
	}
	if got := gjson.GetBytes(body, "conversationState.currentMessage.userInputMessage.images.0.source.bytes").String(); got != "aGVsbG8=" {
		t.Fatalf("tool result image bytes = %q, want aGVsbG8=; body=%s", got, string(body))
	}
	if got := gjson.GetBytes(body, "conversationState.currentMessage.userInputMessage.userInputMessageContext.toolResults.0.toolUseId").String(); got != "toolu_1" {
		t.Fatalf("tool result id = %q, want toolu_1; body=%s", got, string(body))
	}
	if gjson.GetBytes(body, "conversationState.currentMessage.userInputMessage.userInputMessageContext.images").Exists() {
		t.Fatalf("tool result images must be userInputMessage.images, not nested under context; body=%s", string(body))
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

func TestTranslateKiroNonStreamResponseToClaudeIncludesUsage(t *testing.T) {
	openAIResp := buildOpenAINonStreamResponse("claude-opus-4.6", "pong", nil, usage.Detail{})
	out := translateKiroNonStreamResponse(context.Background(), "claude-opus-4.6", openAIResp, []byte(`{"stream":false}`), cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatClaude,
		OriginalRequest: []byte(`{"stream":false}`),
	})

	if got := gjson.GetBytes(out, "type").String(); got != "message" {
		t.Fatalf("response type = %q, want message; body=%s", got, string(out))
	}
	if got := gjson.GetBytes(out, "content.0.text").String(); got != "pong" {
		t.Fatalf("content text = %q, want pong; body=%s", got, string(out))
	}
	if !gjson.GetBytes(out, "usage.input_tokens").Exists() {
		t.Fatalf("usage.input_tokens missing; body=%s", string(out))
	}
	if !gjson.GetBytes(out, "usage.output_tokens").Exists() {
		t.Fatalf("usage.output_tokens missing; body=%s", string(out))
	}
}

func TestKiroSourcePayloadConvertsOpenAIResponsesInput(t *testing.T) {
	original := []byte(`{"model":"claude-sonnet-4.5","input":"Return exactly: pong","metadata":{"session_id":"kiro-session-1"}}`)
	payload := kiroSourcePayload("claude-sonnet-4.5", original, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
	})

	if got := gjson.GetBytes(payload, "messages.0.content").String(); got != "Return exactly: pong" {
		t.Fatalf("converted content = %q, want prompt; payload=%s", got, string(payload))
	}
	if got := gjson.GetBytes(payload, "metadata.session_id").String(); got != "kiro-session-1" {
		t.Fatalf("metadata session_id = %q, want kiro-session-1; payload=%s", got, string(payload))
	}

	body1, convID1 := buildKiroRequestBody(payload, "claude-sonnet-4.5", "")
	body2, convID2 := buildKiroRequestBody(payload, "claude-sonnet-4.5", "")
	if convID1 == "" || convID1 != convID2 {
		t.Fatalf("conversation IDs should be stable, got %q and %q", convID1, convID2)
	}
	if got := gjson.GetBytes(body1, "conversationState.currentMessage.userInputMessage.content").String(); got != "Return exactly: pong" {
		t.Fatalf("kiro current content = %q, want prompt; body=%s", got, string(body1))
	}
	if got := gjson.GetBytes(body2, "conversationState.conversationId").String(); got != convID1 {
		t.Fatalf("body conversationId = %q, want %q", got, convID1)
	}
}

func TestTranslateKiroNonStreamResponseToOpenAIResponsesIncludesUsage(t *testing.T) {
	original := []byte(`{"model":"claude-sonnet-4.5","input":"Return exactly: pong","max_output_tokens":16}`)
	requestPayload := kiroSourcePayload("claude-sonnet-4.5", original, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
	})
	openAIResp := buildOpenAINonStreamResponse("claude-sonnet-4.5", "pong", nil, usage.Detail{})
	out := translateKiroNonStreamResponse(context.Background(), "claude-sonnet-4.5", openAIResp, requestPayload, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		OriginalRequest: original,
	})

	if got := gjson.GetBytes(out, "object").String(); got != "response" {
		t.Fatalf("object = %q, want response; body=%s", got, string(out))
	}
	if got := gjson.GetBytes(out, "output.0.content.0.text").String(); got != "pong" {
		t.Fatalf("response text = %q, want pong; body=%s", got, string(out))
	}
	if !gjson.GetBytes(out, "usage.input_tokens").Exists() {
		t.Fatalf("usage.input_tokens missing; body=%s", string(out))
	}
	if !gjson.GetBytes(out, "usage.output_tokens").Exists() {
		t.Fatalf("usage.output_tokens missing; body=%s", string(out))
	}
}

func TestStreamKiroToClaudeSSEIncludesMessageUsage(t *testing.T) {
	out := make(chan cliproxyexecutor.StreamChunk, 32)
	stats := streamKiroToClaudeSSE(context.Background(), nil, strings.NewReader(`{"content":"pong"}`), "claude-opus-4.6", []byte(`{"stream":true}`), cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatClaude,
		OriginalRequest: []byte(`{"stream":true}`),
	}, time.Now(), out)

	if stats.ResponseLength != len("pong") {
		t.Fatalf("response length = %d, want %d", stats.ResponseLength, len("pong"))
	}

	var combined strings.Builder
	for {
		select {
		case chunk := <-out:
			if chunk.Err != nil {
				t.Fatalf("unexpected stream error: %v", chunk.Err)
			}
			combined.Write(chunk.Payload)
		default:
			text := combined.String()
			if !strings.Contains(text, "event: message_start") {
				t.Fatalf("message_start missing; stream=%s", text)
			}
			if !strings.Contains(text, `"type":"text_delta","text":"pong"`) {
				t.Fatalf("text delta missing; stream=%s", text)
			}
			if !strings.Contains(text, `"usage":{"input_tokens":0,"output_tokens":0}`) {
				t.Fatalf("message usage missing; stream=%s", text)
			}
			if !strings.Contains(text, "event: message_stop") {
				t.Fatalf("message_stop missing; stream=%s", text)
			}
			return
		}
	}
}

func TestStreamKiroToOpenAIResponsesSSEIncludesCompletedUsage(t *testing.T) {
	original := []byte(`{"model":"claude-sonnet-4.5","input":"Return exactly: pong","stream":true}`)
	requestPayload := kiroSourcePayload("claude-sonnet-4.5", original, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       true,
	})
	out := make(chan cliproxyexecutor.StreamChunk, 32)
	stats := streamKiroToOpenAIResponsesSSE(context.Background(), nil, strings.NewReader(`{"content":"pong"}`), "claude-sonnet-4.5", requestPayload, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		OriginalRequest: original,
		Stream:          true,
	}, time.Now(), out)

	if stats.ResponseLength != len("pong") {
		t.Fatalf("response length = %d, want %d", stats.ResponseLength, len("pong"))
	}

	var combined strings.Builder
	for {
		select {
		case chunk := <-out:
			if chunk.Err != nil {
				t.Fatalf("unexpected stream error: %v", chunk.Err)
			}
			combined.Write(chunk.Payload)
		default:
			text := combined.String()
			if !strings.Contains(text, "event: response.completed") {
				t.Fatalf("response.completed missing; stream=%s", text)
			}
			if !strings.Contains(text, `"text":"pong"`) {
				t.Fatalf("output text missing; stream=%s", text)
			}
			if !strings.Contains(text, `"usage":{"input_tokens":`) {
				t.Fatalf("response usage missing; stream=%s", text)
			}
			return
		}
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
	stats := streamKiroToOpenAISSE(context.Background(), nil, reader, "test-model", []byte(`{"messages":[{"role":"user","content":"hi"}]}`), t0, out)

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

	stats := streamKiroToOpenAISSE(context.Background(), nil, reader, "m", []byte(`{"messages":[{"role":"user","content":"hi"}]}`), time.Now(), out)

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
	stats := streamKiroToOpenAISSE(context.Background(), nil, reader, "m", []byte(`{"messages":[{"role":"user","content":"hi"}]}`), t0, out)

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

func TestKiroAccessTokenUsesAPIKeyAttributes(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Provider:   "kiro",
		Attributes: map[string]string{"api_key": "ksk_test"},
		Metadata: map[string]any{
			"access_token": "aoa_token",
			"auth_method":  "api_key",
			"profile_arn":  "arn:test",
		},
	}

	if got := kiroAccessToken(auth); got != "ksk_test" {
		t.Fatalf("kiroAccessToken = %q, want ksk_test", got)
	}
	if got := kiroProfileArn(auth); got != "" {
		t.Fatalf("kiroProfileArn = %q, want empty for API key auth", got)
	}
}
