package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	kiroauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	kiroBaseURL   = "https://q.us-east-1.amazonaws.com/"
	kiroAmzTarget = "AmazonCodeWhispererStreamingService.GenerateAssistantResponse"
)

// Execute sends a non-streaming request to the Kiro API, translating between OpenAI and Kiro formats.
func (e *KiroExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	auth, err = e.ensureFreshKiroAuth(ctx, auth)
	if err != nil {
		return resp, err
	}
	token := kiroAccessToken(auth)
	if token == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "kiro: missing access token"}
		return
	}

	profileArn := kiroProfileArn(auth)
	kiroBody, conversationID := buildKiroRequestBody(req.Payload, baseModel, profileArn)
	httpReq, err := newKiroHTTPRequest(ctx, token, kiroBody)
	if err != nil {
		return resp, err
	}

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL: kiroBaseURL, Method: http.MethodPost, Headers: httpReq.Header.Clone(),
		Body: kiroBody, Provider: "kiro",
		AuthID: authID, AuthLabel: authLabel, AuthType: authType, AuthValue: authValue,
	})

	t0 := time.Now()
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() { _ = httpResp.Body.Close() }()
	ttfc := float64(time.Since(t0).Milliseconds())

	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	body, _ := io.ReadAll(httpResp.Body)
	helps.AppendAPIResponseChunk(ctx, e.cfg, body)

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		err = statusErr{code: httpResp.StatusCode, msg: string(body)}
		return resp, err
	}

	content, toolCalls := parseKiroEventStream(body)
	openAIResp := buildOpenAINonStreamResponse(req.Model, content, toolCalls)
	reporter.EnsurePublished(ctx)
	resp = cliproxyexecutor.Response{Payload: openAIResp, Headers: httpResp.Header.Clone()}

	// Fire-and-forget Q API telemetry (non-streaming: all events parsed at once, timeBetweenChunks is empty)
	go fireQTelemetry(token, conversationID, baseModel, profileArn, telemetryStats{
		ResponseLength:    len(content),
		TTFCMs:            ttfc,
		TimeBetweenChunks: []float64{},
	})

	return resp, nil
}

// ExecuteStream sends a streaming request to the Kiro API and converts the response to OpenAI SSE.
func (e *KiroExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	auth, err = e.ensureFreshKiroAuth(ctx, auth)
	if err != nil {
		return nil, err
	}
	token := kiroAccessToken(auth)
	if token == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "kiro: missing access token"}
		return nil, err
	}

	profileArn := kiroProfileArn(auth)
	kiroBody, conversationID := buildKiroRequestBody(req.Payload, baseModel, profileArn)
	httpReq, err := newKiroHTTPRequest(ctx, token, kiroBody)
	if err != nil {
		return nil, err
	}

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL: kiroBaseURL, Method: http.MethodPost, Headers: httpReq.Header.Clone(),
		Body: kiroBody, Provider: "kiro",
		AuthID: authID, AuthLabel: authLabel, AuthType: authType, AuthValue: authValue,
	})

	t0 := time.Now()
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		_ = httpResp.Body.Close()
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return nil, err
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() { _ = httpResp.Body.Close() }()
		stats := streamKiroToOpenAISSE(ctx, e.cfg, httpResp.Body, req.Model, t0, out)
		reporter.EnsurePublished(ctx)
		go fireQTelemetry(token, conversationID, baseModel, profileArn, stats)
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

// newKiroHTTPRequest builds an HTTP request for the Kiro GenerateAssistantResponse API.
func newKiroHTTPRequest(ctx context.Context, token string, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, kiroBaseURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	kiroauth.SetStreamingHeaders(req, token)
	return req, nil
}

func (e *KiroExecutor) ensureFreshKiroAuth(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if !shouldRefreshKiroAuth(auth, time.Now()) {
		return auth, nil
	}
	refreshed, err := e.Refresh(ctx, auth)
	if err != nil {
		return auth, err
	}
	if refreshed == nil {
		return auth, nil
	}
	return refreshed, nil
}

