// Package update implements the latest-release staleness check that powers the
// upgrade hint and the `session-notes upgrade` subcommand.
//
// CRITICAL: this package must never be reachable from the Claude Code hook path
// (internal/hooks must not import it). Hooks must be fast and offline-safe; the
// staleness check does network I/O. It is only invoked from the --version case
// and from the TUI model's Init (off the UI thread).
package update

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/nitsanavni/session-notes/internal/board"
)

const repoSlug = "nitsanavni/session-notes"

// apiURL is the latest-release endpoint. It is a package var so tests can point
// it at an httptest.Server; at runtime SESSION_NOTES_UPDATE_URL overrides it.
var apiURL = "https://api.github.com/repos/" + repoSlug + "/releases/latest"

// endpoint resolves the API URL, preferring the env override so tests and
// power users can redirect it without recompiling.
func endpoint() string {
	if u := os.Getenv("SESSION_NOTES_UPDATE_URL"); u != "" {
		return u
	}
	return apiURL
}

// userAgentVersion is the version string embedded in the User-Agent header. It
// is set by Hint before it calls LatestTag so the request identifies the
// installed build.
var userAgentVersion = "unknown"

// cacheTTL is how long a cached latest tag is trusted before we re-check.
const cacheTTL = 24 * time.Hour

// semverRe extracts the leading vMAJOR.MINOR.PATCH from a tag. It tolerates the
// `git describe` suffix, so it matches both "v0.1.0" and "v0.1.0-1-gabc-dirty".
var semverRe = regexp.MustCompile(`^(v\d+)\.(\d+)\.(\d+)`)

// cacheFile is the on-disk record of the last successful/attempted check.
type cacheFile struct {
	CheckedAt string `json:"checked_at"`
	Latest    string `json:"latest"`
}

func cachePath() string { return filepath.Join(board.StateDir(), "update-check.json") }

// releaseResp is the sliver of the GitHub release JSON we consume.
type releaseResp struct {
	TagName string `json:"tag_name"`
}

// newer reports whether latest is a strictly higher release than current.
// It is intentionally conservative: any ambiguity (dev build, unparseable tag)
// yields false so we never nag on a build we can't reason about. Versions are
// always compared numerically, never as strings.
func newer(current, latest string) bool {
	// 1. Skip before any network for builds we can't or shouldn't compare.
	if current == "" || current == "unknown" || (len(current) >= 4 && current[:4] == "dev-") {
		return false
	}
	cur, ok := parseSemver(current)
	if !ok {
		return false
	}
	lat, ok := parseSemver(latest)
	if !ok {
		return false
	}
	for i := 0; i < 3; i++ {
		if lat[i] != cur[i] {
			return lat[i] > cur[i]
		}
	}
	return false
}

// parseSemver pulls the three numeric components out of a tag's leading semver.
func parseSemver(s string) ([3]int, bool) {
	m := semverRe.FindStringSubmatch(s)
	if m == nil {
		return [3]int{}, false
	}
	var out [3]int
	// m[1] is "vN"; strip the leading 'v' before parsing.
	major, err := strconv.Atoi(m[1][1:])
	if err != nil {
		return [3]int{}, false
	}
	out[0] = major
	for i := 2; i <= 3; i++ {
		n, err := strconv.Atoi(m[i])
		if err != nil {
			return [3]int{}, false
		}
		out[i-1] = n
	}
	return out, true
}

// LatestTag returns the newest release tag, using a 24h on-disk cache to avoid
// hammering the API. It never returns an error to the caller: every failure
// (offline, non-200, malformed JSON) is silent. On failure it still bumps
// checked_at (retry at most once per day) while preserving the last known tag.
func LatestTag() string {
	if os.Getenv("SESSION_NOTES_NO_UPDATE_CHECK") != "" {
		return ""
	}

	cached, cachedAt, haveCache := readCache()
	if haveCache && time.Since(cachedAt) < cacheTTL {
		return cached
	}

	tag, err := fetchLatest()
	if err != nil || tag == "" {
		// Failure: bump checked_at so we don't retry until the TTL lapses, but
		// keep whatever tag we last knew (may be empty).
		writeCache(cacheFile{CheckedAt: time.Now().Format(time.RFC3339), Latest: cached})
		return cached
	}
	writeCache(cacheFile{CheckedAt: time.Now().Format(time.RFC3339), Latest: tag})
	return tag
}

// fetchLatest performs the anonymous GitHub API request and returns tag_name.
func fetchLatest() (string, error) {
	req, err := http.NewRequest(http.MethodGet, endpoint(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "session-notes/"+userAgentVersion)

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("update check: status %d", resp.StatusCode)
	}
	var r releaseResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", err
	}
	return r.TagName, nil
}

// readCache loads the on-disk cache. A missing or corrupt file reads as absent.
func readCache() (latest string, checkedAt time.Time, ok bool) {
	data, err := os.ReadFile(cachePath())
	if err != nil {
		return "", time.Time{}, false
	}
	var c cacheFile
	if err := json.Unmarshal(data, &c); err != nil {
		return "", time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, c.CheckedAt)
	if err != nil {
		return "", time.Time{}, false
	}
	return c.Latest, t, true
}

// writeCache atomically persists the cache. All failures are silent — the check
// is best-effort and must never surface an error.
func writeCache(c cacheFile) {
	data, err := json.Marshal(c)
	if err != nil {
		return
	}
	dir := board.StateDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	path := cachePath()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

// WriteCacheTag records now + tag as the cache, clearing the staleness hint
// after a successful upgrade. It is exported for the upgrade subcommand.
func WriteCacheTag(tag string) {
	writeCache(cacheFile{CheckedAt: time.Now().Format(time.RFC3339), Latest: tag})
}

// Hint returns a one-line human-readable upgrade notice, or "" when there is
// nothing to say (dev/unknown build, opted out, check failed, or up to date).
func Hint(current string) string {
	if os.Getenv("SESSION_NOTES_NO_UPDATE_CHECK") != "" {
		return ""
	}
	userAgentVersion = current
	latest := LatestTag()
	if !newer(current, latest) {
		return ""
	}
	return fmt.Sprintf("newer release available: %s (installed %s) — run `session-notes upgrade`", latest, current)
}
