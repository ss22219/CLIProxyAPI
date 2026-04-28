package kiro

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGetTelemetryClientID_StableAcrossCalls(t *testing.T) {
	ResetTelemetryClientIDCache()
	defer ResetTelemetryClientIDCache()

	// Use a temp dir for the persisted file
	tmp := t.TempDir()
	origHome := overrideHomeDir(t, tmp)
	defer restoreHomeDir(origHome)

	id1 := GetTelemetryClientID()
	if id1 == "" {
		t.Fatal("expected non-empty clientId")
	}
	id2 := GetTelemetryClientID()
	if id1 != id2 {
		t.Errorf("clientId not stable: %q vs %q", id1, id2)
	}
}

func TestGetTelemetryClientID_PersistsToFile(t *testing.T) {
	ResetTelemetryClientIDCache()
	defer ResetTelemetryClientIDCache()

	tmp := t.TempDir()
	origHome := overrideHomeDir(t, tmp)
	defer restoreHomeDir(origHome)

	id1 := GetTelemetryClientID()

	// Verify file was written
	fp := filepath.Join(tmp, ".agents", "kiro-telemetry-client-id")
	data, err := os.ReadFile(fp)
	if err != nil {
		t.Fatalf("expected persisted file at %s: %v", fp, err)
	}
	if string(data) != id1 {
		t.Errorf("persisted value %q != returned %q", string(data), id1)
	}

	// Reset cache, should read from file
	ResetTelemetryClientIDCache()
	id2 := GetTelemetryClientID()
	if id1 != id2 {
		t.Errorf("after reset, clientId changed: %q vs %q", id1, id2)
	}
}

func TestGetTelemetryClientID_UsesExistingFile(t *testing.T) {
	ResetTelemetryClientIDCache()
	defer ResetTelemetryClientIDCache()

	tmp := t.TempDir()
	origHome := overrideHomeDir(t, tmp)
	defer restoreHomeDir(origHome)

	// Pre-write a known ID
	fp := filepath.Join(tmp, ".agents", "kiro-telemetry-client-id")
	_ = os.MkdirAll(filepath.Dir(fp), 0o700)
	_ = os.WriteFile(fp, []byte("pre-existing-id-12345"), 0o600)

	id := GetTelemetryClientID()
	if id != "pre-existing-id-12345" {
		t.Errorf("expected pre-existing-id-12345, got %q", id)
	}
}

// overrideHomeDir sets HOME/USERPROFILE to tmp for testing.
// Returns the original values.
func overrideHomeDir(t *testing.T, tmp string) map[string]string {
	t.Helper()
	orig := map[string]string{
		"HOME":        os.Getenv("HOME"),
		"USERPROFILE": os.Getenv("USERPROFILE"),
	}
	os.Setenv("HOME", tmp)
	os.Setenv("USERPROFILE", tmp)
	return orig
}

func restoreHomeDir(orig map[string]string) {
	for k, v := range orig {
		if v == "" {
			os.Unsetenv(k)
		} else {
			os.Setenv(k, v)
		}
	}
}
