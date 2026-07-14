package hooks

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nitsanavni/session-notes/internal/board"
)

// TestBlurb checks the session-start blurb substitutes the board path and keeps
// its key lines (it is the single source for `session-notes docs blurb`).
func TestBlurb(t *testing.T) {
	b := Blurb("/tmp/board.md")
	for _, want := range []string{
		"Session board: /tmp/board.md",
		"WRITE THE BOARD VIA THE CLI",
		"--board /tmp/board.md",
		"(protocol|monitor|conflicts|cli|subtree|server|headless|blurb)",
	} {
		if !strings.Contains(b, want) {
			t.Errorf("blurb missing %q", want)
		}
	}
}

// TestPinsDueCadence exercises the three branches: first injection, unchanged +
// fresh (suppressed), unchanged + stale (past cadence), and content change.
func TestPinsDueCadence(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SESSION_NOTES_DIR", dir)
	sp := pinsPath("sess")
	now := time.Now()

	cad := DefaultPinCadence
	// 1. No state yet -> due.
	if !pinsDue(sp, []string{"a", "b"}, cad, now) {
		t.Fatal("first injection should be due")
	}
	// 2. Same content, only a minute later -> not due.
	if pinsDue(sp, []string{"a", "b"}, cad, now.Add(time.Minute)) {
		t.Error("unchanged + fresh should be suppressed")
	}
	// 3. Same content, well past the cadence -> due by staleness.
	if !pinsDue(sp, []string{"a", "b"}, cad, now.Add(cad+time.Minute)) {
		t.Error("unchanged + stale should re-inject")
	}
	// After the stale injection the timestamp resets: a minute later is fresh.
	if pinsDue(sp, []string{"a", "b"}, cad, now.Add(cad+2*time.Minute)) {
		t.Error("timestamp should reset after a stale injection")
	}
	// 4. Content change -> due, even when fresh.
	if !pinsDue(sp, []string{"a", "c"}, cad, now.Add(cad+3*time.Minute)) {
		t.Error("changed content should re-inject")
	}
}

// TestPinCadenceParse checks the lenient frontmatter cadence knob: valid Go
// durations win, a missing key uses the default, and garbage / non-positive
// values fall back to the default without crashing.
func TestPinCadenceParse(t *testing.T) {
	cases := []struct {
		fm   string
		want time.Duration
	}{
		{"", DefaultPinCadence},           // absent -> default
		{"10m", 10 * time.Minute},         // minutes
		{"90s", 90 * time.Second},         // seconds
		{"1h", time.Hour},                 // hours
		{"  5m  ", 5 * time.Minute},       // surrounding whitespace tolerated
		{"garbage", DefaultPinCadence},    // unparseable -> default
		{"0s", DefaultPinCadence},         // zero -> default
		{"-5m", DefaultPinCadence},        // negative -> default
		{"10 minutes", DefaultPinCadence}, // wrong syntax -> default
	}
	for _, c := range cases {
		b := &board.Board{}
		b.Frontmatter.PinCadence = c.fm
		if got := PinCadence(b); got != c.want {
			t.Errorf("PinCadence(%q) = %v, want %v", c.fm, got, c.want)
		}
	}
	if got := PinCadence(nil); got != DefaultPinCadence {
		t.Errorf("PinCadence(nil) = %v, want default", got)
	}
}

