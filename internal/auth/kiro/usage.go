package kiro

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// UsageLimitsResponse is returned by the Kiro GetUsageLimits API.
type UsageLimitsResponse struct {
	DaysUntilReset       int           `json:"daysUntilReset"`
	NextDateReset        UnixTimestamp `json:"nextDateReset"`
	OverageConfiguration struct {
		OverageStatus string `json:"overageStatus"`
	} `json:"overageConfiguration"`
	SubscriptionInfo struct {
		SubscriptionTitle string `json:"subscriptionTitle"`
		Type              string `json:"type"`
	} `json:"subscriptionInfo"`
	UsageBreakdownList []UsageBreakdown `json:"usageBreakdownList"`
}

// UsageBreakdown describes usage for one Kiro resource.
type UsageBreakdown struct {
	ResourceType                 string        `json:"resourceType"`
	DisplayName                  string        `json:"displayName"`
	CurrentUsage                 int64         `json:"currentUsage"`
	CurrentUsageWithPrecision    float64       `json:"currentUsageWithPrecision"`
	UsageLimit                   int64         `json:"usageLimit"`
	UsageLimitWithPrecision      float64       `json:"usageLimitWithPrecision"`
	CurrentOverages              int64         `json:"currentOverages"`
	CurrentOveragesWithPrecision float64       `json:"currentOveragesWithPrecision"`
	OverageCap                   int64         `json:"overageCap"`
	OverageCapWithPrecision      float64       `json:"overageCapWithPrecision"`
	OverageCharges               float64       `json:"overageCharges"`
	Currency                     string        `json:"currency"`
	NextDateReset                UnixTimestamp `json:"nextDateReset"`
}

// UnixTimestamp accepts upstream epoch values encoded as integers, strings, or scientific-notation numbers.
type UnixTimestamp int64

// UnmarshalJSON decodes Kiro reset timestamps. Upstream sometimes emits values like 1.780272E9.
func (t *UnixTimestamp) UnmarshalJSON(data []byte) error {
	raw := strings.TrimSpace(string(data))
	if raw == "" || raw == "null" {
		*t = 0
		return nil
	}
	if strings.HasPrefix(raw, `"`) {
		var strValue string
		if err := json.Unmarshal(data, &strValue); err != nil {
			return fmt.Errorf("invalid unix timestamp string: %w", err)
		}
		raw = strings.TrimSpace(strValue)
		if raw == "" {
			*t = 0
			return nil
		}
	}
	if value, err := strconv.ParseInt(raw, 10, 64); err == nil {
		*t = UnixTimestamp(value)
		return nil
	}
	floatValue, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return fmt.Errorf("invalid unix timestamp %q: %w", raw, err)
	}
	if math.IsNaN(floatValue) || math.IsInf(floatValue, 0) || floatValue < 0 || floatValue > float64(math.MaxInt64) {
		return fmt.Errorf("invalid unix timestamp %q", raw)
	}
	*t = UnixTimestamp(int64(math.Round(floatValue)))
	return nil
}

// CreditUsageSummary is a normalized view of the CREDIT quota bucket.
type CreditUsageSummary struct {
	Current        float64
	Limit          float64
	OverageCap     float64
	EffectiveLimit float64
	NextReset      time.Time
	Plan           string
	Subscription   string
	OverageStatus  string
	Currency       string
}

