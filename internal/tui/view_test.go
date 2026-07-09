package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/nitsanavni/session-notes/internal/board"
)

// focus points the cursor (and active section) at the first item whose visible
// text equals want, mirroring how key handlers keep m.selSec in sync with the
// cursor. It fails the test if no such item exists.
func focus(t *testing.T, m *model, want string) {
	t.Helper()
	for i, p := range m.positions {
		if p.item != nil && p.item.DisplayText() == want {
			m.cursor = i
			m.selSec = p.sec
			return
		}
	}
	t.Fatalf("no navigable item with text %q", want)
}

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
// The board path points at a throwaway file in the OS temp dir (not the repo) so
// that key handlers which persist through the locked save path — and the sidecar
// lock file they create — do not litter the working tree.
func newTestModel(src string, w, h int) *model {
	m := newModel()
	m.board = board.Parse(src)
	m.path = filepath.Join(os.TempDir(), "session-notes-tui-test.md")
	m.board.Path = m.path
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
	focus(t, m, long)
	m.handleBoardKey(keyPress("w")) // opt into wrap; items default to one truncated line
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

// TestStickyTitleAndReadout asserts the board title stays at the top row
// regardless of scroll offset, and that the "start–end/total" scroll readout
// appears only when the board is taller than the viewport.
func TestStickyTitleAndReadout(t *testing.T) {
	// Short board: fits the viewport, so no readout and the title is row 0.
	short := newTestModel("## Threads\n- [ ] one\n- [ ] two\n", 80, 24)
	out := stripANSI(short.viewBoard())
	rows := strings.Split(out, "\n")
	if !strings.Contains(rows[0], "test.md") {
		t.Errorf("title not on top row of short board: %q", rows[0])
	}
	if strings.ContainsAny(out, "▲▼") || strings.Contains(out, "/2") {
		t.Errorf("short board should not show a scroll readout:\n%s", out)
	}

	// Tall board: scroll to the bottom, the title must still be pinned at row 0
	// and the readout with a running total must be present.
	var b strings.Builder
	b.WriteString("## Threads\n")
	for i := 0; i < 40; i++ {
		b.WriteString("- [ ] item\n")
	}
	tall := newTestModel(b.String(), 80, 12)
	tall.cursor = len(tall.positions) - 1
	out = stripANSI(tall.viewBoard())
	if tall.scroll == 0 {
		t.Fatalf("expected a scrolled viewport, scroll=0")
	}
	rows = strings.Split(out, "\n")
	if !strings.Contains(rows[0], "test.md") {
		t.Errorf("title not sticky at top when scrolled: %q", rows[0])
	}
	if !strings.Contains(rows[0], "/41") { // 40 items + 1 section header
		t.Errorf("scroll readout total missing from title row: %q", rows[0])
	}
	if !strings.Contains(rows[0], "▲") {
		t.Errorf("expected up chevron when scrolled down: %q", rows[0])
	}
}

func TestHeaderShowsShortSessionIDWithTitle(t *testing.T) {
	src := "---\nsession: 0123456789abcdef\ntitle: My Board\n---\n## Threads\n- [ ] one\n"
	m := newTestModel(src, 80, 24)
	out := stripANSI(m.viewBoard())
	row0 := strings.Split(out, "\n")[0]
	if !strings.Contains(row0, "My Board") {
		t.Errorf("title missing from header: %q", row0)
	}
	if !strings.Contains(row0, "01234567") {
		t.Errorf("short session id missing from header: %q", row0)
	}
	if strings.Contains(row0, "0123456789abcdef") {
		t.Errorf("header should show only the shortened id, not the full one: %q", row0)
	}

	// Map view header carries the short id too.
	m.mapView = true
	m.mp = nil
	m.ensureMap()
	mout := stripANSI(m.viewMap())
	mrow0 := strings.Split(mout, "\n")[0]
	if !strings.Contains(mrow0, "01234567") {
		t.Errorf("short session id missing from map header: %q", mrow0)
	}
}

func TestHeaderNoDoubledSessionWithoutTitle(t *testing.T) {
	src := "---\nsession: 0123456789abcdef\n---\n## Threads\n- [ ] one\n"
	m := newTestModel(src, 80, 24)
	out := stripANSI(m.viewBoard())
	row0 := strings.Split(out, "\n")[0]
	// Session id serves as the title; the short tag must not be appended.
	if strings.Count(row0, "01234567") != 1 {
		t.Errorf("session id should appear once (as the title), got: %q", row0)
	}
}

func TestYankBoardPathStatus(t *testing.T) {
	m := newTestModel("## Threads\n- [ ] one\n", 80, 24)
	m.yankBoardPath()
	if !strings.Contains(m.status, m.board.Path) {
		t.Errorf("status should reveal the board path, got %q", m.status)
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
	if m.sectionCollapsed(board.ArchiveTitle) {
		t.Fatal("enter on Archive header did not expand it")
	}
	out := stripANSI(m.viewBoard())
	if !strings.Contains(out, "one") {
		t.Errorf("expanded Archive should show items:\n%s", out)
	}
	m.handleBoardKey(keyPress("h"))
	if !m.sectionCollapsed(board.ArchiveTitle) {
		t.Error("h on Archive header did not collapse it")
	}
}

// TestSectionCollapseToggle asserts the collapse toggle generalizes beyond
// Archive: enter on any section header (Log here) hides its items from both
// the rendered output and the cursor positions, shows a dim "(N items)" hint,
// and a second enter expands it again. The collapse state must survive a
// reload (it is keyed by section title, not index or pointer).
func TestSectionCollapseToggle(t *testing.T) {
	src := "## Threads\n- [>] t\n\n## Log\n- 10:00 user: first\n- 10:05 claude: second\n"
	m := newTestModel(src, 80, 24)

	// Log starts expanded (only Archive defaults collapsed).
	out := stripANSI(m.viewBoard())
	if !strings.Contains(out, "first") || !strings.Contains(out, "second") {
		t.Fatalf("Log should start expanded:\n%s", out)
	}

	// Move cursor onto the Log header and collapse it.
	for i, p := range m.positions {
		if p.header && m.board.Sections[p.sec].Title == board.LogTitle {
			m.cursor = i
		}
	}
	m.handleBoardKey(keyPress("enter"))
	if !m.sectionCollapsed(board.LogTitle) {
		t.Fatal("enter on Log header did not collapse it")
	}
	out = stripANSI(m.viewBoard())
	if strings.Contains(out, "first") || strings.Contains(out, "second") {
		t.Errorf("collapsed Log should hide its items:\n%s", out)
	}
	if !strings.Contains(out, "(2 items)") {
		t.Errorf("collapsed Log header missing hidden-item hint:\n%s", out)
	}
	// Collapsed items are not navigable: no item stops in the Log section.
	for _, p := range m.positions {
		if !p.header && m.board.Sections[p.sec].Title == board.LogTitle {
			t.Errorf("collapsed Log item is navigable: %+v", p)
		}
	}

	// The state is keyed by title, so a reparse-driven rebuild keeps it.
	m.board = board.Parse(src)
	m.board.Path = m.path
	m.rebuildPositions()
	if !m.sectionCollapsed(board.LogTitle) {
		t.Error("Log collapse state lost across a reparse")
	}

	// Enter again expands.
	m.handleBoardKey(keyPress("enter"))
	if m.sectionCollapsed(board.LogTitle) {
		t.Fatal("second enter on Log header did not expand it")
	}
	out = stripANSI(m.viewBoard())
	if !strings.Contains(out, "first") || !strings.Contains(out, "second") {
		t.Errorf("re-expanded Log should show items:\n%s", out)
	}
	items := 0
	for _, p := range m.positions {
		if !p.header && m.board.Sections[p.sec].Title == board.LogTitle {
			items++
		}
	}
	if items != 2 {
		t.Errorf("expanded Log item stops = %d, want 2", items)
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

// TestAuthorTokenTinted asserts the leading "user:" author token is tinted
// user-blue (256-color 39) prefix-only, with the rest of the line intact under
// the ANSI codes. The default renderer disables color outside a TTY, so force a
// 256-color profile for the duration of the test.
func TestAuthorTokenTinted(t *testing.T) {
	orig := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	defer lipgloss.SetColorProfile(orig)

	m := newTestModel("## Threads\n- user: hello there\n", 80, 24)
	out := m.viewBoard() // keep ANSI codes
	if !strings.Contains(out, "\x1b[38;5;39m") {
		t.Errorf("expected user author token tinted blue (38;5;39):\n%q", out)
	}
	if !strings.Contains(stripANSI(out), "user: hello there") {
		t.Errorf("author line text lost:\n%s", stripANSI(out))
	}
}

// TestSelectedDoneReverseVideo asserts a selected done item uses reverse-video
// (SGR 7) for its selection affordance, and that done items no longer carry
// strikethrough (SGR 9) — greyed-out only, per the user preference.
func TestSelectedDoneReverseVideo(t *testing.T) {
	orig := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	defer lipgloss.SetColorProfile(orig)

	m := newTestModel("## Threads\n- [x] finished task\n", 80, 24)
	for i, p := range m.positions {
		if p.item != nil && p.item.DisplayText() == "finished task" {
			m.cursor = i
		}
	}
	out := m.viewBoard()
	if !strings.Contains(out, ";7;") && !strings.Contains(out, ";7m") {
		t.Errorf("expected reverse-video (SGR 7) on selected row:\n%q", out)
	}
	if strings.Contains(out, ";9m") {
		t.Errorf("expected NO strikethrough (SGR 9) on done item:\n%q", out)
	}
}

// TestDoneNoStrikethrough asserts an unselected done item renders greyed out
// (dim 240) with no strikethrough (SGR 9) in either view.
func TestDoneNoStrikethrough(t *testing.T) {
	orig := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	defer lipgloss.SetColorProfile(orig)

	m := newTestModel("## Threads\n- [x] finished task\n", 80, 24)
	out := m.viewBoard()
	if strings.Contains(out, ";9m") || strings.Contains(out, "\x1b[9m") {
		t.Errorf("outline: expected NO strikethrough on done item:\n%q", out)
	}
	if !strings.Contains(out, "240") {
		t.Errorf("outline: expected done item greyed out (240):\n%q", out)
	}
	m.enterMap()
	mout := m.viewMap()
	if strings.Contains(mout, ";9m") || strings.Contains(mout, "\x1b[9m") {
		t.Errorf("map: expected NO strikethrough on done node:\n%q", mout)
	}
}

// childBoard builds a single Threads section whose first item is SELITEM,
// followed by n indented children (2-space reply indent). At width 80 every
// bullet is one display row, so lines are: [0]=header, [1]=SELITEM,
// [2..n+1]=child00..child(n-1).
func childBoard(n int) string {
	var b strings.Builder
	b.WriteString("## Threads\n- [ ] SELITEM\n")
	for i := 0; i < n; i++ {
		b.WriteString(fmt.Sprintf("  - [ ] child%02d\n", i))
	}
	return b.String()
}

// TestScrollUpKeepsBlockOverHeader is the bug fix: with the cursor on an item
// whose rows + child subtree overflow the viewport, scrolling up must not snap
// the viewport to the section header (which would evict the selected item's
// tail). The block-aware clamp keeps the cursor row at the top instead.
func TestScrollUpKeepsBlockOverHeader(t *testing.T) {
	m := newTestModel(childBoard(10), 80, 12) // bodyH = 12 - 2 - 1 = 9
	focus(t, m, "SELITEM")                    // cursorLine == 1, header (selHeaderLine) == 0
	m.scroll = 4                              // as if scrolled down, now arrowing back up
	out := stripANSI(m.viewBoard())

	// The whole block (SELITEM + 10 children) is 11 rows > bodyH, so the header
	// preference must NOT fire: scroll stays on the cursor row, not the header.
	if m.scroll != 1 {
		t.Fatalf("scroll snapped away from cursor row: got %d, want 1 (header line 0 not shown)", m.scroll)
	}
	if strings.Contains(out, "## Threads") {
		t.Errorf("section header should not be pinned on screen:\n%s", out)
	}
	// Selected item and at least its first child stay visible.
	for _, want := range []string{"SELITEM", "child00"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q visible after scroll-up:\n%s", want, out)
		}
	}
}

// TestHeaderPreferenceStillFits confirms the header preference is intact: when
// the selected item's block fits below the section header, scrolling reveals
// the header row.
func TestHeaderPreferenceStillFits(t *testing.T) {
	var b strings.Builder
	b.WriteString("## Top\n")
	for i := 0; i < 9; i++ {
		b.WriteString(fmt.Sprintf("- [ ] f%d\n", i))
	}
	// Threads header lands at line 10; t0 at line 11.
	b.WriteString("## Threads\n- [ ] t0\n- [ ] t1\n")
	m := newTestModel(b.String(), 80, 12) // bodyH = 9
	focus(t, m, "t0")                     // cursorLine 11, selHeaderLine 10, block is 1 row
	m.scroll = 11                         // header just above the window
	out := stripANSI(m.viewBoard())

	if m.scroll != 10 {
		t.Fatalf("header preference did not fire: scroll = %d, want 10 (## Threads row)", m.scroll)
	}
	if !strings.Contains(out, "## Threads") {
		t.Errorf("active section header should be revealed:\n%s", out)
	}
	if !strings.Contains(out, "t0") {
		t.Errorf("selected item should remain visible:\n%s", out)
	}
}

// TestOverflowingBlockPinsCursorTop asserts that an item whose subtree is taller
// than the viewport puts the selected row on the first body line, with the
// children filling the rest of the window.
func TestOverflowingBlockPinsCursorTop(t *testing.T) {
	m := newTestModel(childBoard(12), 80, 12) // bodyH = 9; block = 13 rows
	focus(t, m, "SELITEM")                    // cursorLine 1
	m.scroll = 0
	out := stripANSI(m.viewBoard())

	if m.scroll != 1 {
		t.Fatalf("selected row not pinned to top of body: scroll = %d, want 1 (== cursorLine)", m.scroll)
	}
	if strings.Contains(out, "## Threads") {
		t.Errorf("header should be scrolled off for an overflowing block:\n%s", out)
	}
	// Children fill the body: rows 1..9 => SELITEM + child00..child07.
	for _, want := range []string{"SELITEM", "child00", "child07"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in the filled body:\n%s", want, out)
		}
	}
}

// TestWrappedItemNoHeaderSnap covers a wrapped item with no children near the
// header: scrolling up keeps its tail rows on screen without snapping to the
// header.
func TestWrappedItemNoHeaderSnap(t *testing.T) {
	long := "STARTMARK " + strings.TrimSpace(strings.Repeat("word ", 60))
	m := newTestModel("## Threads\n- [ ] "+long+"\n", 40, 12) // narrow -> wraps to many rows
	// The board's only item is the wrapped bullet; put the cursor on it.
	for i, p := range m.positions {
		if p.item != nil {
			m.cursor, m.selSec = i, p.sec
			break
		}
	}
	m.handleBoardKey(keyPress("w")) // wrap this item so it spans many rows
	m.scroll = 3                    // scrolled down, arrowing up
	out := stripANSI(m.viewBoard())

	if m.scroll != 1 {
		t.Fatalf("wrapped item snapped scroll unexpectedly: got %d, want 1", m.scroll)
	}
	if strings.Contains(out, "## Threads") {
		t.Errorf("header should not snap onto screen for a tall wrapped item:\n%s", out)
	}
	// The item's first row (its head) is at the top of the body.
	if !strings.Contains(out, "STARTMARK") {
		t.Errorf("expected the wrapped item's first row visible:\n%s", out)
	}
	// The body is filled to bodyH with the item's wrapped rows.
	rows := bodyRows(out)
	filled := 0
	for _, r := range rows {
		if strings.Contains(r, "word") {
			filled++
		}
	}
	if filled < 9 {
		t.Errorf("expected the wrapped tail to fill bodyH (9) rows, got %d:\n%s", filled, out)
	}
}

// TestDownwardSingleRowBottomAligns is the regression guard for downward
// movement: single-row items still bottom-align (the block-aware clamp reduces
// to the old cursorLine-bodyH+1 behavior when the block is one row).
func TestDownwardSingleRowBottomAligns(t *testing.T) {
	var b strings.Builder
	b.WriteString("## Threads\n")
	for i := 0; i < 20; i++ {
		b.WriteString(fmt.Sprintf("- [ ] item%02d\n", i))
	}
	m := newTestModel(b.String(), 80, 12) // bodyH = 9
	focus(t, m, "item19")                 // cursorLine 20 (header + 20 items)
	m.scroll = 0
	out := stripANSI(m.viewBoard())

	// Bottom-aligned: scroll = cursorLine - bodyH + 1 = 20 - 9 + 1 = 12.
	if m.scroll != 12 {
		t.Fatalf("single-row item not bottom-aligned: scroll = %d, want 12", m.scroll)
	}
	if !strings.Contains(out, "item19") {
		t.Errorf("selected bottom item should be visible:\n%s", out)
	}
	if !strings.Contains(out, "item11") { // first visible body line
		t.Errorf("expected the window to start at item11:\n%s", out)
	}
	if strings.Contains(out, "## Threads") || strings.Contains(out, "item00") {
		t.Errorf("top of board should be scrolled away:\n%s", out)
	}
}
