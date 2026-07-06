package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/nitsanavni/session-notes/internal/board"
)

// keyPress builds a tea.KeyMsg whose String() matches the board key handler's
// switch (single runes, plus the named "enter"/"left" keys used in tests).
func keyPress(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

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
	// Continuation lines are not nav stops: only the section header and the
	// single bullet are navigable (2 stops), never the ASCII-art rows.
	items := 0
	for _, p := range m.positions {
		if !p.header {
			items++
		}
	}
	if items != 1 {
		t.Errorf("item nav stops = %d, want 1 (bullet only)", items)
	}
	if len(m.positions) != 2 {
		t.Errorf("nav positions = %d, want 2 (header + bullet)", len(m.positions))
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

func TestArchiveCollapsedByDefault(t *testing.T) {
	src := "## Threads\n- [>] t\n\n## Archive\n- [x] one\n- [x] two\n"
	m := newTestModel(src, 80, 24)
	out := stripANSI(m.viewBoard())
	if !strings.Contains(out, "## Archive") || !strings.Contains(out, "(2 archived)") {
		t.Errorf("collapsed Archive header/count missing:\n%s", out)
	}
	if strings.Contains(out, "one") || strings.Contains(out, "two") {
		t.Errorf("collapsed Archive should hide its items:\n%s", out)
	}
	// Archived items are not navigable while collapsed: header stops only for it.
	for _, p := range m.positions {
		if p.sec == 1 && !p.header {
			t.Errorf("collapsed Archive item is navigable: %+v", p)
		}
	}
}

func TestArchiveExpandToggle(t *testing.T) {
	src := "## Threads\n- [>] t\n\n## Archive\n- [x] one\n"
	m := newTestModel(src, 80, 24)
	// Move cursor onto the Archive header (the second header stop).
	for i, p := range m.positions {
		if p.header && m.board.Sections[p.sec].Title == board.ArchiveTitle {
			m.cursor = i
		}
	}
	m.handleBoardKey(keyPress("enter"))
	if !m.archiveExpanded {
		t.Fatal("enter on Archive header did not expand it")
	}
	out := stripANSI(m.viewBoard())
	if !strings.Contains(out, "one") {
		t.Errorf("expanded Archive should show items:\n%s", out)
	}
	m.handleBoardKey(keyPress("h"))
	if m.archiveExpanded {
		t.Error("h on Archive header did not collapse it")
	}
}

func TestArchiveItemViaKey(t *testing.T) {
	src := "## Threads\n- [ ] keep\n- [>] archive me\n"
	m := newTestModel(src, 80, 24)
	// Cursor onto the "archive me" item.
	for i, p := range m.positions {
		if p.item != nil && p.item.DisplayText() == "archive me" {
			m.cursor = i
		}
	}
	m.handleBoardKey(keyPress("d"))
	got := m.board.Render()
	if !strings.Contains(got, "## Archive\n- [>] archive me\n") {
		t.Errorf("item not archived:\n%s", got)
	}
	for _, it := range m.board.Section("Threads").Items {
		if it.IsItem() && it.DisplayText() == "archive me" {
			t.Errorf("item still in Threads:\n%s", got)
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
