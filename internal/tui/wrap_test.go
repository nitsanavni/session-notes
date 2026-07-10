package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// itemRows returns the rendered body rows that contain part of the given item's
// text, stripped of ANSI. Used to count how many display rows an item spans.
func bodyRowsWith(out, needle string) []string {
	var got []string
	for _, r := range bodyRows(out) {
		if strings.Contains(r, needle) {
			got = append(got, r)
		}
	}
	return got
}

// TestListWrapToggle drives the outline view's `w`: an item defaults to a single
// truncated line, `w` wraps it to a multi-line block, `w` again collapses it.
func TestListWrapToggle(t *testing.T) {
	long := "WRAPME " + strings.TrimSpace(strings.Repeat("word ", 30))
	m := newTestModel("## Threads\n- [ ] "+long+"\n", 40, 24)
	focus(t, m, long)

	// Default: a single truncated line ending in the ellipsis; no visible row
	// exceeds the viewport width, and not every word survives (it is truncated).
	out := stripANSI(m.viewBoard())
	rows := bodyRowsWith(out, "WRAPME")
	if len(rows) != 1 {
		t.Fatalf("default should render one line, got %d:\n%s", len(rows), out)
	}
	if !strings.Contains(out, "…") {
		t.Errorf("truncated line should end with an ellipsis:\n%s", out)
	}
	for _, line := range bodyRows(out) {
		if w := ansi.StringWidth(line); w > 40 {
			t.Errorf("row exceeds width 40: %q (%d)", line, w)
		}
	}

	// `w` wraps: the block now spans several rows and every word survives in order.
	m.handleBoardKey(keyPress("w"))
	out = stripANSI(m.viewBoard())
	wrapped := 0
	for _, r := range bodyRows(out) {
		if strings.Contains(r, "word") || strings.Contains(r, "WRAPME") {
			wrapped++
		}
	}
	if wrapped < 2 {
		t.Fatalf("expected the wrapped item to span >= 2 rows, got %d:\n%s", wrapped, out)
	}
	if flat := strings.Join(strings.Fields(out), " "); !strings.Contains(flat, long) {
		t.Errorf("wrapped text lost words:\n%s", out)
	}
	for _, line := range bodyRows(out) {
		if w := ansi.StringWidth(line); w > 40 {
			t.Errorf("wrapped row exceeds width 40: %q (%d)", line, w)
		}
	}

	// `w` again collapses back to a single truncated line.
	m.handleBoardKey(keyPress("w"))
	out = stripANSI(m.viewBoard())
	if rows := bodyRowsWith(out, "WRAPME"); len(rows) != 1 {
		t.Fatalf("re-collapsed item should be one line again, got %d:\n%s", len(rows), out)
	}
}

// TestWrapReplyIndentAndAuthorColor asserts a wrapped reply keeps its reply
// indent (continuation rows hang deeper than a top-level item's) and that the
// author token on the first line is still tinted after wrapping.
func TestWrapReplyIndentAndAuthorColor(t *testing.T) {
	orig := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	defer lipgloss.SetColorProfile(orig)

	long := "user: " + strings.TrimSpace(strings.Repeat("chat ", 30))
	src := "## Threads\n- [ ] top\n  - " + long + "\n"
	m := newTestModel(src, 40, 24)
	// Focus the reply (its DisplayText drops the marker; it stays "user: chat…").
	focus(t, m, long)
	m.handleBoardKey(keyPress("w"))
	out := m.viewBoard() // keep ANSI

	// Author token tinted user-blue (38;5;39) on the first line — the selected row
	// carries extra reverse-video attributes, so match the color code loosely.
	if !strings.Contains(out, "38;5;39m") {
		t.Errorf("wrapped reply lost its user author tint:\n%q", out)
	}
	plain := stripANSI(out)
	// Continuation rows of the reply hang-indent past a top-level item's text
	// column (reply indent preserved): find a "chat" continuation row and check
	// its leading spaces exceed the top-level hang (2 gutter + 3 marker + 1 = 6).
	for _, r := range bodyRows(plain) {
		if strings.HasPrefix(strings.TrimLeft(r, " "), "chat") {
			lead := len(r) - len(strings.TrimLeft(r, " "))
			if lead <= 6 {
				t.Errorf("reply continuation not indented past top-level column: lead=%d row=%q", lead, r)
			}
			return
		}
	}
	t.Fatalf("no wrapped continuation row found:\n%s", plain)
}