// TestPinsBlock checks the shared block format (header + one bullet per live pin)
// and that a board with no pins yields empty output.
func TestPinsBlock(t *testing.T) {
	b := board.Parse("## Working Agreements\n- !pin keep tests green\n- [x] !pin done\n- plain\n")
	block, texts := PinsBlock(b)
	if len(texts) != 1 || texts[0] != "keep tests green" {
		t.Fatalf("texts = %v, want [keep tests green]", texts)
	}
	want := "Pinned board items (reinjected on cadence):\n- keep tests green\n"
	if block != want {
		t.Errorf("block = %q, want %q", block, want)
	}
	if bl, tx := PinsBlock(board.Parse("## Plan\n- [ ] nothing pinned\n")); bl != "" || tx != nil {
		t.Errorf("no-pin board = (%q,%v), want empty", bl, tx)
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

// TestStatusLineFull parses a rich statusLine payload with pre-computed
// used_percentage and writes the sidecar + passthrough line.
func TestStatusLineFull(t *testing.T) {
	t.Setenv("SESSION_NOTES_DIR", t.TempDir())
	in := `{"session_id":"s1","model":{"display_name":"Opus","id":"claude-opus-4-8"},` +
		`"cost":{"total_cost_usd":2.5},"context_window":{"used_percentage":62.4,` +
		`"total_input_tokens":124800,"context_window_size":200000}}`
	var out strings.Builder
	StatusLine(strings.NewReader(in), &out)

	line := strings.TrimSpace(out.String())
	if line != "Opus | ctx 62%" {
		t.Fatalf("passthrough = %q", line)
	}
	st, ok := board.ReadStatus("s1")
	if !ok || st.Model != "Opus" || st.CostUSD != 2.5 {
		t.Fatalf("sidecar = %+v ok=%v", st, ok)
	}
	if st.ContextPct < 62 || st.ContextPct > 63 {
		t.Fatalf("context pct = %v", st.ContextPct)
	}
	if st.Activity != "working" {
		t.Fatalf("activity = %q", st.Activity)
	}
}

// TestStatusLineFallbackPct derives the percentage from token counts when
// used_percentage is absent, and tolerates the missing display_name.
func TestStatusLineFallbackPct(t *testing.T) {
	t.Setenv("SESSION_NOTES_DIR", t.TempDir())
	in := `{"session_id":"s2","model":{"id":"claude-opus-4-8"},` +
		`"context_window":{"total_input_tokens":50000,"context_window_size":200000}}`
	var out strings.Builder
	StatusLine(strings.NewReader(in), &out)
	if got := strings.TrimSpace(out.String()); got != "claude-opus-4-8 | ctx 25%" {
		t.Fatalf("passthrough = %q", got)
	}
	st, _ := board.ReadStatus("s2")
	if st.ContextPct != 25 {
		t.Fatalf("derived pct = %v", st.ContextPct)
	}
}

// TestStatusLineTolerant ignores junk, unknown fields, and missing session id.
func TestStatusLineTolerant(t *testing.T) {
	t.Setenv("SESSION_NOTES_DIR", t.TempDir())
	var out strings.Builder
	StatusLine(strings.NewReader(`not json`), &out)
	StatusLine(strings.NewReader(`{"model":{"display_name":"X"}}`), &out) // no session_id
	if out.Len() != 0 {
		t.Fatalf("expected no output for bad input, got %q", out.String())
	}
	// Unknown fields + no context window: model recorded, context unknown.
	out.Reset()
	StatusLine(strings.NewReader(`{"session_id":"s3","model":{"display_name":"Sonnet"},"future_field":123}`), &out)
	if got := strings.TrimSpace(out.String()); got != "Sonnet" {
		t.Fatalf("passthrough = %q", got)
	}
	st, _ := board.ReadStatus("s3")
	if st.HasContext() {
		t.Fatalf("context should be unknown: %+v", st)
	}
}

// TestRecordActivity stamps the activity kind without disturbing an existing
// statusline-written sidecar.
func TestRecordActivity(t *testing.T) {
	t.Setenv("SESSION_NOTES_DIR", t.TempDir())
	StatusLine(strings.NewReader(`{"session_id":"s4","model":{"display_name":"Opus"},"context_window":{"used_percentage":10}}`), &strings.Builder{})
	RecordActivity("s4", "ended")
	st, _ := board.ReadStatus("s4")
	if st.Model != "Opus" || st.Activity != "ended" {
		t.Fatalf("sidecar = %+v", st)
	}
}