func shouldRefreshKiroAuth(auth *cliproxyauth.Auth, now time.Time) bool {
	if auth == nil || auth.Disabled {
		return false
	}
	expiry, ok := auth.ExpirationTime()
	if !ok || expiry.IsZero() {
		return false
	}
	return !expiry.After(now.Add(5 * time.Minute))
}

// telemetryStats holds timing data collected during response parsing.
type telemetryStats struct {
	ResponseLength    int
	TTFCMs            float64   // time-to-first-content in milliseconds
	TimeBetweenChunks []float64 // seconds between consecutive content events
}

// telemetrySender is the function used to send telemetry. Package-level var for testability.
var telemetrySender = defaultTelemetrySender

func defaultTelemetrySender(token, conversationID, modelID, profileArn string, stats telemetryStats) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := kiroauth.SendQTelemetryEvent(ctx, &http.Client{Timeout: 10 * time.Second}, token, conversationID, modelID, profileArn, stats.ResponseLength, stats.TTFCMs, stats.TimeBetweenChunks); err != nil {
		log.Debugf("kiro: Q telemetry failed (non-fatal): %v", err)
	}
}

// fireQTelemetry sends Q API telemetry in a fire-and-forget manner.
func fireQTelemetry(token, conversationID, modelID, profileArn string, stats telemetryStats) {
	telemetrySender(token, conversationID, modelID, profileArn, stats)
}

// buildKiroRequestBody converts an OpenAI-format payload into a Kiro conversationState JSON.
// Returns the serialized body and the conversationId used.
func buildKiroRequestBody(payload []byte, modelID, profileArn string) ([]byte, string) {
	messages := gjson.GetBytes(payload, "messages")
	systemPrompt := extractKiroSystemPrompt(payload)

	tools := gjson.GetBytes(payload, "tools")

	envState := map[string]string{
		"operatingSystem":         kiroauth.KiroOSTag(),
		"currentWorkingDirectory": ".",
	}

	toolsContext := buildKiroToolsContext(tools)

	var history []map[string]any
	var msgs []gjson.Result
	if messages.IsArray() {
		msgs = messages.Array()
	}

	startIdx := 0
	if len(msgs) > 0 && msgs[0].Get("role").String() == "system" {
		startIdx = 1
	}

	// Collect tool messages that follow the last assistant into toolResults
	// Build history from all messages except the last user/tool batch
	lastUserIdx := findLastUserMessageIdx(msgs, startIdx)

	for i := startIdx; i < lastUserIdx; i++ {
		msg := msgs[i]
		role := msg.Get("role").String()
		switch role {
		case "user":
			history = append(history, buildKiroUserHistoryEntry(msg, envState))
		case "assistant":
			history = append(history, buildKiroAssistantHistoryEntry(msg))
		case "tool":
			history = appendKiroHistoryToolResults(history, envState, extractToolResults(msg))
		}
	}

	// Build current message from remaining messages (lastUserIdx to end)
	var currentContent string
	var currentImages []map[string]any
	var currentToolResults []map[string]any
	for i := lastUserIdx; i < len(msgs); i++ {
		msg := msgs[i]
		role := msg.Get("role").String()
		switch role {
		case "user":
			currentContent = extractTextContent(msg)
			currentImages = extractImages(msg)
			currentToolResults = append(currentToolResults, extractToolResults(msg)...)
		case "assistant":
			history = append(history, buildKiroAssistantHistoryEntry(msg))
		case "tool":
			currentToolResults = append(currentToolResults, extractToolResults(msg)...)
		}
	}

	if currentContent == "" && len(currentToolResults) == 0 {
		currentContent = "Continue"
	}

	// Fake work time: rewrite "Current time: ..." to business hours
	currentContent = kiroauth.FakeWorkTime(currentContent)
	systemPrompt = kiroauth.FakeWorkTime(systemPrompt)

	// Prepend system prompt
	if systemPrompt != "" {
		if len(history) > 0 {
			for _, h := range history {
				if uim, ok := h["userInputMessage"].(map[string]any); ok {
					if c, ok := uim["content"].(string); ok {
						uim["content"] = systemPrompt + "\n\n" + c
					}
					break
				}
			}
		} else if currentContent != "" {
			currentContent = systemPrompt + "\n\n" + currentContent
		}
	}

	uimCtx := map[string]any{
		"envState": envState,
		"tools":    toolsContext,
	}
	if len(currentToolResults) > 0 {
		uimCtx["toolResults"] = currentToolResults
		if currentContent == "" {
			currentContent = ""
		}
	}

	userInputMessage := map[string]any{
		"content":                 currentContent,
		"origin":                  "KIRO_CLI",
		"userInputMessageContext": uimCtx,
	}
	if len(currentImages) > 0 {
		userInputMessage["images"] = currentImages
	}
	if modelID != "" {
		userInputMessage["modelId"] = modelID
	}

	convID := uuid.New().String()
	conversationState := map[string]any{
		"chatTriggerType":     "MANUAL",
		"conversationId":      convID,
		"currentMessage":      map[string]any{"userInputMessage": userInputMessage},
		"agentContinuationId": uuid.New().String(),
		"agentTaskType":       "vibe",
	}
	if len(history) > 0 {
		conversationState["history"] = history
	}

	result := map[string]any{"conversationState": conversationState}
	if profileArn != "" {
		result["profileArn"] = profileArn
	}
	b, err := json.Marshal(result)
	if err != nil {
		log.Errorf("kiro: failed to marshal request: %v", err)
		return []byte("{}"), convID
	}
	return b, convID
}

