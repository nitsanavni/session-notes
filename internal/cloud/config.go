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

// configDir returns the config directory, honoring $SESSION_NOTES_CONFIG_DIR
// (used by tests) then os.UserConfigDir/session-notes. It is created 0700.
func configDir() (string, error) {
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
	return dir, nil
}

// configPath returns the token store path (in configDir).
func configPath() (string, error) {
	dir, err := configDir()
	if err != nil {
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

// Per-directory board links: `session-notes link <ref>` pins a default board ref
// for a working directory, so `edit`/`watch` with no --board use it. Stored as a
// map of absolute directory -> ref in links.json (same dir as tokens.json, 0600).
// Lookups walk up parent directories (git-style) to find the nearest link.

func linkPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "links.json"), nil
}

type linkStore struct {
	Links map[string]string `json:"links"` // absolute dir -> board ref
}

func loadLinks() (linkStore, error) {
	ls := linkStore{Links: map[string]string{}}
	path, err := linkPath()
	if err != nil {
		return ls, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ls, nil
	}
	if err != nil {
		return ls, err
	}
	_ = json.Unmarshal(data, &ls)
	if ls.Links == nil {
		ls.Links = map[string]string{}
	}
	return ls, nil
}

func saveLinks(ls linkStore) error {
	path, err := linkPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(ls, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// SaveLink pins ref as the default board for the absolute directory dir.
func SaveLink(dir, ref string) error {
	ls, err := loadLinks()
	if err != nil {
		return err
	}
	ls.Links[dir] = ref
	return saveLinks(ls)
}

// RemoveLink drops dir's link. ok reports whether an entry existed.
func RemoveLink(dir string) (ok bool, err error) {
	ls, err := loadLinks()
	if err != nil {
		return false, err
	}
	if _, ok = ls.Links[dir]; !ok {
		return false, nil
	}
	delete(ls.Links, dir)
	return true, saveLinks(ls)
}

// LinkFor returns the board ref linked to dir or its nearest ancestor directory,
// git-style. source is the directory the link was found on ("" when none).
func LinkFor(dir string) (ref, source string, ok bool) {
	ls, err := loadLinks()
	if err != nil {
		return "", "", false
	}
	for d := dir; ; {
		if r, found := ls.Links[d]; found {
			return r, d, true
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", "", false
		}
		d = parent
	}
}
