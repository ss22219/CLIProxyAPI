package helps

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tiktoken-go/tokenizer"
)

const (
	defaultPromptCacheTTL       = 5 * time.Minute
	oneHourPromptCacheTTL       = time.Hour
	promptCacheLookbackLimit    = 10
	promptCacheBillingHeaderTag = "__anthropic_billing_header__"
)

// PromptCacheResult contains Anthropic-compatible prompt-cache accounting.
type PromptCacheResult struct {
	CacheReadInputTokens       int64
	CacheCreationInputTokens   int64
	CacheCreation5mInputTokens int64
	CacheCreation1hInputTokens int64
}

type PromptCacheTracker struct {
	mu      sync.Mutex
	entries map[string]map[[32]byte]promptCacheEntry
}

type promptCacheEntry struct {
	tokenCount int64
	ttl        time.Duration
	expiresAt  time.Time
}

type promptCacheProfile struct {
	totalInputTokens int64
	minCacheable     int64
	blocks           []promptCacheBlock
	breakpoints      []promptCacheBreakpoint
}

type promptCacheBlock struct {
	prefixFingerprint [32]byte
	cumulativeTokens  int64
}

type promptCacheBreakpoint struct {
	blockIndex int
	ttl        time.Duration
}

type resolvedPromptCacheBreakpoint struct {
	blockIndex       int
	cumulativeTokens int64
	ttl              time.Duration
}

type pendingPromptCacheBlock struct {
	value         any
	tokens        int64
	breakpointTTL time.Duration
	hasBreakpoint bool
	messageIndex  int
	isMessageEnd  bool
}

var defaultPromptCacheTracker = NewPromptCacheTracker()

func NewPromptCacheTracker() *PromptCacheTracker {
	return &PromptCacheTracker{
		entries: make(map[string]map[[32]byte]promptCacheEntry),
	}
}

// TrackPromptCacheUsage computes prompt-cache accounting for a successful request
// and then stores its cacheable checkpoints. Cache hits do not extend TTL.
func TrackPromptCacheUsage(model string, payload []byte, totalInputTokens int64, credentialKey string, maxSupportedTTL time.Duration) PromptCacheResult {
	return defaultPromptCacheTracker.Track(model, payload, totalInputTokens, credentialKey, maxSupportedTTL)
}

func (t *PromptCacheTracker) Track(model string, payload []byte, totalInputTokens int64, credentialKey string, maxSupportedTTL time.Duration) PromptCacheResult {
	if t == nil || len(payload) == 0 || totalInputTokens <= 0 {
		return PromptCacheResult{}
	}
	profile, ok := buildPromptCacheProfile(model, payload, totalInputTokens, maxSupportedTTL)
	if !ok {
		return PromptCacheResult{}
	}
	if strings.TrimSpace(credentialKey) == "" {
		credentialKey = "default"
	}
	result := t.compute(credentialKey, &profile)
	t.update(credentialKey, &profile)
	return result
}

func buildPromptCacheProfile(model string, payload []byte, totalInputTokens int64, maxSupportedTTL time.Duration) (promptCacheProfile, bool) {
	if !gjson.ValidBytes(payload) {
		return promptCacheProfile{}, false
	}
	enc, err := TokenizerForModel(model)
	if err != nil {
		return promptCacheProfile{}, false
	}
	if maxSupportedTTL <= 0 {
		maxSupportedTTL = defaultPromptCacheTTL
	}

	root := gjson.ParseBytes(payload)
	flattened := flattenPromptCacheBlocks(enc, root)
	if len(flattened) == 0 {
		return promptCacheProfile{}, false
	}

	prelude := map[string]any{
		"model":       root.Get("model").Value(),
		"tool_choice": rawJSONValue(root.Get("tool_choice")),
	}
	preludeBytes := mustCanonicalJSON(prelude)
	prefixSeed := sha256.New()
	var lenBytes [8]byte
	binary.BigEndian.PutUint64(lenBytes[:], uint64(len(preludeBytes)))
	prefixSeed.Write(lenBytes[:])
	prefixSeed.Write(preludeBytes)
	currentPrefix := prefixSeed.Sum(nil)

	profile := promptCacheProfile{
		totalInputTokens: maxInt64(totalInputTokens, 0),
		minCacheable:     minimumPromptCacheTokens(model),
		blocks:           make([]promptCacheBlock, 0, len(flattened)),
	}

	activeTTL := time.Duration(0)
	seenBreakpoints := make(map[int]struct{})
	var cumulativeTokens int64
	for i, block := range flattened {
		cumulativeTokens += block.tokens
		blockHash := sha256.Sum256(mustCanonicalJSON(block.value))
		prefixHasher := sha256.New()
		prefixHasher.Write(currentPrefix)
		prefixHasher.Write(blockHash[:])
		var fingerprint [32]byte
		copy(fingerprint[:], prefixHasher.Sum(nil))
		currentPrefix = fingerprint[:]

		profile.blocks = append(profile.blocks, promptCacheBlock{
			prefixFingerprint: fingerprint,
			cumulativeTokens:  cumulativeTokens,
		})

		if block.hasBreakpoint {
			activeTTL = minDuration(block.breakpointTTL, maxSupportedTTL)
			addPromptCacheBreakpoint(&profile, seenBreakpoints, i, activeTTL)
		}
		if block.isMessageEnd && block.messageIndex >= 0 && activeTTL > 0 {
			addPromptCacheBreakpoint(&profile, seenBreakpoints, i, activeTTL)
		}
	}

	// Implicit breakpoint at the very last block when the request carries no explicit
	// cache_control markers. This lets backends without Anthropic-style annotations
	// (Kiro upstream, OpenAI-format clients, etc.) still benefit from prefix matching:
	// repeating the same conversation prefix within TTL is reported as cache_read.
	if len(profile.breakpoints) == 0 && len(profile.blocks) > 0 {
		lastIdx := len(profile.blocks) - 1
		addPromptCacheBreakpoint(&profile, seenBreakpoints, lastIdx, maxSupportedTTL)
	}

	return profile, true
}