// findLastUserMessageIdx finds the index of the last "user" or "tool" batch start.
func findLastUserMessageIdx(msgs []gjson.Result, startIdx int) int {
	if len(msgs) == 0 {
		return startIdx
	}
	// Walk backwards to find the start of the last user/tool batch
	i := len(msgs) - 1
	for i >= startIdx {
		role := msgs[i].Get("role").String()
		if role == "user" {
			return i
		}
		if role == "tool" {
			i--
			continue
		}
		// assistant or other: the batch starts after this
		return i + 1
	}
	return startIdx
}

func buildKiroUserHistoryEntry(msg gjson.Result, envState map[string]string) map[string]any {
	ctx := map[string]any{
		"envState": envState,
	}
	if toolResults := extractToolResults(msg); len(toolResults) > 0 {
		ctx["toolResults"] = toolResults
	}
	return map[string]any{
		"userInputMessage": map[string]any{
			"content":                 extractTextContent(msg),
			"userInputMessageContext": ctx,
		},
	}
}

func buildKiroAssistantHistoryEntry(msg gjson.Result) map[string]any {
	content := extractTextContent(msg)
	assistant := map[string]any{
		"content": content,
	}

	var toolUses []map[string]any
	toolCalls := msg.Get("tool_calls")
	if toolCalls.IsArray() {
		for _, tc := range toolCalls.Array() {
			name := tc.Get("function.name").String()
			toolUseID := tc.Get("id").String()
			if name == "" || toolUseID == "" {
				continue
			}
			args := tc.Get("function.arguments").String()
			var input any
			if err := json.Unmarshal([]byte(args), &input); err != nil {
				input = map[string]any{}
			}
			toolUses = append(toolUses, map[string]any{
				"input":     input,
				"name":      name,
				"toolUseId": toolUseID,
			})
		}
	}

	msgContent := msg.Get("content")
	if msgContent.IsArray() {
		for _, part := range msgContent.Array() {
			if part.Get("type").String() != "tool_use" {
				continue
			}
			name := part.Get("name").String()
			toolUseID := part.Get("id").String()
			if name == "" || toolUseID == "" {
				continue
			}
			var input any
			if raw := part.Get("input").Raw; raw != "" {
				if err := json.Unmarshal([]byte(raw), &input); err != nil {
					input = map[string]any{}
				}
			}
			if input == nil {
				input = map[string]any{}
			}
			toolUses = append(toolUses, map[string]any{
				"input":     input,
				"name":      name,
				"toolUseId": toolUseID,
			})
		}
	}

	if len(toolUses) > 0 {
		assistant["toolUses"] = toolUses
		assistant["messageId"] = uuid.New().String()
	}

	return map[string]any{"assistantResponseMessage": assistant}
}

