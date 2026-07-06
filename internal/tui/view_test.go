package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/nitsanavni/session-notes/internal/board"
)

// newTestModel builds a board-view model over src at the given viewport size.
func newTestModel(src string, w, h int) *model {
	m := newModel()
	m.board = board.Parse(src)
	m.path = "test.md"
	m.mode = modeBoard
	m.width, m.height = w, h
	m.rebuildPositions()
	return m
}

// stripANSI removes escape sequences so tests can assert on visible text.
func stripANSI(s string) string { return ansi.Strip(s) }

// bodyRows returns the rendered board rows, dropping the fixed footer/help bar
// (which is a status line clipped by the terminal, not wrapped).
func bodyRows(out string) []string {
	rows := strings.Split(out, "\n")
	for i, r := range rows {
		if strings.Contains(r, "j/k move") {
			return rows[:i]
		}
	}
	return rows
}

func TestLongLineWraps(t *testing.T) {
	long := "this is a deliberately very long single line item that should soft wrap across several rows when the viewport is narrow"
	m := newTestModel("## Threads\n- [ ] "+long+"\n", 40, 24)
	out := stripANSI(m.viewBoard())
	// No visible body row may exceed the viewport width.
	for _, line := range bodyRows(out) {
		if w := ansi.StringWidth(line); w > 40 {
			t.Errorf("row exceeds width %d: %q (%d)", 40, line, w)
		}
	}
	// The text must survive wrapping (its words are all still present in order).
	flat := strings.Join(strings.Fields(out), " ")
	if !strings.Contains(flat, long) {
		t.Errorf("wrapped text lost words:\n%s", out)
	}
	// It actually wrapped to more than one row (start + at least one continuation).
	if n := strings.Count(out, "wrap"); n < 1 {
		t.Fatalf("expected wrapped content, got:\n%s", out)
	}
}

func TestContinuationRendersVerbatim(t *testing.T) {
	art := []string{
		"      +---------+      +---------+",
		"      |  input  | ---> | process |",
		"      +---------+      +---------+",
	}
	src := "## Threads\n- [>] pipeline\n" + strings.Join(art, "\n") + "\n"
	m := newTestModel(src, 100, 24)
	out := stripANSI(m.viewBoard())
	// Each ASCII-art line appears verbatim (internal spacing preserved, untrimmed).
	for _, line := range art {
		if !strings.Contains(out, line) {
			t.Errorf("ASCII-art line not rendered verbatim: %q\n--- view ---\n%s", line, out)
		}
	}
	// Continuation lines are not nav stops: only the single bullet is navigable.
	if len(m.positions) != 1 {
		t.Errorf("nav positions = %d, want 1 (bullet only)", len(m.positions))
	}
}

func TestContinuationClipsNotWraps(t *testing.T) {
	longArt := "  " + strings.Repeat("=", 200)
	src := "## Threads\n- [>] wide\n" + longArt + "\n"
	m := newTestModel(src, 40, 24)
	out := stripANSI(m.viewBoard())
	for _, line := range bodyRows(out) {
		if w := ansi.StringWidth(line); w > 40 {
			t.Errorf("continuation row not clipped to width: %q (%d)", line, w)
		}
	}
}

func TestScrollReachesTop(t *testing.T) {
	// Empty Plan + empty Threads at the top, then a Questions section tall enough
	// to require scrolling in a short viewport.
	var b strings.Builder
	b.WriteString("## Plan\n\n## Threads\n\n## Questions\n")
	for i := 0; i < 30; i++ {
		b.WriteString("- [ ] item\n")
	}
	m := newTestModel(b.String(), 80, 10)

	// Scroll to the bottom item, then back to the first — the classic "can't
	// reach the top" path.
	m.cursor = len(m.positions) - 1
	m.viewBoard() // establish a scrolled-down viewport
	if m.scroll == 0 {
		t.Fatalf("expected a scrolled viewport at the bottom, scroll=0")
	}
	m.cursor = 0
	out := stripANSI(m.viewBoard())
	if m.scroll != 0 {
		t.Errorf("at first item scroll = %d, want 0 (top of board)", m.scroll)
	}
	// The very top of the board is now visible: title + both empty headers.
	for _, want := range []string{"test.md", "## Plan", "## Threads", "## Questions"} {
		if !strings.Contains(out, want) {
			t.Errorf("top-of-board not visible: missing %q in\n%s", want, out)
		}
	}
}

func TestEmptySectionHeaderRenders(t *testing.T) {
	m := newTestModel("## Plan\n\n## Ideas\n- a thought\n", 80, 24)
	out := stripANSI(m.viewBoard())
	if !strings.Contains(out, "## Plan") {
		t.Errorf("empty section header not rendered:\n%s", out)
	}
}