// FetchUsageLimits calls the Kiro GetUsageLimits API.
func (k *KiroAuth) FetchUsageLimits(ctx context.Context, accessToken, profileArn string) (*UsageLimitsResponse, error) {
	if strings.TrimSpace(accessToken) == "" {
		return nil, fmt.Errorf("kiro: access token required for GetUsageLimits")
	}
	trimmedProfileArn := strings.TrimSpace(profileArn)

	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, usageLimitsURL(DefaultRegion, trimmedProfileArn), nil)
	if err != nil {
		return nil, fmt.Errorf("kiro: create GetUsageLimits request: %w", err)
	}
	SetUsageLimitsHeaders(req, accessToken)
	req.Close = true

	resp, err := k.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kiro: GetUsageLimits request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respReader := io.Reader(resp.Body)
	if strings.EqualFold(strings.TrimSpace(resp.Header.Get("Content-Encoding")), "gzip") {
		gzipReader, errGzip := gzip.NewReader(resp.Body)
		if errGzip != nil {
			return nil, fmt.Errorf("kiro: decode GetUsageLimits gzip response: %w", errGzip)
		}
		defer func() { _ = gzipReader.Close() }()
		respReader = gzipReader
	}

	respBody, err := io.ReadAll(respReader)
	if err != nil {
		return nil, fmt.Errorf("kiro: read GetUsageLimits response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		log.Debugf("kiro: GetUsageLimits failed (status %d): %s", resp.StatusCode, string(respBody))
		return nil, fmt.Errorf("kiro: GetUsageLimits failed (status %d): %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var result UsageLimitsResponse
	if err = json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("kiro: parse GetUsageLimits response: %w", err)
	}
	return &result, nil
}

// CreditExhausted reports whether the CREDIT resource has reached its usable cap.
func (r *UsageLimitsResponse) CreditExhausted(now time.Time) (bool, time.Time, string) {
	if r == nil {
		return false, time.Time{}, ""
	}
	summary, ok := r.CreditUsage(now)
	if !ok || summary.Limit <= 0 {
		return false, time.Time{}, ""
	}
	if summary.Current < summary.EffectiveLimit {
		return false, time.Time{}, ""
	}
	resetAt := summary.NextReset
	if resetAt.IsZero() || !resetAt.After(now) {
		resetAt = now.Add(30 * time.Minute)
	}
	msg := fmt.Sprintf("kiro API key credit quota exceeded: %.2f/%.2f credits used; resets at %s", summary.Current, summary.EffectiveLimit, resetAt.UTC().Format(time.RFC3339))
	return true, resetAt, msg
}

// CreditUsage returns the normalized CREDIT resource usage, when present.
func (r *UsageLimitsResponse) CreditUsage(now time.Time) (CreditUsageSummary, bool) {
	if r == nil {
		return CreditUsageSummary{}, false
	}
	for _, item := range r.UsageBreakdownList {
		if !strings.EqualFold(strings.TrimSpace(item.ResourceType), "CREDIT") {
			continue
		}
		current := firstPositiveFloat(item.CurrentUsageWithPrecision, float64(item.CurrentUsage))
		limit := firstPositiveFloat(item.UsageLimitWithPrecision, float64(item.UsageLimit))
		overageCap := firstPositiveFloat(item.OverageCapWithPrecision, float64(item.OverageCap))
		effectiveLimit := limit
		overageStatus := strings.TrimSpace(r.OverageConfiguration.OverageStatus)
		if strings.EqualFold(overageStatus, "ENABLED") && overageCap > 0 {
			effectiveLimit += overageCap
		}
		resetUnix := int64(item.NextDateReset)
		if resetUnix <= 0 {
			resetUnix = int64(r.NextDateReset)
		}
		resetAt := time.Time{}
		if resetUnix > 0 {
			resetAt = time.Unix(resetUnix, 0)
		}
		if resetAt.IsZero() || !resetAt.After(now) {
			resetAt = now.Add(30 * time.Minute)
		}
		return CreditUsageSummary{
			Current:        current,
			Limit:          limit,
			OverageCap:     overageCap,
			EffectiveLimit: effectiveLimit,
			NextReset:      resetAt,
			Plan:           strings.TrimSpace(r.SubscriptionInfo.Type),
			Subscription:   strings.TrimSpace(r.SubscriptionInfo.SubscriptionTitle),
			OverageStatus:  overageStatus,
			Currency:       strings.TrimSpace(item.Currency),
		}, true
	}
	return CreditUsageSummary{}, false
}

func firstPositiveFloat(values ...float64) float64 {
	for _, value := range values {
		if !math.IsNaN(value) && !math.IsInf(value, 0) && value > 0 {
			return value
		}
	}
	return 0
}

func usageLimitsURL(region, profileArn string) string {
	if strings.TrimSpace(region) == "" {
		region = DefaultRegion
	}
	u := url.URL{
		Scheme: "https",
		Host:   fmt.Sprintf("q.%s.amazonaws.com", region),
		Path:   "/getUsageLimits",
	}
	q := u.Query()
	q.Set("origin", "AI_EDITOR")
	q.Set("resourceType", "AGENTIC_REQUEST")
	if trimmedArn := strings.TrimSpace(profileArn); trimmedArn != "" {
		q.Set("profileArn", trimmedArn)
	}
	u.RawQuery = q.Encode()
	return u.String()
}
