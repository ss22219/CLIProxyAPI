package executor

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	kiroauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// KiroExecutor handles Kiro (AWS Q) API requests and token refresh.
type KiroExecutor struct {
	OpenAICompatExecutor
	cfg *config.Config
}

// NewKiroExecutor creates a new Kiro executor.
func NewKiroExecutor(cfg *config.Config) *KiroExecutor {
	return &KiroExecutor{
		OpenAICompatExecutor: *NewOpenAICompatExecutor("kiro", cfg),
		cfg:                  cfg,
	}
}

// Identifier returns the executor identifier.
func (e *KiroExecutor) Identifier() string { return "kiro" }

// PrepareRequest injects Kiro credentials into the outgoing HTTP request.
func (e *KiroExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	token := kiroAccessToken(auth)
	if strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	// Kiro Q API requires specific content type and target header
	if req.Header.Get("x-amz-target") != "" {
		req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// Refresh refreshes the Kiro token using the appropriate method (OIDC or Social).
func (e *KiroExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("kiro executor: refresh called")
	if auth == nil {
		return nil, fmt.Errorf("kiro executor: auth is nil")
	}
	if isKiroAPIKeyAuth(auth) {
		return auth, nil
	}

	storage := metadataToKiroStorage(auth.Metadata)
	if strings.TrimSpace(storage.RefreshToken) == "" {
		return auth, nil
	}

	svc := kiroauth.NewKiroAuthWithProxyURL(e.cfg, auth.ProxyURL)
	result, err := svc.RefreshToken(ctx, storage)
	if err != nil {
		return nil, err
	}

	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = result.AccessToken
	if result.RefreshToken != "" {
		auth.Metadata["refresh_token"] = result.RefreshToken
	}
	if result.ExpiresAt != "" {
		auth.Metadata["expires_at"] = result.ExpiresAt
	}
	auth.Metadata["auth_method"] = result.AuthMethod
	auth.Metadata["type"] = "kiro"
	auth.Metadata["last_refresh"] = time.Now().Format(time.RFC3339)
	if result.ProfileArn != "" {
		auth.Metadata["profile_arn"] = result.ProfileArn
	}
	updateNestedKiroToken(auth.Metadata, result)

	// Fire-and-forget login telemetry
	go kiroauth.SendLoginTelemetry(&http.Client{Timeout: 10 * time.Second}, "")

	return auth, nil
}

func updateNestedKiroToken(metadata map[string]any, result *kiroauth.KiroTokenStorage) {
	if metadata == nil || result == nil {
		return
	}
	tokenMap, _ := metadata["token"].(map[string]any)
	if tokenMap == nil {
		tokenMap = make(map[string]any)
	}
	if result.AccessToken != "" {
		tokenMap["access_token"] = result.AccessToken
	}
	if result.RefreshToken != "" {
		tokenMap["refresh_token"] = result.RefreshToken
	}
	if result.ExpiresAt != "" {
		tokenMap["expires_at"] = result.ExpiresAt
	}
	if len(tokenMap) > 0 {
		metadata["token"] = tokenMap
	}
}

// kiroAccessToken extracts the access token from auth metadata.
func kiroAccessToken(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["api_key"]); v != "" {
			return v
		}
	}
	if auth.Metadata == nil {
		return ""
	}
	if v, ok := auth.Metadata["api_key"].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	if v, ok := auth.Metadata["access_token"].(string); ok && v != "" {
		return v
	}
	// Auth file stores token nested: {"token":{"access_token":"..."}
	if tokenMap, ok := auth.Metadata["token"].(map[string]any); ok {
		if v, ok := tokenMap["access_token"].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// kiroProfileArn extracts the profileArn from auth metadata.
func kiroProfileArn(auth *cliproxyauth.Auth) string {
	if auth == nil || isKiroAPIKeyAuth(auth) || auth.Metadata == nil {
		return ""
	}
	if v, ok := auth.Metadata["profile_arn"].(string); ok {
		return v
	}
	if v, ok := auth.Metadata["auth_method"].(string); ok && strings.EqualFold(v, "oidc") {
		return kiroauth.DefaultBuilderIDProfileArn
	}
	return ""
}

// FetchModels calls the Kiro ListAvailableModels API and returns registry-compatible ModelInfo entries.
func (e *KiroExecutor) FetchModels(ctx context.Context, auth *cliproxyauth.Auth) ([]*registry.ModelInfo, error) {
	var err error
	auth, err = e.ensureFreshKiroAuth(ctx, auth)
	if err != nil {
		return nil, err
	}
	token := kiroAccessToken(auth)
	if token == "" {
		return nil, fmt.Errorf("kiro: no access token for model fetch")
	}
	profileArn := kiroProfileArn(auth)
	svc := kiroauth.NewKiroAuthWithProxyURL(e.cfg, auth.ProxyURL)
	models, err := svc.FetchModels(ctx, token, profileArn)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	out := make([]*registry.ModelInfo, 0, len(models))
	for _, m := range models {
		if strings.TrimSpace(m.ModelID) == "" {
			continue
		}
		displayName := m.ModelName
		if displayName == "" {
			displayName = m.ModelID
		}
		out = append(out, &registry.ModelInfo{
			ID:                  m.ModelID,
			Object:              "model",
			Created:             now,
			OwnedBy:             "amazon",
			Type:                "kiro",
			DisplayName:         displayName,
			Description:         m.Description,
			ContextLength:       m.TokenLimits.MaxInputTokens,
			MaxCompletionTokens: m.TokenLimits.MaxOutputTokens,
			SupportedParameters: kiroSupportedParameters(m),
			Thinking:            kiroThinkingSupport(m),
		})
	}
	return out, nil
}

func kiroSupportedParameters(m kiroauth.KiroModel) []string {
	if len(kiroEffortLevels(m)) == 0 {
		return nil
	}
	return []string{"thinking", "output_config", "reasoning_effort"}
}

func kiroThinkingSupport(m kiroauth.KiroModel) *registry.ThinkingSupport {
	levels := kiroEffortLevels(m)
	defaultEffort := defaultKiroEffort(m.ModelID)
	if defaultEffort != "" && !stringSliceContains(levels, defaultEffort) {
		levels = append(levels, defaultEffort)
	}
	if len(levels) == 0 {
		return nil
	}
	return &registry.ThinkingSupport{
		Levels:  levels,
		Default: defaultEffort,
	}
}

func stringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), want) {
			return true
		}
	}
	return false
}