// TestWrapScrollBlockAware asserts the block-aware scroll clamp treats a wrapped
// item as one block: scrolling up onto it pins its first row, never snapping the
// section header on screen (the wrapped rows fill the body below it).
func TestWrapScrollBlockAware(t *testing.T) {
	long := "HEADMARK " + strings.TrimSpace(strings.Repeat("token ", 80))
	m := newTestModel("## Threads\n- [ ] "+long+"\n", 40, 12) // bodyH = 9
	focus(t, m, long)
	m.handleBoardKey(keyPress("w")) // wrap to many rows
	m.scroll = 3                    // scrolled down, arrowing back up
	out := stripANSI(m.viewBoard())
	if m.scroll != 1 {
		t.Fatalf("wrapped block did not pin cursor row: scroll = %d, want 1", m.scroll)
	}
	if strings.Contains(out, "## Threads") {
		t.Errorf("header should not snap on for a tall wrapped block:\n%s", out)
	}
	if !strings.Contains(out, "HEADMARK") {
		t.Errorf("wrapped item's first row should be visible:\n%s", out)
	}
}

// --- map: navigating into a collapsed node expands it ---

// TestMapExpandIntoCollapsed: a lateral away-from-center move onto a collapsed
// node advances its fold one step (collapsed -> default) and lands on the first
// now-visible child.
func TestMapExpandIntoCollapsed(t *testing.T) {
	src := "## Threads\n- [ ] parent\n  - [ ] childA\n  - [ ] childB\n"
	m := newMapModel(t, src, 120, 40)
	// Collapse parent (key s0/0), then focus it and rebuild.
	m.mapFold = map[string]foldState{"s0/0": foldCollapsed}
	m.mp = nil
	m.ensureMap()
	focusMapKey(t, m, "s0/0")

	m.handleMapKey(keyPress("l")) // away from center on the right side
	if got := m.mapFold["s0/0"]; got != foldDefault {
		// foldDefault is the zero value and is deleted from the map, so read absence.
		if _, present := m.mapFold["s0/0"]; present {
			t.Errorf("collapsed node not advanced to default: state=%v", got)
		}
	}
	if m.mapFocusKey != "s0/0/0" {
		t.Errorf("focus did not land on first revealed child: %q, want s0/0/0", m.mapFocusKey)
	}
}

// TestMapExpandIntoRepliesOnly: a collapsed replies-only node descends in ONE
// press — the keystroke jumps straight to replies-expanded (default would
// reveal nothing) and lands focus on the first reply, never wasting a press
// on an expansion that doesn't move (single-press expand-and-nav).
func TestMapExpandIntoRepliesOnly(t *testing.T) {
	src := "## Threads\n- [ ] rparent\n  - user: hi\n  - claude: yo\n"
	m := newMapModel(t, src, 120, 40)
	m.mapFold = map[string]foldState{"s0/0": foldCollapsed}
	m.mp = nil
	m.ensureMap()
	focusMapKey(t, m, "s0/0")

	m.handleMapKey(keyPress("l")) // ONE press: collapsed -> replies-expanded + move
	if got := m.mapFold["s0/0"]; got != foldRepliesExpanded {
		t.Fatalf("single press should reach replies-expanded, got %v", got)
	}
	if m.mapFocusKey != "s0/0/0" {
		t.Errorf("focus did not land on first reply: %q, want s0/0/0", m.mapFocusKey)
	}
}

// TestMapExpandIntoMixedGuard: a mixed node (non-reply + reply children) in the
// default state is never over-expanded into its reply thread. Stepping in lands
// on the visible non-reply child; and calling expandInto directly on it is a
// no-op that leaves the reply thread folded.
func TestMapExpandIntoMixedGuard(t *testing.T) {
	src := "## Threads\n- [ ] mparent\n  - [ ] childA\n  - user: reply\n"
	m := newMapModel(t, src, 120, 40)
	focusMapKey(t, m, "s0/0") // default state (mixed): childA visible, reply summarized

	m.handleMapKey(keyPress("l")) // steps into the visible non-reply child
	if m.mapFocusKey != "s0/0/0" {
		t.Errorf("l on a mixed node should enter its visible child: %q, want s0/0/0", m.mapFocusKey)
	}

	// Directly exercise the guard: expandInto on the mixed default node must not
	// advance to replies-expanded.
	focusMapKey(t, m, "s0/0")
	m.expandInto()
	if _, present := m.mapFold["s0/0"]; present {
		t.Errorf("mixed default node was over-expanded: %v", m.mapFold["s0/0"])
	}
	if !strings.Contains(m.status, "already expanded") {
		t.Errorf("expected an already-expanded status, got %q", m.status)
	}
}

// TestMapExpandIntoDeadEnd: a fully-expanded / childless leaf is a dead end with
// a status message, no fold change.
func TestMapExpandIntoDeadEnd(t *testing.T) {
	src := "## Threads\n- [ ] lonely\n"
	m := newMapModel(t, src, 120, 40)
	focusMapKey(t, m, "s0/0")
	m.expandInto()
	if !strings.Contains(m.status, "nothing to expand") {
		t.Errorf("expected a dead-end status on a childless node, got %q", m.status)
	}
}