func extractKiroSystemPrompt(payload []byte) string {
	if systemPrompt := extractTextFromContent(gjson.GetBytes(payload, "system")); systemPrompt != "" {
		return systemPrompt
	}
	first := gjson.GetBytes(payload, "messages.0")
	if first.Get("role").String() == "system" {
		return extractTextContent(first)
	}
	return ""
}

func extractTextContent(msg gjson.Result) string {
	return extractTextFromContent(msg.Get("content"))
}

func extractTextFromContent(content gjson.Result) string {
	if content.Type == gjson.String {
		return content.String()
	}
	if content.IsArray() {
		var parts []string
		for _, p := range content.Array() {
			if p.Get("type").String() == "text" {
				parts = append(parts, p.Get("text").String())
			}
		}
		return strings.Join(parts, "")
	}
	return ""
}

func extractToolResults(msg gjson.Result) []map[string]any {
	if msg.Get("role").String() == "tool" {
		toolUseID := msg.Get("tool_call_id").String()
		if toolUseID == "" {
			toolUseID = msg.Get("tool_use_id").String()
		}
		if toolUseID == "" {
			return nil
		}
		return []map[string]any{buildKiroToolResult(toolUseID, extractTextContent(msg), "success")}
	}

	content := msg.Get("content")
	if !content.IsArray() {
		return nil
	}
	var results []map[string]any
	for _, part := range content.Array() {
		if part.Get("type").String() != "tool_result" {
			continue
		}
		toolUseID := part.Get("tool_use_id").String()
		if toolUseID == "" {
			continue
		}
		status := "success"
		if part.Get("is_error").Bool() {
			status = "error"
		}
		results = append(results, buildKiroToolResult(toolUseID, extractTextFromContent(part.Get("content")), status))
	}
	return results
}

func buildKiroToolResult(toolUseID, content, status string) map[string]any {
	return map[string]any{
		"content":   kiroTextContentArray(content),
		"status":    status,
		"toolUseId": toolUseID,
	}
}

func appendKiroHistoryToolResults(history []map[string]any, envState map[string]string, toolResults []map[string]any) []map[string]any {
	if len(toolResults) == 0 {
		return history
	}
	if len(history) > 0 {
		last := history[len(history)-1]
		if uim, ok := last["userInputMessage"].(map[string]any); ok {
			uctx, ok := uim["userInputMessageContext"].(map[string]any)
			if !ok {
				uctx = map[string]any{}
				uim["userInputMessageContext"] = uctx
			}
			if existing, ok := uctx["toolResults"].([]map[string]any); ok {
				uctx["toolResults"] = append(existing, toolResults...)
			} else {
				uctx["toolResults"] = toolResults
			}
			return history
		}
	}
	return append(history, map[string]any{
		"userInputMessage": map[string]any{
			"content": "",
			"userInputMessageContext": map[string]any{
				"envState":    envState,
				"toolResults": toolResults,
			},
		},
	})
}