func addPromptCacheBreakpoint(profile *promptCacheProfile, seen map[int]struct{}, blockIndex int, ttl time.Duration) {
	if profile == nil || ttl <= 0 {
		return
	}
	if _, ok := seen[blockIndex]; ok {
		return
	}
	seen[blockIndex] = struct{}{}
	profile.breakpoints = append(profile.breakpoints, promptCacheBreakpoint{
		blockIndex: blockIndex,
		ttl:        ttl,
	})
}

func (t *PromptCacheTracker) compute(credentialKey string, profile *promptCacheProfile) PromptCacheResult {
	last, ok := profile.lastCacheableBreakpoint()
	if !ok {
		return PromptCacheResult{}
	}
	lastTokens := minInt64(last.cumulativeTokens, profile.totalInputTokens)

	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneExpiredLocked(now)

	credentialEntries, ok := t.entries[credentialKey]
	if !ok {
		cache5m, cache1h := promptCacheTTLBreakdown(profile, 0)
		return PromptCacheResult{
			CacheCreationInputTokens:   lastTokens,
			CacheCreation5mInputTokens: cache5m,
			CacheCreation1hInputTokens: cache1h,
		}
	}

	var matchedTokens int64
	breakpoints := profile.cacheableBreakpoints()
	for i, checked := len(breakpoints)-1, 0; i >= 0 && checked < promptCacheLookbackLimit; i, checked = i-1, checked+1 {
		breakpoint := breakpoints[i]
		block := profile.blocks[breakpoint.blockIndex]
		entry, exists := credentialEntries[block.prefixFingerprint]
		if !exists || !entry.expiresAt.After(now) {
			continue
		}
		matchedTokens = minInt64(breakpoint.cumulativeTokens, profile.totalInputTokens)
		break
	}

	newTokens := maxInt64(lastTokens-matchedTokens, 0)
	cache5m, cache1h := promptCacheTTLBreakdown(profile, matchedTokens)
	return PromptCacheResult{
		CacheReadInputTokens:       maxInt64(matchedTokens, 0),
		CacheCreationInputTokens:   newTokens,
		CacheCreation5mInputTokens: cache5m,
		CacheCreation1hInputTokens: cache1h,
	}
}

func (t *PromptCacheTracker) update(credentialKey string, profile *promptCacheProfile) {
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneExpiredLocked(now)

	credentialEntries := t.entries[credentialKey]
	if credentialEntries == nil {
		credentialEntries = make(map[[32]byte]promptCacheEntry)
		t.entries[credentialKey] = credentialEntries
	}

	for _, breakpoint := range profile.cacheableBreakpoints() {
		block := profile.blocks[breakpoint.blockIndex]
		if existing, ok := credentialEntries[block.prefixFingerprint]; ok {
			if block.cumulativeTokens > existing.tokenCount {
				existing.tokenCount = block.cumulativeTokens
			}
			if breakpoint.ttl > existing.ttl {
				existing.ttl = breakpoint.ttl
			}
			credentialEntries[block.prefixFingerprint] = existing
			continue
		}
		credentialEntries[block.prefixFingerprint] = promptCacheEntry{
			tokenCount: block.cumulativeTokens,
			ttl:        breakpoint.ttl,
			expiresAt:  now.Add(breakpoint.ttl),
		}
	}
}

