package executor

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	kiroauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/kiro"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	kiroAPIKeyUsageOKTTL          = time.Minute
	kiroCapacityRetryAfter        = time.Minute
	kiroDefaultQuotaRetryAfter    = 30 * time.Minute
	kiroMinimumQuotaRetryAfter    = time.Second
	kiroQuotaCacheKeyTokenMaxSize = 16
)

type kiroQuotaCacheEntry struct {
	ExpiresAt  time.Time
	Exceeded   bool
	RetryAfter time.Duration
	Message    string
	Credit     *kiroauth.CreditUsageSummary
}

var kiroAPIKeyQuotaCache sync.Map

func (e *KiroExecutor) preflightKiroAPIKeyQuota(ctx context.Context, auth *cliproxyauth.Auth, token string) (*kiroauth.CreditUsageSummary, error) {
	if !isKiroAPIKeyAuth(auth) || strings.TrimSpace(token) == "" {
		return nil, nil
	}
	now := time.Now()
	cacheKey := kiroAPIKeyQuotaCacheKey(auth, token)
	if cached, ok := kiroAPIKeyQuotaCache.Load(cacheKey); ok {
		if entry, okEntry := cached.(kiroQuotaCacheEntry); okEntry && entry.ExpiresAt.After(now) {
			if entry.Exceeded {
				retryAfter := entry.RetryAfter
				if retryAfter < kiroMinimumQuotaRetryAfter {
					retryAfter = time.Until(entry.ExpiresAt)
				}
				if retryAfter < kiroMinimumQuotaRetryAfter {
					retryAfter = kiroDefaultQuotaRetryAfter
				}
				return entry.Credit, statusErr{code: http.StatusTooManyRequests, msg: entry.Message, retryAfter: &retryAfter}
			}
			return entry.Credit, nil
		}
		kiroAPIKeyQuotaCache.Delete(cacheKey)
	}

	svc := kiroauth.NewKiroAuthWithProxyURL(e.cfg, auth.ProxyURL)
	limits, err := svc.FetchUsageLimits(ctx, token, "")
	if err != nil {
		log.Debugf("kiro: API key usage preflight skipped: %v", err)
		kiroAPIKeyQuotaCache.Store(cacheKey, kiroQuotaCacheEntry{ExpiresAt: now.Add(kiroAPIKeyUsageOKTTL)})
		return nil, nil
	}
	credit, hasCredit := limits.CreditUsage(now)
	var creditPtr *kiroauth.CreditUsageSummary
	if hasCredit {
		creditPtr = &credit
	}
	exceeded, resetAt, message := limits.CreditExhausted(now)
	if !exceeded {
		kiroAPIKeyQuotaCache.Store(cacheKey, kiroQuotaCacheEntry{ExpiresAt: now.Add(kiroAPIKeyUsageOKTTL), Credit: creditPtr})
		return creditPtr, nil
	}
	retryAfter := resetAt.Sub(now)
	if retryAfter < kiroMinimumQuotaRetryAfter {
		retryAfter = kiroDefaultQuotaRetryAfter
		resetAt = now.Add(retryAfter)
	}
	if strings.TrimSpace(message) == "" {
		message = "kiro API key credit quota exceeded"
	}
	kiroAPIKeyQuotaCache.Store(cacheKey, kiroQuotaCacheEntry{
		ExpiresAt:  resetAt,
		Exceeded:   true,
		RetryAfter: retryAfter,
		Message:    message,
		Credit:     creditPtr,
	})
	return creditPtr, statusErr{code: http.StatusTooManyRequests, msg: message, retryAfter: &retryAfter}
}

func kiroAPIKeyQuotaCacheKey(auth *cliproxyauth.Auth, token string) string {
	if auth != nil && strings.TrimSpace(auth.ID) != "" {
		return auth.ID
	}
	trimmed := strings.TrimSpace(token)
	if len(trimmed) > kiroQuotaCacheKeyTokenMaxSize {
		trimmed = trimmed[:kiroQuotaCacheKeyTokenMaxSize]
	}
	return "token:" + trimmed
}

func normalizeKiroStatusError(statusCode int, body []byte) statusErr {
	msg := string(body)
	if statusCode == http.StatusTooManyRequests && kiroIsInsufficientModelCapacity(body) {
		retryAfter := kiroCapacityRetryAfter
		return statusErr{code: http.StatusServiceUnavailable, msg: msg, retryAfter: &retryAfter}
	}
	if kiroLooksLikeCreditQuotaExceeded(body) {
		retryAfter := kiroDefaultQuotaRetryAfter
		return statusErr{code: http.StatusTooManyRequests, msg: msg, retryAfter: &retryAfter}
	}
	return statusErr{code: statusCode, msg: msg}
}

func kiroIsInsufficientModelCapacity(body []byte) bool {
	reason := gjson.GetBytes(body, "reason").String()
	if strings.EqualFold(strings.TrimSpace(reason), "INSUFFICIENT_MODEL_CAPACITY") {
		return true
	}
	lower := strings.ToLower(string(body))
	return strings.Contains(lower, "insufficient_model_capacity") ||
		strings.Contains(lower, "experiencing high traffic") ||
		strings.Contains(lower, "insufficient model capacity")
}

func kiroLooksLikeCreditQuotaExceeded(body []byte) bool {
	lower := strings.ToLower(string(body))
	if lower == "" {
		return false
	}
	if strings.Contains(lower, "insufficient_model_capacity") || strings.Contains(lower, "experiencing high traffic") {
		return false
	}
	quotaSignals := []string{
		"quota",
		"credit",
		"credits",
		"usage limit",
		"usage_limit",
		"overage cap",
		"overagecap",
		"limit exceeded",
		"limitexceeded",
		"limit_reached",
	}
	for _, signal := range quotaSignals {
		if strings.Contains(lower, signal) {
			return true
		}
	}
	return false
}

func kiroQuotaExceededMessage(auth *cliproxyauth.Auth, model string) string {
	if auth == nil {
		return "kiro quota exceeded"
	}
	if model == "" {
		return fmt.Sprintf("kiro quota exceeded for auth %s", auth.ID)
	}
	return fmt.Sprintf("kiro quota exceeded for auth %s model %s", auth.ID, model)
}