// extractImages extracts base64 images from OpenAI multimodal content parts
// and converts them to Kiro format: [{format: "png", source: {bytes: "<base64>"}]
func extractImages(msg gjson.Result) []map[string]any {
	content := msg.Get("content")
	if !content.IsArray() {
		return nil
	}
	var images []map[string]any
	for _, p := range content.Array() {
		switch p.Get("type").String() {
		case "image_url":
			url := p.Get("image_url.url").String()
			if url == "" {
				continue
			}
			const prefix = ";base64,"
			idx := strings.Index(url, prefix)
			if idx < 0 {
				continue
			}
			images = append(images, map[string]any{
				"format": kiroImageFormatFromMediaType(url[5:idx]),
				"source": map[string]any{"bytes": url[idx+len(prefix):]},
			})
		case "image":
			source := p.Get("source")
			if source.Get("type").String() != "base64" {
				continue
			}
			b64 := source.Get("data").String()
			if b64 == "" {
				continue
			}
			images = append(images, map[string]any{
				"format": kiroImageFormatFromMediaType(source.Get("media_type").String()),
				"source": map[string]any{"bytes": b64},
			})
		}
	}
	return images
}

func kiroImageFormatFromMediaType(mediaType string) string {
	format := "png"
	if strings.Contains(mediaType, "jpeg") || strings.Contains(mediaType, "jpg") {
		format = "jpeg"
	} else if strings.Contains(mediaType, "gif") {
		format = "gif"
	} else if strings.Contains(mediaType, "webp") {
		format = "webp"
	}
	return format
}

func buildKiroToolsContext(tools gjson.Result) []map[string]any {
	emptyProps := map[string]any{}
	emptySchema := map[string]any{"type": "object", "properties": emptyProps}

	if !tools.IsArray() || len(tools.Array()) == 0 {
		return []map[string]any{
			{
				"toolSpecification": map[string]any{
					"name":        "no_tool_available",
					"description": "Placeholder tool when no other tools are available.",
					"inputSchema": map[string]any{"json": emptySchema},
				},
			},
		}
	}

	var result []map[string]any
	for _, t := range tools.Array() {
		name := t.Get("function.name").String()
		if name == "" {
			name = t.Get("name").String()
		}
		if name == "" {
			continue
		}
		desc := t.Get("function.description").String()
		if desc == "" {
			desc = t.Get("description").String()
		}
		params := t.Get("function.parameters")
		if !params.Exists() {
			params = t.Get("input_schema")
		}
		if !params.Exists() {
			params = t.Get("inputSchema")
		}
		var schema any
		if params.Exists() {
			_ = json.Unmarshal([]byte(params.Raw), &schema)
		}
		if schema == nil {
			schema = emptySchema
		}
		result = append(result, map[string]any{
			"toolSpecification": map[string]any{
				"name":        name,
				"description": desc,
				"inputSchema": map[string]any{"json": schema},
			},
		})
	}
	if len(result) == 0 {
		return []map[string]any{
			{
				"toolSpecification": map[string]any{
					"name":        "no_tool_available",
					"description": "Placeholder tool when no other tools are available.",
					"inputSchema": map[string]any{"json": emptySchema},
				},
			},
		}
	}
	return result
}

// --- Kiro event stream parsing ---

// kiroTextContentArray wraps text in the Kiro content array format: [{"text": "..."}]
func kiroTextContentArray(text string) []map[string]string {
	m := map[string]string{"text": text}
	return []map[string]string{m}
}

type kiroToolCall struct {
	ID   string
	Name string
	Args string
}

type kiroEvent struct {
	evtType   string
	data      string
	name      string
	toolUseID string
	stop      bool
}

func parseKiroEventStream(data []byte) (string, []kiroToolCall) {
	var content strings.Builder
	var toolCalls []kiroToolCall
	var cur *kiroToolCall

	for _, evt := range extractKiroEvents(data) {
		switch evt.evtType {
		case "content":
			content.WriteString(evt.data)
		case "toolUse":
			// Initial tool use event — start a new tool call
			cur = &kiroToolCall{ID: evt.toolUseID, Name: evt.name}
		case "toolUseInput":
			if cur != nil {
				cur.Args += evt.data
			}
		case "toolUseStop":
			if cur != nil {
				toolCalls = append(toolCalls, *cur)
				cur = nil
			}
		}
	}
	if cur != nil {
		toolCalls = append(toolCalls, *cur)
	}
	return content.String(), toolCalls
}