func (t *PromptCacheTracker) pruneExpiredLocked(now time.Time) {
	for credentialKey, credentialEntries := range t.entries {
		for fingerprint, entry := range credentialEntries {
			if !entry.expiresAt.After(now) {
				delete(credentialEntries, fingerprint)
			}
		}
		if len(credentialEntries) == 0 {
			delete(t.entries, credentialKey)
		}
	}
}

func (p *promptCacheProfile) cacheableBreakpoints() []resolvedPromptCacheBreakpoint {
	if p == nil || len(p.breakpoints) == 0 {
		return nil
	}
	out := make([]resolvedPromptCacheBreakpoint, 0, len(p.breakpoints))
	for _, breakpoint := range p.breakpoints {
		if breakpoint.blockIndex < 0 || breakpoint.blockIndex >= len(p.blocks) {
			continue
		}
		block := p.blocks[breakpoint.blockIndex]
		if block.cumulativeTokens < p.minCacheable {
			continue
		}
		out = append(out, resolvedPromptCacheBreakpoint{
			blockIndex:       breakpoint.blockIndex,
			cumulativeTokens: block.cumulativeTokens,
			ttl:              breakpoint.ttl,
		})
	}
	return out
}

func (p *promptCacheProfile) lastCacheableBreakpoint() (resolvedPromptCacheBreakpoint, bool) {
	breakpoints := p.cacheableBreakpoints()
	if len(breakpoints) == 0 {
		return resolvedPromptCacheBreakpoint{}, false
	}
	return breakpoints[len(breakpoints)-1], true
}

func promptCacheTTLBreakdown(profile *promptCacheProfile, matchedTokens int64) (int64, int64) {
	last, ok := profile.lastCacheableBreakpoint()
	if !ok {
		return 0, 0
	}
	newTokens := maxInt64(minInt64(last.cumulativeTokens, profile.totalInputTokens)-matchedTokens, 0)
	if newTokens == 0 {
		return 0, 0
	}
	if last.ttl == oneHourPromptCacheTTL {
		return 0, newTokens
	}
	return newTokens, 0
}

func flattenPromptCacheBlocks(enc tokenizer.Codec, root gjson.Result) []pendingPromptCacheBlock {
	blocks := make([]pendingPromptCacheBlock, 0, 32)

	tools := root.Get("tools")
	if tools.IsArray() {
		tools.ForEach(func(idx, tool gjson.Result) bool {
			value := rawJSONValue(tool)
			ttl, hasBreakpoint := extractPromptCacheTTL(value)
			stripPromptCacheControl(value)
			blocks = append(blocks, pendingPromptCacheBlock{
				value: map[string]any{
					"kind":       "tool",
					"tool_index": int(idx.Int()),
					"tool":       value,
				},
				tokens:        countPromptCacheValueTokens(enc, value),
				breakpointTTL: ttl,
				hasBreakpoint: hasBreakpoint,
				messageIndex:  -1,
			})
			return true
		})
	}

	system := root.Get("system")
	if system.Type == gjson.String {
		value := map[string]any{"type": "text", "text": system.String()}
		blocks = append(blocks, pendingPromptCacheBlock{
			value: map[string]any{
				"kind":         "system",
				"system_index": 0,
				"block":        value,
			},
			tokens:       countPromptCacheValueTokens(enc, value),
			messageIndex: -1,
		})
	} else if system.IsArray() {
		system.ForEach(func(idx, block gjson.Result) bool {
			value := rawJSONValue(block)
			ttl, hasBreakpoint := extractPromptCacheTTL(value)
			stripPromptCacheControl(value)
			canonicalizeSystemPromptCacheBlock(value)
			blocks = append(blocks, pendingPromptCacheBlock{
				value: map[string]any{
					"kind":         "system",
					"system_index": int(idx.Int()),
					"block":        value,
				},
				tokens:        countPromptCacheValueTokens(enc, value),
				breakpointTTL: ttl,
				hasBreakpoint: hasBreakpoint,
				messageIndex:  -1,
			})
			return true
		})
	}

	messages := root.Get("messages")
	if messages.IsArray() {
		messages.ForEach(func(idx, msg gjson.Result) bool {
			messageIndex := int(idx.Int())
			role := msg.Get("role").String()
			content := msg.Get("content")
			switch {
			case content.Type == gjson.String:
				value := map[string]any{"type": "text", "text": content.String()}
				blocks = append(blocks, buildPromptCacheMessageBlock(enc, messageIndex, role, 0, value, 0, false, true))
			case content.IsArray():
				parts := content.Array()
				lastPart := len(parts) - 1
				for blockIndex, part := range parts {
					value := rawJSONValue(part)
					ttl, hasBreakpoint := extractPromptCacheTTL(value)
					stripPromptCacheControl(value)
					blocks = append(blocks, buildPromptCacheMessageBlock(enc, messageIndex, role, blockIndex, value, ttl, hasBreakpoint, blockIndex == lastPart))
				}
			case content.Exists():
				value := rawJSONValue(content)
				blocks = append(blocks, buildPromptCacheMessageBlock(enc, messageIndex, role, 0, value, 0, false, true))
			}
			return true
		})
	}

	return blocks
}

