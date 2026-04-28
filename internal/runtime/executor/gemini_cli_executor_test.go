package executor

import (
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestGeminiCLIExpiryParsesNativeExpiryDate(t *testing.T) {
	want := time.UnixMilli(1770000000123)

	for _, tc := range []struct {
		name     string
		metadata map[string]any
	}{
		{
			name: "top level number",
			metadata: map[string]any{
				"expiry_date": float64(want.UnixMilli()),
			},
		},
		{
			name: "nested token string",
			metadata: map[string]any{
				"token": map[string]any{
					"expiry_date": "1770000000123",
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := geminiCLIExpiry(tc.metadata)
			if !ok {
				t.Fatal("geminiCLIExpiry() did not parse expiry_date")
			}
			if !got.Equal(want) {
				t.Fatalf("geminiCLIExpiry() = %s, want %s", got, want)
			}
		})
	}
}

func TestBuildGeminiTokenMapWritesNativeExpiryDate(t *testing.T) {
	expiry := time.UnixMilli(1770000000123)
	got := buildGeminiTokenMap(map[string]any{"expiry_date": float64(1)}, &oauth2.Token{
		AccessToken:  "access",
		RefreshToken: "refresh",
		Expiry:       expiry,
	})

	if got["expiry_date"] != expiry.UnixMilli() {
		t.Fatalf("expiry_date = %#v, want %d", got["expiry_date"], expiry.UnixMilli())
	}
}
