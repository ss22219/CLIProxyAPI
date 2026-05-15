package executor

import (
	"context"
	"math"
	"strings"
	"time"

	kiroauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

func (e *KiroExecutor) logKiroCreditUsage(ctx context.Context, auth *cliproxyauth.Auth, token, profileArn, model string, requestPayload []byte, detail usage.Detail, before *kiroauth.CreditUsageSummary) {
	if strings.TrimSpace(token) == "" {
		return
	}
	authID := ""
	authLabel := ""
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
	}
	contextLength := kiroModelContextLength(model)
	localPromptTokens := estimateKiroLocalPromptTokens(model, requestPayload)
	contextExtraTokens := detail.InputTokens - localPromptTokens
	if contextExtraTokens < 0 {
		contextExtraTokens = 0
	}

	fields := log.Fields{
		"auth_id":              authID,
		"auth_label":           authLabel,
		"model":                model,
		"context_length":       contextLength,
		"local_prompt_tokens":  localPromptTokens,
		"input_tokens":         detail.InputTokens,
		"output_tokens":        detail.OutputTokens,
		"total_tokens":         detail.TotalTokens,
		"context_extra_tokens": contextExtraTokens,
	}
	if before != nil {
		addKiroCreditFields(fields, "credit_before", *before)
	}
	log.WithFields(fields).Info("kiro credit usage estimate")

	go e.logKiroCreditUsageAfter(auth, token, profileArn, model, detail, before, localPromptTokens, contextExtraTokens)
}

func (e *KiroExecutor) logKiroCreditUsageAfter(auth *cliproxyauth.Auth, token, profileArn, model string, detail usage.Detail, before *kiroauth.CreditUsageSummary, localPromptTokens, contextExtraTokens int64) {
	proxyURL := ""
	authID := ""
	authLabel := ""
	if auth != nil {
		proxyURL = auth.ProxyURL
		authID = auth.ID
		authLabel = auth.Label
	}
	svc := kiroauth.NewKiroAuthWithProxyURL(e.cfg, proxyURL)
	limits, err := svc.FetchUsageLimits(context.Background(), token, profileArn)
	if err != nil {
		log.WithFields(log.Fields{
			"auth_id": authID,
			"model":   model,
			"error":   err.Error(),
		}).Debug("kiro credit usage after request lookup failed")
		return
	}
	after, ok := limits.CreditUsage(time.Now())
	if !ok {
		log.WithFields(log.Fields{
			"auth_id": authID,
			"model":   model,
		}).Debug("kiro credit usage after request missing CREDIT bucket")
		return
	}
	fields := log.Fields{
		"auth_id":              authID,
		"auth_label":           authLabel,
		"model":                model,
		"local_prompt_tokens":  localPromptTokens,
		"input_tokens":         detail.InputTokens,
		"output_tokens":        detail.OutputTokens,
		"total_tokens":         detail.TotalTokens,
		"context_extra_tokens": contextExtraTokens,
	}
	addKiroCreditFields(fields, "credit_after", after)
	if before != nil {
		delta := after.Current - before.Current
		fields["credit_delta"] = roundKiroCredit(delta)
		fields["credit_delta_per_1k_input_tokens"] = roundKiroCredit(perKiroThousand(delta, detail.InputTokens))
		fields["credit_delta_per_1k_local_prompt_tokens"] = roundKiroCredit(perKiroThousand(delta, localPromptTokens))
		fields["credit_delta_per_1k_context_extra_tokens"] = roundKiroCredit(perKiroThousand(delta, contextExtraTokens))
	}
	log.WithFields(fields).Info("kiro credit usage after request")
}

func addKiroCreditFields(fields log.Fields, prefix string, summary kiroauth.CreditUsageSummary) {
	fields[prefix+"_current"] = roundKiroCredit(summary.Current)
	fields[prefix+"_limit"] = roundKiroCredit(summary.Limit)
	fields[prefix+"_effective_limit"] = roundKiroCredit(summary.EffectiveLimit)
	fields[prefix+"_overage_cap"] = roundKiroCredit(summary.OverageCap)
	fields[prefix+"_overage_status"] = summary.OverageStatus
	fields[prefix+"_plan"] = summary.Plan
	fields[prefix+"_subscription"] = summary.Subscription
	if !summary.NextReset.IsZero() {
		fields[prefix+"_reset_at"] = summary.NextReset.UTC().Format(time.RFC3339)
	}
	if summary.Currency != "" {
		fields[prefix+"_currency"] = summary.Currency
	}
}

func estimateKiroLocalPromptTokens(model string, requestPayload []byte) int64 {
	enc, err := helps.TokenizerForModel(model)
	if err != nil {
		return 0
	}
	n, err := helps.CountOpenAIChatTokens(enc, requestPayload)
	if err != nil {
		return 0
	}
	return n
}

func perKiroThousand(value float64, tokens int64) float64 {
	if tokens <= 0 {
		return 0
	}
	return value / float64(tokens) * 1000
}

func roundKiroCredit(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return math.Round(value*10000) / 10000
}
