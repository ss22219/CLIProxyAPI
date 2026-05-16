package helps

import (
	"strings"
	"testing"
	"time"
)

func TestPromptCacheTrackerCreatesThenReadsSamePrefix(t *testing.T) {
	tracker := NewPromptCacheTracker()
	payload := []byte(`{
		"model":"claude-sonnet-4.6",
		"system":[{"type":"text","text":"` + longPromptCacheText() + `","cache_control":{"type":"ephemeral"}}],
		"messages":[{"role":"user","content":"hello"}]
	}`)
	total := promptCacheTestTokenCount(t, "claude-sonnet-4.6", payload)

	first := tracker.Track("claude-sonnet-4.6", payload, total, "cred-a", defaultPromptCacheTTL)
	if first.CacheCreationInputTokens <= 0 {
		t.Fatalf("first creation tokens = %d, want > 0", first.CacheCreationInputTokens)
	}
	if first.CacheReadInputTokens != 0 {
		t.Fatalf("first read tokens = %d, want 0", first.CacheReadInputTokens)
	}

	second := tracker.Track("claude-sonnet-4.6", payload, total, "cred-a", defaultPromptCacheTTL)
	if second.CacheReadInputTokens != first.CacheCreationInputTokens {
		t.Fatalf("second read tokens = %d, want first creation %d", second.CacheReadInputTokens, first.CacheCreationInputTokens)
	}
	if second.CacheCreationInputTokens != 0 {
		t.Fatalf("second creation tokens = %d, want 0", second.CacheCreationInputTokens)
	}
}

func TestPromptCacheTrackerBillingHeaderDriftDoesNotMiss(t *testing.T) {
	tracker := NewPromptCacheTracker()
	payload1 := []byte(`{
		"model":"claude-sonnet-4.6",
		"system":[
			{"type":"text","text":"x-anthropic-billing-header: cc_version=1; cch=aaaaa;"},
			{"type":"text","text":"` + longPromptCacheText() + `","cache_control":{"type":"ephemeral"}}
		],
		"messages":[{"role":"user","content":"hello"}]
	}`)
	payload2 := []byte(`{
		"model":"claude-sonnet-4.6",
		"system":[
			{"type":"text","text":"x-anthropic-billing-header: cc_version=2.999; cch=bbbbb; extra=changed;"},
			{"type":"text","text":"` + longPromptCacheText() + `","cache_control":{"type":"ephemeral"}}
		],
		"messages":[{"role":"user","content":"hello"}]
	}`)

	total1 := promptCacheTestTokenCount(t, "claude-sonnet-4.6", payload1)
	total2 := promptCacheTestTokenCount(t, "claude-sonnet-4.6", payload2)
	first := tracker.Track("claude-sonnet-4.6", payload1, total1, "cred-a", defaultPromptCacheTTL)
	second := tracker.Track("claude-sonnet-4.6", payload2, total2, "cred-a", defaultPromptCacheTTL)

	if first.CacheCreationInputTokens <= 0 {
		t.Fatalf("first creation tokens = %d, want > 0", first.CacheCreationInputTokens)
	}
	if second.CacheReadInputTokens <= 0 {
		t.Fatalf("second read tokens = %d, want > 0", second.CacheReadInputTokens)
	}
	if second.CacheCreationInputTokens != 0 {
		t.Fatalf("second creation tokens = %d, want 0", second.CacheCreationInputTokens)
	}
}

func TestPromptCacheTrackerScopesByCredential(t *testing.T) {
	tracker := NewPromptCacheTracker()
	payload := []byte(`{
		"model":"claude-sonnet-4.6",
		"system":[{"type":"text","text":"` + longPromptCacheText() + `","cache_control":{"type":"ephemeral"}}],
		"messages":[{"role":"user","content":"hello"}]
	}`)
	total := promptCacheTestTokenCount(t, "claude-sonnet-4.6", payload)

	first := tracker.Track("claude-sonnet-4.6", payload, total, "cred-a", defaultPromptCacheTTL)
	secondCredential := tracker.Track("claude-sonnet-4.6", payload, total, "cred-b", defaultPromptCacheTTL)

	if first.CacheCreationInputTokens <= 0 {
		t.Fatalf("first creation tokens = %d, want > 0", first.CacheCreationInputTokens)
	}
	if secondCredential.CacheReadInputTokens != 0 {
		t.Fatalf("second credential read tokens = %d, want 0", secondCredential.CacheReadInputTokens)
	}
	if secondCredential.CacheCreationInputTokens <= 0 {
		t.Fatalf("second credential creation tokens = %d, want > 0", secondCredential.CacheCreationInputTokens)
	}
}

func longPromptCacheText() string {
	return strings.Repeat("cacheable prompt chunk ", 1200)
}

func promptCacheTestTokenCount(t *testing.T, model string, payload []byte) int64 {
	t.Helper()
	enc, err := TokenizerForModel(model)
	if err != nil {
		t.Fatalf("TokenizerForModel error: %v", err)
	}
	count, err := CountOpenAIChatTokens(enc, payload)
	if err != nil {
		t.Fatalf("CountOpenAIChatTokens error: %v", err)
	}
	if count <= 0 {
		t.Fatalf("token count = %d, want > 0", count)
	}
	return count
}

func TestPromptCacheTrackerDoesNotRefreshTTLOnHit(t *testing.T) {
	tracker := NewPromptCacheTracker()
	payload := []byte(`{
		"model":"claude-sonnet-4.6",
		"system":[{"type":"text","text":"` + longPromptCacheText() + `","cache_control":{"type":"ephemeral"}}],
		"messages":[{"role":"user","content":"hello"}]
	}`)
	total := promptCacheTestTokenCount(t, "claude-sonnet-4.6", payload)

	first := tracker.Track("claude-sonnet-4.6", payload, total, "cred-a", 10*time.Millisecond)
	if first.CacheCreationInputTokens <= 0 {
		t.Fatalf("first creation tokens = %d, want > 0", first.CacheCreationInputTokens)
	}
	time.Sleep(20 * time.Millisecond)
	second := tracker.Track("claude-sonnet-4.6", payload, total, "cred-a", 10*time.Millisecond)
	if second.CacheReadInputTokens != 0 {
		t.Fatalf("expired read tokens = %d, want 0", second.CacheReadInputTokens)
	}
}
