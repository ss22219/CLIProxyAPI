package kiro

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	log "github.com/sirupsen/logrus"
)

// KiroCliCredentials holds credentials read from the kiro-cli SQLite database.
type KiroCliCredentials struct {
	ClientID     string
	ClientSecret string
	Region       string
	AccessToken  string
	RefreshToken string
	ExpiresAt    string
}

// ReadKiroCliCredentials reads credentials from the kiro-cli SQLite database.
// The database is at %LOCALAPPDATA%\Kiro-Cli\data.sqlite3 on Windows.
func ReadKiroCliCredentials() (*KiroCliCredentials, error) {
	dbPath := kiroCliDBPath()
	if dbPath == "" {
		return nil, fmt.Errorf("kiro: cannot determine kiro-cli database path")
	}
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("kiro: kiro-cli database not found at %s", dbPath)
	}

	creds := &KiroCliCredentials{Region: DefaultRegion}

	// Read device-registration (client_id, client_secret, region)
	devReg, err := queryKV(dbPath, "device-registration")
	if err != nil {
		log.Debugf("kiro: failed to read device-registration: %v", err)
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

	// Read token (access_token, refresh_token, expires_at)
	tokenData, err := queryKV(dbPath, "token")
	if err != nil {
		return nil, fmt.Errorf("kiro: failed to read token from kiro-cli: %w", err)
	}
	if tokenData == "" {
		return nil, fmt.Errorf("kiro: no token found in kiro-cli database")
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
	// expires_at may also be a number
	if v, ok := tok["expires_at"].(float64); ok && v > 0 {
		creds.ExpiresAt = fmt.Sprintf("%.0f", v)
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

// queryKV queries the auth_kv table for a given key using the sqlite3 CLI.
func queryKV(dbPath, key string) (string, error) {
	sqlite3Path, err := findSqlite3()
	if err != nil {
		return "", err
	}

	query := fmt.Sprintf("SELECT value FROM auth_kv WHERE key='%s';", key)
	cmd := exec.Command(sqlite3Path, dbPath, query)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("sqlite3 query failed: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// findSqlite3 locates the sqlite3 CLI binary.
func findSqlite3() (string, error) {
	path, err := exec.LookPath("sqlite3")
	if err == nil {
		return path, nil
	}
	// On Windows, check common locations
	if runtime.GOOS == "windows" {
		candidates := []string{
			filepath.Join(os.Getenv("ProgramFiles"), "SQLite", "sqlite3.exe"),
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "sqlite3", "sqlite3.exe"),
		}
		for _, c := range candidates {
			if _, statErr := os.Stat(c); statErr == nil {
				return c, nil
			}
		}
	}
	return "", fmt.Errorf("sqlite3 CLI not found in PATH")
}