func kiroEffortLevels(m kiroauth.KiroModel) []string {
	switch strings.ReplaceAll(strings.ToLower(strings.TrimSpace(m.ModelID)), ".", "-") {
	case "claude-opus-4-6":
		return []string{"low", "medium", "high", "max", "xhigh"}
	case "claude-opus-4-7":
		return []string{"low", "medium", "high", "xhigh", "max"}
	case "claude-sonnet-4-6":
		return []string{"low", "medium", "high"}
	}

	enum := gjson.GetBytes(m.AdditionalModelRequestFieldsSchema, "properties.output_config.properties.effort.enum")
	if enum.IsArray() {
		levels := make([]string, 0, len(enum.Array()))
		for _, item := range enum.Array() {
			if value := strings.TrimSpace(item.String()); value != "" {
				levels = append(levels, value)
			}
		}
		if len(levels) > 0 {
			return levels
		}
	}
	return nil
}

// metadataToKiroStorage converts auth metadata to a KiroTokenStorage.
func metadataToKiroStorage(metadata map[string]any) *kiroauth.KiroTokenStorage {
	s := &kiroauth.KiroTokenStorage{
		AuthMethod: "social",
		Region:     kiroauth.DefaultRegion,
		Type:       "kiro",
	}
	if metadata == nil {
		return s
	}
	// Try flat keys first, then nested "token" map
	getString := func(key string) string {
		if v, ok := metadata[key].(string); ok && v != "" {
			return v
		}
		if tokenMap, ok := metadata["token"].(map[string]any); ok {
			if v, ok := tokenMap[key].(string); ok && v != "" {
				return v
			}
		}
		return ""
	}
	s.AccessToken = getString("access_token")
	if s.AccessToken == "" {
		s.AccessToken = getString("api_key")
	}
	s.RefreshToken = getString("refresh_token")
	s.ExpiresAt = getString("expires_at")
	s.ProfileArn = getString("profile_arn")
	if v := getString("auth_method"); v != "" {
		s.AuthMethod = v
	}
	if strings.EqualFold(s.AuthMethod, "api_key") || (s.AccessToken != "" && strings.HasPrefix(s.AccessToken, "ksk_")) {
		s.AuthMethod = "api_key"
		s.ProfileArn = ""
	}
	if strings.EqualFold(s.AuthMethod, "sso") {
		s.AuthMethod = "oidc"
	}
	if s.ProfileArn == "" && s.AuthMethod == "oidc" {
		s.ProfileArn = kiroauth.DefaultBuilderIDProfileArn
	}
	if v := getString("region"); v != "" {
		s.Region = v
	}
	// idc_region is at top level in auth file
	if v, ok := metadata["idc_region"].(string); ok && v != "" {
		s.Region = v
	}
	s.ClientID = getString("client_id")
	s.ClientSecret = getString("client_secret")
	return s
}

func isKiroAPIKeyAuth(auth *cliproxyauth.Auth) bool {
	if auth == nil {
		return false
	}
	if auth.Attributes != nil && strings.TrimSpace(auth.Attributes["api_key"]) != "" {
		return true
	}
	if auth.Metadata == nil {
		return false
	}
	if v, ok := auth.Metadata["auth_method"].(string); ok && strings.EqualFold(strings.TrimSpace(v), "api_key") {
		return true
	}
	if v, ok := auth.Metadata["api_key"].(string); ok && strings.TrimSpace(v) != "" {
		return true
	}
	return false
}
