package management

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestAPICallTransportDirectBypassesGlobalProxy(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
		},
	}

	transport := h.apiCallTransport(&coreauth.Auth{ProxyURL: "direct"})
	httpTransport, ok := transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", transport)
	}
	if httpTransport.Proxy != nil {
		t.Fatal("expected direct transport to disable proxy function")
	}
}

func TestAPICallTransportInvalidAuthFallsBackToGlobalProxy(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
		},
	}

	transport := h.apiCallTransport(&coreauth.Auth{ProxyURL: "bad-value"})
	httpTransport, ok := transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", transport)
	}

	req, errRequest := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if errRequest != nil {
		t.Fatalf("http.NewRequest returned error: %v", errRequest)
	}

	proxyURL, errProxy := httpTransport.Proxy(req)
	if errProxy != nil {
		t.Fatalf("httpTransport.Proxy returned error: %v", errProxy)
	}
	if proxyURL == nil || proxyURL.String() != "http://global-proxy.example.com:8080" {
		t.Fatalf("proxy URL = %v, want http://global-proxy.example.com:8080", proxyURL)
	}
}

func TestAPICallTransportAPIKeyAuthFallsBackToConfigProxyURL(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
			GeminiKey: []config.GeminiKey{{
				APIKey:   "gemini-key",
				ProxyURL: "http://gemini-proxy.example.com:8080",
			}},
			ClaudeKey: []config.ClaudeKey{{
				APIKey:   "claude-key",
				ProxyURL: "http://claude-proxy.example.com:8080",
			}},
			CodexKey: []config.CodexKey{{
				APIKey:   "codex-key",
				ProxyURL: "http://codex-proxy.example.com:8080",
			}},
			OpenAICompatibility: []config.OpenAICompatibility{{
				Name:    "bohe",
				BaseURL: "https://bohe.example.com",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{{
					APIKey:   "compat-key",
					ProxyURL: "http://compat-proxy.example.com:8080",
				}},
			}},
		},
	}

	cases := []struct {
		name      string
		auth      *coreauth.Auth
		wantProxy string
	}{
		{
			name: "gemini",
			auth: &coreauth.Auth{
				Provider:   "gemini",
				Attributes: map[string]string{"api_key": "gemini-key"},
			},
			wantProxy: "http://gemini-proxy.example.com:8080",
		},
		{
			name: "claude",
			auth: &coreauth.Auth{
				Provider:   "claude",
				Attributes: map[string]string{"api_key": "claude-key"},
			},
			wantProxy: "http://claude-proxy.example.com:8080",
		},
		{
			name: "codex",
			auth: &coreauth.Auth{
				Provider:   "codex",
				Attributes: map[string]string{"api_key": "codex-key"},
			},
			wantProxy: "http://codex-proxy.example.com:8080",
		},
		{
			name: "openai-compatibility",
			auth: &coreauth.Auth{
				Provider: "bohe",
				Attributes: map[string]string{
					"api_key":      "compat-key",
					"compat_name":  "bohe",
					"provider_key": "bohe",
				},
			},
			wantProxy: "http://compat-proxy.example.com:8080",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			transport := h.apiCallTransport(tc.auth)
			httpTransport, ok := transport.(*http.Transport)
			if !ok {
				t.Fatalf("transport type = %T, want *http.Transport", transport)
			}

			req, errRequest := http.NewRequest(http.MethodGet, "https://example.com", nil)
			if errRequest != nil {
				t.Fatalf("http.NewRequest returned error: %v", errRequest)
			}

			proxyURL, errProxy := httpTransport.Proxy(req)
			if errProxy != nil {
				t.Fatalf("httpTransport.Proxy returned error: %v", errProxy)
			}
			if proxyURL == nil || proxyURL.String() != tc.wantProxy {
				t.Fatalf("proxy URL = %v, want %s", proxyURL, tc.wantProxy)
			}
		})
	}
}

func TestKiroStorageFromAPICallMetadataNormalizesAgentsNetFields(t *testing.T) {
	t.Parallel()

	storage := kiroStorageFromAPICallMetadata(map[string]any{
		"accessToken":  "access",
		"refreshToken": "refresh",
		"authMethod":   "builderid",
		"idcRegion":    "us-west-2",
		"clientId":     "client-id",
		"clientSecret": "client-secret",
		"expiresAt":    "2026-05-09T10:00:00Z",
	})

	if storage.AccessToken != "access" {
		t.Fatalf("AccessToken = %q, want access", storage.AccessToken)
	}
	if storage.RefreshToken != "refresh" {
		t.Fatalf("RefreshToken = %q, want refresh", storage.RefreshToken)
	}
	if storage.AuthMethod != "oidc" {
		t.Fatalf("AuthMethod = %q, want oidc", storage.AuthMethod)
	}
	if storage.Region != "us-west-2" {
		t.Fatalf("Region = %q, want us-west-2", storage.Region)
	}
	if storage.ClientID != "client-id" || storage.ClientSecret != "client-secret" {
		t.Fatalf("client material = %q/%q, want client-id/client-secret", storage.ClientID, storage.ClientSecret)
	}
	if storage.ProfileArn == "" {
		t.Fatal("ProfileArn should default for builderid/OIDC credentials")
	}
}

