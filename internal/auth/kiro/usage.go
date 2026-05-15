package kiro

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// UsageLimitsResponse is returned by the Kiro GetUsageLimits API.
type UsageLimitsResponse struct {
	DaysUntilReset       int   `json:"daysUntilReset"`
	NextDateReset        int64 `json:"nextDateReset"`
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
	ResourceType                 string  `json:"resourceType"`
	DisplayName                  string  `json:"displayName"`
	CurrentUsage                 int64   `json:"currentUsage"`
	CurrentUsageWithPrecision    float64 `json:"currentUsageWithPrecision"`
	UsageLimit                   int64   `json:"usageLimit"`
	UsageLimitWithPrecision      float64 `json:"usageLimitWithPrecision"`
	CurrentOverages              int64   `json:"currentOverages"`
	CurrentOveragesWithPrecision float64 `json:"currentOveragesWithPrecision"`
	OverageCap                   int64   `json:"overageCap"`
	OverageCapWithPrecision      float64 `json:"overageCapWithPrecision"`
	OverageCharges               float64 `json:"overageCharges"`
	Currency                     string  `json:"currency"`
	NextDateReset                int64   `json:"nextDateReset"`
}

// FetchUsageLimits calls the Kiro GetUsageLimits API.
func (k *KiroAuth) FetchUsageLimits(ctx context.Context, accessToken, profileArn string) (*UsageLimitsResponse, error) {
	if strings.TrimSpace(accessToken) == "" {
		return nil, fmt.Errorf("kiro: access token required for GetUsageLimits")
	}
	trimmedProfileArn := strings.TrimSpace(profileArn)

	body := map[string]string{"origin": "KIRO_CLI"}
	if trimmedProfileArn != "" {
		body["profileArn"] = trimmedProfileArn
	}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("kiro: marshal GetUsageLimits request: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, usageLimitsURL(DefaultRegion, trimmedProfileArn), bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("kiro: create GetUsageLimits request: %w", err)
	}
	SetUsageLimitsHeaders(req, accessToken)

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
	for _, item := range r.UsageBreakdownList {
		if !strings.EqualFold(strings.TrimSpace(item.ResourceType), "CREDIT") {
			continue
		}
		current := firstPositiveFloat(item.CurrentUsageWithPrecision, float64(item.CurrentUsage))
		limit := firstPositiveFloat(item.UsageLimitWithPrecision, float64(item.UsageLimit))
		if limit <= 0 {
			return false, time.Time{}, ""
		}
		capValue := limit
		overageEnabled := strings.EqualFold(strings.TrimSpace(r.OverageConfiguration.OverageStatus), "ENABLED")
		overageCap := firstPositiveFloat(item.OverageCapWithPrecision, float64(item.OverageCap))
		if overageEnabled && overageCap > 0 {
			capValue += overageCap
		}
		if current < capValue {
			return false, time.Time{}, ""
		}
		resetUnix := item.NextDateReset
		if resetUnix <= 0 {
			resetUnix = r.NextDateReset
		}
		resetAt := time.Unix(resetUnix, 0)
		if resetUnix <= 0 || !resetAt.After(now) {
			resetAt = now.Add(30 * time.Minute)
		}
		msg := fmt.Sprintf("kiro API key credit quota exceeded: %.2f/%.2f credits used; resets at %s", current, capValue, resetAt.UTC().Format(time.RFC3339))
		return true, resetAt, msg
	}
	return false, time.Time{}, ""
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
		Path:   "/",
	}
	q := u.Query()
	q.Set("origin", "KIRO_CLI")
	q.Set("isEmailRequired", "true")
	if trimmedArn := strings.TrimSpace(profileArn); trimmedArn != "" {
		q.Set("profileArn", trimmedArn)
	}
	u.RawQuery = q.Encode()
	return u.String()
}