func extractKiroEvents(data []byte) []kiroEvent {
	var events []kiroEvent
	s := string(data)
	pos := 0
	for pos < len(s) {
		idx := findKiroJSONStart(s, pos)
		if idx < 0 {
			break
		}
		end := findKiroJSONEnd(s, idx)
		if end < 0 {
			break
		}
		if evt, ok := classifyKiroEvent(s[idx : end+1]); ok {
			events = append(events, evt)
		}
		pos = end + 1
	}
	return events
}

func findKiroJSONStart(s string, from int) int {
	prefixes := []string{
		`{"content":`, `{"name":`, `{"followupPrompt":`,
		`{"input":`, `{"stop":`, `{"unit":`,
	}
	minPos := -1
	for _, p := range prefixes {
		idx := strings.Index(s[from:], p)
		if idx >= 0 {
			actual := from + idx
			if minPos < 0 || actual < minPos {
				minPos = actual
			}
		}
	}
	return minPos
}

func findKiroJSONEnd(s string, start int) int {
	braces := 0
	inStr := false
	esc := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if esc {
			esc = false
			continue
		}
		if c == '\\' && inStr {
			esc = true
			continue
		}
		if c == '"' {
			inStr = !inStr
			continue
		}
		if !inStr {
			if c == '{' {
				braces++
			} else if c == '}' {
				braces--
				if braces == 0 {
					return i
				}
			}
		}
	}
	return -1
}

func classifyKiroEvent(jsonStr string) (kiroEvent, bool) {
	r := gjson.Parse(jsonStr)

	// Content event: has "content", no "followupPrompt"
	if r.Get("content").Exists() && !r.Get("followupPrompt").Exists() {
		return kiroEvent{evtType: "content", data: r.Get("content").String()}, true
	}
	// Tool events: all have "name" + "toolUseId" per spec
	if r.Get("toolUseId").Exists() {
		// Stop event: has "stop": true
		if r.Get("stop").Bool() {
			return kiroEvent{evtType: "toolUseStop", stop: true, name: r.Get("name").String(), toolUseID: r.Get("toolUseId").String()}, true
		}
		// Input event: has "input" field
		if inp := r.Get("input"); inp.Exists() {
			data := inp.String()
			if inp.Type != gjson.String {
				data = inp.Raw
			}
			return kiroEvent{evtType: "toolUseInput", data: data, name: r.Get("name").String(), toolUseID: r.Get("toolUseId").String()}, true
		}
		// Initial tool use event: name + toolUseId, no input, no stop
		return kiroEvent{evtType: "toolUse", name: r.Get("name").String(), toolUseID: r.Get("toolUseId").String()}, true
	}
	return kiroEvent{}, false
}

// --- OpenAI response builders ---

func buildOpenAINonStreamResponse(model, content string, toolCalls []kiroToolCall) []byte {
	finishReason := "stop"
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
	}

	msg := map[string]any{
		"role":    "assistant",
		"content": content,
	}
	if len(toolCalls) > 0 {
		var tcs []map[string]any
		for _, tc := range toolCalls {
			tcs = append(tcs, map[string]any{
				"id":   tc.ID,
				"type": "function",
				"function": map[string]string{
					"name":      tc.Name,
					"arguments": tc.Args,
				},
			})
		}
		msg["tool_calls"] = tcs
	}

	resp := map[string]any{
		"id":     "chatcmpl-" + uuid.New().String()[:8],
		"object": "chat.completion",
		"model":  model,
		"choices": []map[string]any{
			{"index": 0, "message": msg, "finish_reason": finishReason},
		},
	}
	b, _ := json.Marshal(resp)
	return b
}

