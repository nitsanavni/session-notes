package cloud

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Login-token config: `session-notes login <server-url>` stores a bearer token
// keyed by host in a 0600 file under the user config dir, so remote edit/watch
// pick it up automatically. This is deliberately simple (host -> token); M4's
// multi-user grants can layer identity on top without changing the file shape.

// configPath returns the token store path, honoring $SESSION_NOTES_CONFIG_DIR
// (used by tests) then os.UserConfigDir.
func configPath() (string, error) {
	dir := os.Getenv("SESSION_NOTES_CONFIG_DIR")
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(base, "session-notes")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, "tokens.json"), nil
}

type tokenStore struct {
	Tokens map[string]string `json:"tokens"` // host -> token
}

func loadTokens() (tokenStore, error) {
	ts := tokenStore{Tokens: map[string]string{}}
	path, err := configPath()
	if err != nil {
		return ts, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ts, nil
	}
	if err != nil {
		return ts, err
	}
	_ = json.Unmarshal(data, &ts)
	if ts.Tokens == nil {
		ts.Tokens = map[string]string{}
	}
	return ts, nil
}

// SaveToken stores token for host (0600), replacing any existing entry.
func SaveToken(host, token string) error {
	ts, err := loadTokens()
	if err != nil {
		return err
	}
	ts.Tokens[host] = token
	path, err := configPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(ts, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// TokenFor returns the stored token for host, or "" when none is saved.
func TokenFor(host string) string {
	ts, err := loadTokens()
	if err != nil {
		return ""
	}
	return ts.Tokens[host]
}
