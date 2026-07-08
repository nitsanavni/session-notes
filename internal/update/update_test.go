package update

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		// Plain release tags.
		{"v0.1.0", "v0.1.0", false},
		{"v0.1.0", "v0.2.0", true},
		{"v0.1.0", "v0.0.9", false},
		{"v0.1.0", "v1.0.0", true},
		{"v0.9.0", "v0.10.0", true},  // numeric, not string, compare
		{"v0.10.0", "v0.9.0", false}, // string compare would wrongly say true
		{"v1.2.3", "v1.2.4", true},   // patch bump
		{"v1.2.3", "v1.2.3", false},  // identical
		// git-describe suffixed current tag.
		{"v0.1.0-1-gabc-dirty", "v0.1.0", false},
		{"v0.1.0-1-gabc-dirty", "v0.2.0", true},
		// Non-comparable current builds skip before any compare.
		{"dev-abc123", "v9.9.9", false},
		{"unknown", "v9.9.9", false},
		{"", "v9.9.9", false},
		// Malformed tags.
		{"garbage", "v1.0.0", false},
		{"v1.0.0", "garbage", false},
		{"v1.2", "v1.3.0", false}, // current not full semver
	}
	for _, c := range cases {
		if got := newer(c.current, c.latest); got != c.want {
			t.Errorf("newer(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}

// TestLatestTagCacheTTL asserts a single request within the TTL: the first call
// hits the server and caches; the second reads the cache without a request.
func TestLatestTagCacheTTL(t *testing.T) {
	t.Setenv("SESSION_NOTES_DIR", t.TempDir())
	t.Setenv("SESSION_NOTES_NO_UPDATE_CHECK", "")

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		fmt.Fprint(w, `{"tag_name":"v1.2.3"}`)
	}))
	defer srv.Close()
	t.Setenv("SESSION_NOTES_UPDATE_URL", srv.URL)

	if got := LatestTag(); got != "v1.2.3" {
		t.Fatalf("first LatestTag() = %q, want v1.2.3", got)
	}
	if got := LatestTag(); got != "v1.2.3" {
		t.Fatalf("second LatestTag() = %q, want v1.2.3", got)
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Fatalf("server hit %d times within TTL, want 1", n)
	}
}

// TestLatestTagSilentFailure asserts a non-200 response is swallowed (no tag,
// no panic) and that the failed attempt still writes the cache so we don't retry
// until the TTL lapses.
func TestLatestTagSilentFailure(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SESSION_NOTES_DIR", dir)
	t.Setenv("SESSION_NOTES_NO_UPDATE_CHECK", "")

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	t.Setenv("SESSION_NOTES_UPDATE_URL", srv.URL)

	if got := LatestTag(); got != "" {
		t.Fatalf("LatestTag() on failure = %q, want empty", got)
	}
	// Second call within TTL must not hit the server again.
	if got := LatestTag(); got != "" {
		t.Fatalf("second LatestTag() = %q, want empty", got)
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Fatalf("server hit %d times, want 1 (failure still bumps checked_at)", n)
	}
}

// TestLatestTagOptOut asserts the check is a no-op when opted out.
func TestLatestTagOptOut(t *testing.T) {
	t.Setenv("SESSION_NOTES_DIR", t.TempDir())
	t.Setenv("SESSION_NOTES_NO_UPDATE_CHECK", "1")

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		fmt.Fprint(w, `{"tag_name":"v1.2.3"}`)
	}))
	defer srv.Close()
	t.Setenv("SESSION_NOTES_UPDATE_URL", srv.URL)

	if got := LatestTag(); got != "" {
		t.Fatalf("LatestTag() opted out = %q, want empty", got)
	}
	if got := Hint("v0.1.0"); got != "" {
		t.Fatalf("Hint() opted out = %q, want empty", got)
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("server hit %d times while opted out, want 0", n)
	}
}

// TestHint asserts the message wording and the up-to-date/dev suppression.
func TestHint(t *testing.T) {
	t.Setenv("SESSION_NOTES_DIR", t.TempDir())
	t.Setenv("SESSION_NOTES_NO_UPDATE_CHECK", "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"tag_name":"v0.2.0"}`)
	}))
	defer srv.Close()
	t.Setenv("SESSION_NOTES_UPDATE_URL", srv.URL)

	want := "newer release available: v0.2.0 (installed v0.1.0) — run `session-notes upgrade`"
	if got := Hint("v0.1.0"); got != want {
		t.Fatalf("Hint(v0.1.0) = %q, want %q", got, want)
	}
	// Dev build: no hint even when a newer release exists (cache already warm).
	if got := Hint("dev-abc123"); got != "" {
		t.Fatalf("Hint(dev-abc123) = %q, want empty", got)
	}
}