func TestKiroStorageFromAPICallMetadataReadsNestedSocialToken(t *testing.T) {
	t.Parallel()

	storage := kiroStorageFromAPICallMetadata(map[string]any{
		"auth_method": "social",
		"token": map[string]any{
			"access_token":  "nested-access",
			"refresh_token": "nested-refresh",
			"expires_at":    "2026-05-09T10:00:00Z",
			"profile_arn":   "arn:test",
		},
	})

	if storage.AccessToken != "nested-access" {
		t.Fatalf("AccessToken = %q, want nested-access", storage.AccessToken)
	}
	if storage.RefreshToken != "nested-refresh" {
		t.Fatalf("RefreshToken = %q, want nested-refresh", storage.RefreshToken)
	}
	if storage.ProfileArn != "arn:test" {
		t.Fatalf("ProfileArn = %q, want arn:test", storage.ProfileArn)
	}
	if storage.AuthMethod != "social" {
		t.Fatalf("AuthMethod = %q, want social", storage.AuthMethod)
	}
}

func TestKiroAPICallAPIKeyDoesNotUseProfileArnOrRefresh(t *testing.T) {
	t.Parallel()

	auth := &coreauth.Auth{
		Provider:   "kiro",
		Attributes: map[string]string{"api_key": "ksk_test"},
		Metadata: map[string]any{
			"type":        "kiro",
			"auth_method": "api_key",
			"api_key":     "ksk_test",
		},
	}
	h := &Handler{}

	token, errToken := h.resolveTokenForAuth(context.Background(), auth)
	if errToken != nil {
		t.Fatalf("resolveTokenForAuth returned error: %v", errToken)
	}
	if token != "ksk_test" {
		t.Fatalf("token = %q, want ksk_test", token)
	}

	profileArn, errProfile := h.resolveProfileArnForAuth(context.Background(), auth)
	if errProfile != nil {
		t.Fatalf("resolveProfileArnForAuth returned error: %v", errProfile)
	}
	if profileArn != "" {
		t.Fatalf("profileArn = %q, want empty", profileArn)
	}
}

func TestApplyAPICallReplacementsHandlesEscapedProfileArn(t *testing.T) {
	t.Parallel()

	replacements := map[string]string{
		"$PROFILE_ARN$": "arn:aws:codewhisperer:us-east-1:123:profile/A/B",
	}
	got := applyAPICallReplacements(
		"https://q.us-east-1.amazonaws.com/?origin=KIRO_CLI&profileArn=%24PROFILE_ARN%24",
		replacements,
	)
	want := "https://q.us-east-1.amazonaws.com/?origin=KIRO_CLI&profileArn=arn%3Aaws%3Acodewhisperer%3Aus-east-1%3A123%3Aprofile%2FA%2FB"
	if got != want {
		t.Fatalf("replacement = %q, want %q", got, want)
	}
}

func TestAuthByIndexDistinguishesSharedAPIKeysAcrossProviders(t *testing.T) {
	t.Parallel()

	manager := coreauth.NewManager(nil, nil, nil)
	geminiAuth := &coreauth.Auth{
		ID:       "gemini:apikey:123",
		Provider: "gemini",
		Attributes: map[string]string{
			"api_key": "shared-key",
		},
	}
	compatAuth := &coreauth.Auth{
		ID:       "openai-compatibility:bohe:456",
		Provider: "bohe",
		Label:    "bohe",
		Attributes: map[string]string{
			"api_key":      "shared-key",
			"compat_name":  "bohe",
			"provider_key": "bohe",
		},
	}

	if _, errRegister := manager.Register(context.Background(), geminiAuth); errRegister != nil {
		t.Fatalf("register gemini auth: %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), compatAuth); errRegister != nil {
		t.Fatalf("register compat auth: %v", errRegister)
	}

	geminiIndex := geminiAuth.EnsureIndex()
	compatIndex := compatAuth.EnsureIndex()
	if geminiIndex == compatIndex {
		t.Fatalf("shared api key produced duplicate auth_index %q", geminiIndex)
	}

	h := &Handler{authManager: manager}

	gotGemini := h.authByIndex(geminiIndex)
	if gotGemini == nil {
		t.Fatal("expected gemini auth by index")
	}
	if gotGemini.ID != geminiAuth.ID {
		t.Fatalf("authByIndex(gemini) returned %q, want %q", gotGemini.ID, geminiAuth.ID)
	}

	gotCompat := h.authByIndex(compatIndex)
	if gotCompat == nil {
		t.Fatal("expected compat auth by index")
	}
	if gotCompat.ID != compatAuth.ID {
		t.Fatalf("authByIndex(compat) returned %q, want %q", gotCompat.ID, compatAuth.ID)
	}
}
