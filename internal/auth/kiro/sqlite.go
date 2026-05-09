package kiro

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	log "github.com/sirupsen/logrus"
	_ "modernc.org/sqlite"
)

// KiroCliCredentials holds credentials read from the kiro-cli SQLite database.
type KiroCliCredentials struct {
	ClientID     string
	ClientSecret string
	Region       string
	AccessToken  string
	RefreshToken string
	ExpiresAt    string
	ProfileArn   string // populated from social:token, state.api.codewhisperer.profile, or BuilderId default
	Provider     string // populated from social:token (e.g., "google")
	AuthMethod   string // "oidc" or "social", inferred from which key contains the token
}

// ReadKiroCliCredentials reads credentials from the kiro-cli SQLite database.
// The database is at %LOCALAPPDATA%\Kiro-Cli\data.sqlite3 on Windows.
//
// The actual auth_kv keys in the sqlite file are:
//   - kirocli:odic:device-registration  (SSO: client_id, client_secret, region)
//   - kirocli:odic:token                (SSO token rows; accessToken starts with "aoa")
//   - kirocli:social:token              (Social/Google/GitHub: token + provider + profile_arn)
//
// Precedence: if kirocli:social:token is present, treat as social login. Otherwise
// fall back to the kirocli:odic:token + kirocli:odic:device-registration pair.
func ReadKiroCliCredentials() (*KiroCliCredentials, error) {
	dbPath := kiroCliDBPath()
	if dbPath == "" {
		return nil, fmt.Errorf("kiro: cannot determine kiro-cli database path")
	}
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("kiro: kiro-cli database not found at %s", dbPath)
	}

	creds := &KiroCliCredentials{Region: DefaultRegion}

	// Prefer social:token if present.
	socialRaw, err := queryKVExact(dbPath, "kirocli:social:token")
	if err != nil {
		log.Debugf("kiro: read social:token failed: %v", err)
	}
	if socialRaw != "" {
		var tok map[string]any
		if errParse := json.Unmarshal([]byte(socialRaw), &tok); errParse == nil {
			creds.AuthMethod = "social"
			if v, ok := tok["access_token"].(string); ok {
				creds.AccessToken = v
			}
			if v, ok := tok["refresh_token"].(string); ok {
				creds.RefreshToken = v
			}
			if v, ok := tok["expires_at"].(string); ok {
				creds.ExpiresAt = v
			}
			if v, ok := tok["profile_arn"].(string); ok {
				creds.ProfileArn = v
			}
			if v, ok := tok["provider"].(string); ok {
				creds.Provider = v
			}
			// Best-effort: also populate ProfileArn from the state table if missing.
			if creds.ProfileArn == "" {
				if arn := readProfileArnFromState(dbPath); arn != "" {
					creds.ProfileArn = arn
				}
			}
			if creds.AccessToken == "" && creds.RefreshToken == "" {
				return nil, fmt.Errorf("kiro: social:token present but missing tokens")
			}
			return creds, nil
		}
	}

	// Fallback to SSO (OIDC) key pair.
	creds.AuthMethod = "oidc"

	devReg, err := queryKVExact(dbPath, "kirocli:odic:device-registration")
	if err != nil {
		log.Debugf("kiro: read device-registration failed: %v", err)
	} else if devReg != "" {
		var reg map[string]any
		if errParse := json.Unmarshal([]byte(devReg), &reg); errParse == nil {
			if v, ok := reg["client_id"].(string); ok {
				creds.ClientID = v
			}
			if v, ok := reg["client_secret"].(string); ok {
				creds.ClientSecret = v
			}
			if v, ok := reg["region"].(string); ok && v != "" {
				creds.Region = v
			}
		}
	}

	tokenData, err := queryKVExact(dbPath, "kirocli:odic:token")
	if err != nil {
		return nil, fmt.Errorf("kiro: failed to read odic:token from kiro-cli: %w", err)
	}
	if tokenData == "" {
		return nil, fmt.Errorf("kiro: no social or odic token found in kiro-cli database")
	}

	var tok map[string]any
	if err = json.Unmarshal([]byte(tokenData), &tok); err != nil {
		return nil, fmt.Errorf("kiro: failed to parse token data: %w", err)
	}
	if v, ok := tok["access_token"].(string); ok {
		creds.AccessToken = v
	}
	if v, ok := tok["refresh_token"].(string); ok {
		creds.RefreshToken = v
	}
	if v, ok := tok["expires_at"].(string); ok {
		creds.ExpiresAt = v
	}
	if v, ok := tok["expires_at"].(float64); ok && v > 0 {
		creds.ExpiresAt = fmt.Sprintf("%.0f", v)
	}
	if v, ok := tok["region"].(string); ok && v != "" {
		creds.Region = v
	}

	if arn := readProfileArnFromState(dbPath); arn != "" {
		creds.ProfileArn = arn
	}
	if creds.ProfileArn == "" {
		creds.ProfileArn = profileArnOrDefault("", creds.AuthMethod)
	}

	if creds.AccessToken == "" && creds.RefreshToken == "" {
		return nil, fmt.Errorf("kiro: kiro-cli database has no usable tokens")
	}

	return creds, nil
}

// kiroCliDBPath returns the path to the kiro-cli SQLite database.
func kiroCliDBPath() string {
	if runtime.GOOS == "windows" {
		localAppData := os.Getenv("LOCALAPPDATA")
		if localAppData == "" {
			return ""
		}
		return filepath.Join(localAppData, "Kiro-Cli", "data.sqlite3")
	}
	// Linux/macOS: ~/.local/share/Kiro-Cli/data.sqlite3
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share", "Kiro-Cli", "data.sqlite3")
}

// queryKVExact queries the auth_kv table for an exact key.
func queryKVExact(dbPath, key string) (string, error) {
	return querySQLiteValue(dbPath, "SELECT value FROM auth_kv WHERE key = ?", key)
}

func queryStateExact(dbPath, key string) (string, error) {
	return querySQLiteValue(dbPath, "SELECT value FROM state WHERE key = ?", key)
}

func querySQLiteValue(dbPath, query, key string) (string, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return "", fmt.Errorf("open sqlite database: %w", err)
	}
	defer func() {
		if errClose := db.Close(); errClose != nil {
			log.Debugf("kiro: failed to close sqlite database: %v", errClose)
		}
	}()

	var value string
	err = db.QueryRow(query, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("sqlite query failed: %w", err)
	}
	return strings.TrimSpace(value), nil
}

// readProfileArnFromState reads api.codewhisperer.profile from the state table
// and extracts the arn field.
func readProfileArnFromState(dbPath string) string {
	raw, err := queryStateExact(dbPath, "api.codewhisperer.profile")
	if err != nil {
		return ""
	}
	if raw == "" {
		return ""
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return ""
	}
	if v, ok := obj["arn"].(string); ok {
		return v
	}
	return ""
}
