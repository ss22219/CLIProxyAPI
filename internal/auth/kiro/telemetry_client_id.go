package kiro

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
)

var (
	telemetryClientIDOnce  sync.Once
	telemetryClientIDCache string
)

// GetTelemetryClientID returns a stable telemetry clientId.
// It tries (in order):
//  1. Kiro CLI sqlite state table (telemetryClientId)
//  2. Persisted file at ~/.agents/kiro-telemetry-client-id
//  3. Generate a new UUID and persist it
func GetTelemetryClientID() string {
	telemetryClientIDOnce.Do(func() {
		if id := readTelemetryClientIDFromSqlite(); id != "" {
			telemetryClientIDCache = id
			return
		}
		fp := telemetryClientIDFilePath()
		if data, err := os.ReadFile(fp); err == nil {
			if id := strings.TrimSpace(string(data)); id != "" {
				telemetryClientIDCache = id
				return
			}
		}
		telemetryClientIDCache = uuid.New().String()
		if fp != "" {
			_ = os.MkdirAll(filepath.Dir(fp), 0o700)
			_ = os.WriteFile(fp, []byte(telemetryClientIDCache), 0o600)
		}
	})
	return telemetryClientIDCache
}

// ResetTelemetryClientIDCache resets the cached value (for testing only).
func ResetTelemetryClientIDCache() {
	telemetryClientIDOnce = sync.Once{}
	telemetryClientIDCache = ""
}

func readTelemetryClientIDFromSqlite() string {
	dbPath := kiroCliDBPath()
	if dbPath == "" {
		return ""
	}
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return ""
	}
	val, err := queryStateExact(dbPath, "telemetryClientId")
	if err != nil {
		log.Debugf("kiro: failed to read telemetryClientId from sqlite: %v", err)
		return ""
	}
	val = strings.TrimSpace(val)
	if val == "" {
		return ""
	}
	// Value may be JSON-quoted string
	var unquoted string
	if err := json.Unmarshal([]byte(val), &unquoted); err == nil && unquoted != "" {
		return unquoted
	}
	return val
}

func telemetryClientIDFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".agents", "kiro-telemetry-client-id")
}
