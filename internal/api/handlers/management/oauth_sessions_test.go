package management

import "testing"

func TestNormalizeOAuthProviderKiro(t *testing.T) {
	provider, err := NormalizeOAuthProvider(" Kiro ")
	if err != nil {
		t.Fatalf("NormalizeOAuthProvider returned error: %v", err)
	}
	if provider != "kiro" {
		t.Fatalf("provider = %q, want %q", provider, "kiro")
	}
}