func streamKiroToOpenAISSE(ctx context.Context, cfg *config.Config, body io.Reader, model string, t0 time.Time, out chan<- cliproxyexecutor.StreamChunk) telemetryStats {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(nil, 52_428_800)
	var buf strings.Builder
	chatID := "chatcmpl-" + uuid.New().String()[:8]

	var cur *kiroToolCall
	tcIndex := 0
	totalContentLen := 0

	// Timing collection
	var ttfc float64
	var timeBetweenChunks []float64
	firstContent := true
	var lastContentTime time.Time

	flush := func() {
		if buf.Len() == 0 {
			return
		}
		events := extractKiroEvents([]byte(buf.String()))
		buf.Reset()
		for _, evt := range events {
			switch evt.evtType {
			case "content":
				now := time.Now()
				if firstContent {
					ttfc = float64(now.Sub(t0).Milliseconds())
					firstContent = false
				} else {
					timeBetweenChunks = append(timeBetweenChunks, now.Sub(lastContentTime).Seconds())
				}
				lastContentTime = now

				chunk := buildOpenAISSEChunk(chatID, model, evt.data, "", nil)
				helps.AppendAPIResponseChunk(ctx, cfg, chunk)
				out <- cliproxyexecutor.StreamChunk{Payload: chunk}
				totalContentLen += len(evt.data)
			case "toolUse":
				cur = &kiroToolCall{ID: evt.toolUseID, Name: evt.name}
			case "toolUseInput":
				if cur != nil {
					cur.Args += evt.data
				}
			case "toolUseStop":
				if cur != nil {
					chunk := buildOpenAISSEToolCallChunk(chatID, model, tcIndex, cur)
					helps.AppendAPIResponseChunk(ctx, cfg, chunk)
					out <- cliproxyexecutor.StreamChunk{Payload: chunk}
					tcIndex++
					cur = nil
				}
			}
		}
	}

	for scanner.Scan() {
		line := scanner.Bytes()
		helps.AppendAPIResponseChunk(ctx, cfg, line)
		buf.Write(line)
		buf.WriteByte('\n')
		flush()
	}
	flush()

	if cur != nil {
		chunk := buildOpenAISSEToolCallChunk(chatID, model, tcIndex, cur)
		out <- cliproxyexecutor.StreamChunk{Payload: chunk}
		tcIndex++
	}

	finishReason := "stop"
	if tcIndex > 0 {
		finishReason = "tool_calls"
	}
	out <- cliproxyexecutor.StreamChunk{Payload: buildOpenAISSEChunk(chatID, model, "", finishReason, nil)}

	if timeBetweenChunks == nil {
		timeBetweenChunks = []float64{}
	}
	return telemetryStats{
		ResponseLength:    totalContentLen,
		TTFCMs:            ttfc,
		TimeBetweenChunks: timeBetweenChunks,
	}
}

func buildOpenAISSEChunk(id, model, content, finishReason string, toolCalls []map[string]any) []byte {
	delta := map[string]any{}
	if content != "" {
		delta["content"] = content
	}
	if toolCalls != nil {
		delta["tool_calls"] = toolCalls
	}
	choice := map[string]any{"index": 0, "delta": delta}
	if finishReason != "" {
		choice["finish_reason"] = finishReason
	} else {
		choice["finish_reason"] = nil
	}
	chunk := map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"model":   model,
		"choices": []map[string]any{choice},
	}
	b, _ := json.Marshal(chunk)
	return b
}

func buildOpenAISSEToolCallChunk(id, model string, index int, tc *kiroToolCall) []byte {
	tcMap := map[string]any{
		"index": index,
		"id":    tc.ID,
		"type":  "function",
		"function": map[string]string{
			"name":      tc.Name,
			"arguments": tc.Args,
		},
	}
	return buildOpenAISSEChunk(id, model, "", "", []map[string]any{tcMap})
}