func buildPromptCacheMessageBlock(enc tokenizer.Codec, messageIndex int, role string, blockIndex int, value any, ttl time.Duration, hasBreakpoint, isMessageEnd bool) pendingPromptCacheBlock {
	return pendingPromptCacheBlock{
		value: map[string]any{
			"kind":          "message",
			"message_index": messageIndex,
			"role":          role,
			"block_index":   blockIndex,
			"block":         value,
		},
		tokens:        countPromptCacheValueTokens(enc, value),
		breakpointTTL: ttl,
		hasBreakpoint: hasBreakpoint,
		messageIndex:  messageIndex,
		isMessageEnd:  isMessageEnd,
	}
}

func extractPromptCacheTTL(value any) (time.Duration, bool) {
	obj, ok := value.(map[string]any)
	if !ok {
		return 0, false
	}
	cacheControl, ok := obj["cache_control"].(map[string]any)
	if !ok {
		return 0, false
	}
	cacheType, _ := cacheControl["type"].(string)
	if cacheType != "ephemeral" {
		return 0, false
	}
	if ttl, _ := cacheControl["ttl"].(string); ttl == "1h" {
		return oneHourPromptCacheTTL, true
	}
	return defaultPromptCacheTTL, true
}

func stripPromptCacheControl(value any) {
	switch typed := value.(type) {
	case map[string]any:
		delete(typed, "cache_control")
		for _, child := range typed {
			stripPromptCacheControl(child)
		}
	case []any:
		for _, child := range typed {
			stripPromptCacheControl(child)
		}
	}
}

func canonicalizeSystemPromptCacheBlock(value any) {
	obj, ok := value.(map[string]any)
	if !ok {
		return
	}
	if blockType, _ := obj["type"].(string); blockType != "" && blockType != "text" {
		return
	}
	text, _ := obj["text"].(string)
	if !strings.HasPrefix(text, "x-anthropic-billing-header:") {
		return
	}
	obj["text"] = promptCacheBillingHeaderTag
}

func countPromptCacheValueTokens(enc tokenizer.Codec, value any) int64 {
	if enc == nil {
		return 0
	}
	var segments []string
	collectPromptCacheTokenSegments(value, &segments)
	joined := strings.TrimSpace(strings.Join(segments, "\n"))
	if joined == "" {
		return 0
	}
	count, err := enc.Count(joined)
	if err != nil {
		return 0
	}
	return int64(count)
}

func collectPromptCacheTokenSegments(value any, segments *[]string) {
	switch typed := value.(type) {
	case string:
		addIfNotEmpty(segments, typed)
	case float64, bool, nil:
		addIfNotEmpty(segments, fmt.Sprintf("%v", typed))
	case []any:
		for _, child := range typed {
			collectPromptCacheTokenSegments(child, segments)
		}
	case map[string]any:
		for _, key := range []string{"type", "role", "name", "description", "text"} {
			if str, ok := typed[key].(string); ok {
				addIfNotEmpty(segments, str)
			}
		}
		for key, child := range typed {
			switch key {
			case "type", "role", "name", "description", "text", "cache_control":
				continue
			default:
				collectPromptCacheTokenSegments(child, segments)
			}
		}
	}
}

func rawJSONValue(result gjson.Result) any {
	if !result.Exists() {
		return nil
	}
	if result.Type == gjson.String {
		return result.String()
	}
	var value any
	if result.Raw != "" && json.Unmarshal([]byte(result.Raw), &value) == nil {
		return value
	}
	return result.Value()
}

func mustCanonicalJSON(value any) []byte {
	b, err := json.Marshal(value)
	if err != nil {
		return []byte("null")
	}
	return b
}

func minimumPromptCacheTokens(model string) int64 {
	model = strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.Contains(model, "opus"):
		return 4096
	case strings.Contains(model, "haiku-3"), strings.Contains(model, "haiku_3"):
		return 2048
	default:
		return 1024
	}
}

func minDuration(a, b time.Duration) time.Duration {
	if a <= 0 {
		return b
	}
	if b <= 0 || a < b {
		return a
	}
	return b
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
