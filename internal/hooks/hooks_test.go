package hooks

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nitsanavni/session-notes/internal/board"
)

// TestPinsDueCadence exercises the three branches: first injection, unchanged +
// fresh (suppressed), unchanged + stale (>35m), and content change.
func TestPinsDueCadence(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SESSION_NOTES_DIR", dir)
	sp := pinsPath("sess")
	now := time.Now()

	// 1. No state yet -> due.
	if !pinsDue(sp, []string{"a", "b"}, now) {
		t.Fatal("first injection should be due")
	}
	// 2. Same content, only a minute later -> not due.
	if pinsDue(sp, []string{"a", "b"}, now.Add(time.Minute)) {
		t.Error("unchanged + fresh should be suppressed")
	}
	// 3. Same content, 40 minutes later -> due by staleness.
	if !pinsDue(sp, []string{"a", "b"}, now.Add(40*time.Minute)) {
		t.Error("unchanged + stale should re-inject")
	}
	// After the stale injection the timestamp resets: a minute later is fresh.
	if pinsDue(sp, []string{"a", "b"}, now.Add(41*time.Minute)) {
		t.Error("timestamp should reset after a stale injection")
	}
	// 4. Content change -> due, even when fresh.
	if !pinsDue(sp, []string{"a", "c"}, now.Add(42*time.Minute)) {
		t.Error("changed content should re-inject")
	}
}

// TestTrackWipLifecycle checks first-seen recording, staleness counting, and
// eviction of lines that are no longer "[>]".
func TestTrackWipLifecycle(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SESSION_NOTES_DIR", dir)
	wp := wipPath("sess")
	t0 := time.Now()

	// Two wip lines appear now: neither is stale yet.
	if got := trackWip(wp, []string{"- [>] alpha", "- [>] beta"}, t0); got != 0 {
		t.Fatalf("fresh wip stale count = %d, want 0", got)
	}
	// Three hours later, still both wip: both are now stale (>2h).
	if got := trackWip(wp, []string{"- [>] alpha", "- [>] beta"}, t0.Add(3*time.Hour)); got != 2 {
		t.Fatalf("aged wip stale count = %d, want 2", got)
	}
	// beta leaves wip; alpha stays (still old). Count 1, and beta is evicted.
	if got := trackWip(wp, []string{"- [>] alpha"}, t0.Add(3*time.Hour)); got != 1 {
		t.Fatalf("after beta left, stale count = %d, want 1", got)
	}
	data, _ := os.ReadFile(wp)
	if strings.Contains(string(data), "beta") {
		t.Error("beta should be evicted from the wip state file")
	}
	// No wip at all: file removed.
	if got := trackWip(wp, nil, t0.Add(3*time.Hour)); got != 0 {
		t.Errorf("empty wip stale count = %d, want 0", got)
	}
	if _, err := os.Stat(wp); !os.IsNotExist(err) {
		t.Error("wip state file should be removed when no items are in progress")
	}
}

func TestCoherenceDigest(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SESSION_NOTES_DIR", dir)
	now := time.Now()

	// Healthy board: nothing to report -> empty digest.
	healthy := board.Parse("## Threads\n- [x] done\n\n## Waiting on User\n")
	if got := coherenceDigest(healthy, wipPath("h"), now); got != "" {
		t.Errorf("healthy digest = %q, want empty", got)
	}

	// A board with one of each signal. Seed the wip file so alpha reads stale.
	wp := wipPath("s")
	trackWip(wp, []string{"- [>] alpha"}, now.Add(-3*time.Hour))
	b := board.Parse(`## Waiting on User
- [ ] approve plan
- [ ] pick color

## Threads
- [>] alpha

## Questions
- [ ] deploy? @claude
`)
	got := coherenceDigest(b, wp, now)
	for _, want := range []string{
		"1 threads [>] untouched >2h",
		"1 questions @claude unanswered",
		"2 items in Waiting on User",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("digest %q missing clause %q", got, want)
		}
	}
	if !strings.HasPrefix(got, "Board health:") {
		t.Errorf("digest = %q, want Board health: prefix", got)
	}
}
