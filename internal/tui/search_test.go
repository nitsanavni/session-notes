package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/nitsanavni/session-notes/internal/board"
)

// forceColor forces a 256-color profile for the test's duration; the default
// renderer strips color outside a TTY, so the highlight tests need this to see
// the SGR sequences they assert on.
func forceColor(t *testing.T) {
	t.Helper()
	orig := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(orig) })
}

// searchBoard has matches spread across sections and a reply thread so tests can
// assert document-order iteration, replies included.
const searchBoard = "---\ntitle: T\n---\n" +
	"## Plan\n" +
	"- [ ] alpha task\n" +
	"- [ ] beta task\n" +
	"## Threads\n" +
	"- [ ] gamma topic\n" +
	"  - claude: alpha reply here\n" +
	"## Ideas\n" +
	"- [ ] ALPHA idea\n"

// typeSearch feeds a query rune-by-rune through the incremental search handler,
// starting from a fresh prompt.
func typeSearch(m *model, q string) {
	m.startSearch()
	for _, r := range q {
		m.handleSearchKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

func TestSearchMatchesOrderingAndCase(t *testing.T) {
	b := board.Parse(searchBoard)
	got := searchMatches(b, "alpha", false)
	// Document order: Plan "alpha task", Threads reply "alpha reply here",
	// Ideas "ALPHA idea" (case-insensitive).
	want := []string{"alpha task", "claude: alpha reply here", "ALPHA idea"}
	if len(got) != len(want) {
		t.Fatalf("got %d matches, want %d: %v", len(got), len(want), got)
	}
	for i, it := range got {
		if it.DisplayText() != want[i] {
			t.Errorf("match %d = %q, want %q", i, it.DisplayText(), want[i])
		}
	}
}

func TestSearchMatchesEmptyQuery(t *testing.T) {
	b := board.Parse(searchBoard)
	if got := searchMatches(b, "   ", false); got != nil {
		t.Errorf("whitespace query matched %d items, want none", len(got))
	}
}

// TestSearchScopeExcludesArchiveAndLog: the default working-set scope skips the
// Archive and Log sections; toggling to "all" includes them. ctrl+s flips it.
func TestSearchScopeExcludesArchiveAndLog(t *testing.T) {
	src := "---\ntitle: T\n---\n" +
		"## Plan\n- [ ] widget in plan\n" +
		"## Log\n- widget in the log\n" +
		"## Archive\n- widget archived\n"
	b := board.Parse(src)
	def := searchMatches(b, "widget", false)
	if len(def) != 1 || def[0].DisplayText() != "widget in plan" {
		t.Fatalf("default scope should match only the Plan item, got %v", def)
	}
	all := searchMatches(b, "widget", true)
	if len(all) != 3 {
		t.Fatalf("all scope should match all three, got %d: %v", len(all), all)
	}

	// ctrl+s in the prompt toggles scope live and re-tallies.
	m := newTestModel(src, 80, 40)
	typeSearch(m, "widget")
	if !strings.Contains(m.status, "working set") {
		t.Errorf("status should note working-set scope: %q", m.status)
	}
	m.handleSearchKey(tea.KeyMsg{Type: tea.KeyTab})
	if !m.searchScopeAll {
		t.Error("tab did not widen the scope")
	}
	if !strings.Contains(m.status, "(all)") || !strings.Contains(m.status, "/3 matches") {
		t.Errorf("after tab expected 3 matches in (all) scope: %q", m.status)
	}
}

func TestSearchIncrementalJumpsToFirstMatch(t *testing.T) {
	m := newTestModel(searchBoard, 80, 40)
	m.cursor = 0 // on the Plan header
	typeSearch(m, "beta")
	it := m.currentItem()
	if it == nil || it.DisplayText() != "beta task" {
		t.Fatalf("cursor did not land on first match; on %v", it)
	}
}

func TestSearchNextWrapsAndReportsCount(t *testing.T) {
	m := newTestModel(searchBoard, 80, 40)
	typeSearch(m, "alpha")
	// Confirm the search (enter) to enable n/N.
	m.handleSearchKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.searchActive {
		t.Fatal("search not active after enter")
	}
	first := m.currentItem().DisplayText()
	if first != "alpha task" {
		t.Fatalf("first match = %q, want alpha task", first)
	}
	m.searchNext(1)
	if got := m.currentItem().DisplayText(); got != "claude: alpha reply here" {
		t.Errorf("next = %q, want reply", got)
	}
	m.searchNext(1)
	if got := m.currentItem().DisplayText(); got != "ALPHA idea" {
		t.Errorf("next = %q, want ALPHA idea", got)
	}
	// Wrap back to the first.
	m.searchNext(1)
	if got := m.currentItem().DisplayText(); got != "alpha task" {
		t.Errorf("wrap = %q, want alpha task", got)
	}
	if !strings.HasPrefix(m.status, "1/3 matches") {
		t.Errorf("status = %q, want it to start with 1/3 matches", m.status)
	}
	// Previous from the first wraps to the last.
	m.searchNext(-1)
	if got := m.currentItem().DisplayText(); got != "ALPHA idea" {
		t.Errorf("prev-wrap = %q, want ALPHA idea", got)
	}
}

func TestSearchRevealsCollapsedSection(t *testing.T) {
	m := newTestModel(searchBoard, 80, 40)
	// Collapse Ideas, where "ALPHA idea" lives; its item drops out of positions.
	m.setCollapsed("Ideas", true)
	m.rebuildPositions()
	for _, p := range m.positions {
		if p.item != nil && p.item.DisplayText() == "ALPHA idea" {
			t.Fatal("precondition: ALPHA idea should be hidden while Ideas collapsed")
		}
	}
	typeSearch(m, "ALPHA idea")
	if m.sectionCollapsed("Ideas") {
		t.Error("Ideas still collapsed after matching inside it")
	}
	if it := m.currentItem(); it == nil || it.DisplayText() != "ALPHA idea" {
		t.Fatalf("cursor did not reveal+land on the collapsed match; on %v", it)
	}
}

func TestSearchEscRestoresCursor(t *testing.T) {
	m := newTestModel(searchBoard, 80, 40)
	m.cursor = 1 // some deliberate starting stop
	start := m.cursor
	typeSearch(m, "idea") // jumps away to ALPHA idea
	if m.cursor == start {
		t.Fatal("precondition: search should have moved the cursor")
	}
	m.handleSearchKey(tea.KeyMsg{Type: tea.KeyEsc})
	if m.cursor != start {
		t.Errorf("esc left cursor at %d, want restored %d", m.cursor, start)
	}
	if m.searchActive {
		t.Error("esc should leave search inactive")
	}
}

// searchTint is the SGR foreground for a whole matching line/node (color 120);
// searchAccent (228) is the substring accent. Both appear only while search is
// active, letting the highlight tests assert presence/absence.
const (
	searchTint   = "38;5;120"
	searchAccent = "38;5;228"
)

func TestSearchHighlightOutline(t *testing.T) {
	forceColor(t)
	m := newTestModel(searchBoard, 80, 40)
	// Live (prompt open): "alpha" matches three items; the cursor lands on the
	// first, the other two get the whole-line tint, and the substring is accented.
	typeSearch(m, "alpha")
	out := m.viewBoard()
	if !strings.Contains(out, searchTint) {
		t.Errorf("expected whole-line search tint (%s) during active search:\n%q", searchTint, out)
	}
	if !strings.Contains(out, searchAccent) {
		t.Errorf("expected matched-substring accent (%s) during active search:\n%q", searchAccent, out)
	}
	// Esc closes search entirely: the highlight clears.
	m.handleSearchKey(tea.KeyMsg{Type: tea.KeyEsc})
	out = m.viewBoard()
	if strings.Contains(out, searchTint) || strings.Contains(out, searchAccent) {
		t.Errorf("expected no search highlight after esc:\n%q", out)
	}
}

// TestSearchResultsModeSticky: after Enter, the search enters a persistent
// results mode. Ordinary navigation (n/N, and plain moves like j) keeps the
// highlight + query alive; only Esc clears it. This is the "too easy to get out
// of search" fix.
func TestSearchResultsModeSticky(t *testing.T) {
	forceColor(t)
	m := newTestModel(searchBoard, 80, 40)
	typeSearch(m, "alpha")
	m.handleSearchKey(tea.KeyMsg{Type: tea.KeyEnter}) // confirm: results mode
	if !strings.Contains(m.viewBoard(), searchTint) {
		t.Fatal("expected highlight to persist after enter (results mode)")
	}
	// n keeps results mode (and highlight) alive.
	m.handleBoardKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if !strings.Contains(m.viewBoard(), searchTint) {
		t.Error("n should keep the highlight alive")
	}
	// A plain move (j) must NOT drop out of results mode any more.
	m.handleBoardKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if !m.searchActive {
		t.Error("j should keep results mode active (sticky search)")
	}
	if !strings.Contains(m.viewBoard(), searchTint) {
		t.Errorf("expected highlight to persist after a nav action:\n%q", m.viewBoard())
	}
	// Only Esc clears the search.
	m.handleBoardKey(tea.KeyMsg{Type: tea.KeyEsc})
	if m.searchActive {
		t.Error("esc should clear results mode")
	}
	if strings.Contains(m.viewBoard(), searchTint) {
		t.Errorf("expected highlight cleared after esc:\n%q", m.viewBoard())
	}
}

// TestSearchLastTermRecall: `/` pre-fills the previous term (Enter reuses it),
// and n/N with no active search re-activate the last term.
func TestSearchLastTermRecall(t *testing.T) {
	m := newTestModel(searchBoard, 80, 40)
	typeSearch(m, "alpha")
	m.handleSearchKey(tea.KeyMsg{Type: tea.KeyEnter})
	// Esc out of results mode; the last term is remembered.
	m.handleBoardKey(tea.KeyMsg{Type: tea.KeyEsc})
	if m.searchActive {
		t.Fatal("precondition: esc should have cleared results mode")
	}
	// `/` recalls the term into a fresh prompt (selected/replaceable).
	m.startSearch()
	if m.input.Value() != "alpha" {
		t.Errorf("startSearch did not recall last term: %q", m.input.Value())
	}
	if !m.searchPrefilled {
		t.Error("recalled term should be marked prefilled (replaceable)")
	}
	// Enter reuses the recalled term as-is.
	m.handleSearchKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.searchActive || m.searchQuery != "alpha" {
		t.Errorf("Enter on recalled term did not confirm alpha (active=%v q=%q)", m.searchActive, m.searchQuery)
	}
	// Esc, then n with no active search re-activates the last term.
	m.handleBoardKey(tea.KeyMsg{Type: tea.KeyEsc})
	if m.searchActive {
		t.Fatal("precondition: esc should clear before recall-via-n")
	}
	if !m.searchNext(1) {
		t.Fatal("n with no active search should re-activate the last term")
	}
	if !m.searchActive || m.searchQuery != "alpha" {
		t.Errorf("n recall did not re-activate alpha (active=%v q=%q)", m.searchActive, m.searchQuery)
	}
}

// TestSearchRecallResumesFromLastMatch: after stepping into the match set and
// leaving search, re-activating with n resumes from where it left off (the next
// match after the last position), not the top.
func TestSearchRecallResumesFromLastMatch(t *testing.T) {
	m := newTestModel(searchBoard, 80, 40)
	typeSearch(m, "alpha") // 3 matches; idx 0 = "alpha task"
	m.handleSearchKey(tea.KeyMsg{Type: tea.KeyEnter})
	m.searchNext(1) // idx 1 = "claude: alpha reply here"
	if got := m.currentItem().DisplayText(); got != "claude: alpha reply here" {
		t.Fatalf("precondition: expected to be on the reply, got %q", got)
	}
	// Leave results mode; the position (idx 1) is remembered.
	m.handleBoardKey(tea.KeyMsg{Type: tea.KeyEsc})
	// n re-activates and resumes: next after idx 1 is idx 2 ("ALPHA idea").
	if !m.searchNext(1) {
		t.Fatal("n should re-activate the last term")
	}
	if got := m.currentItem().DisplayText(); got != "ALPHA idea" {
		t.Errorf("recall did not resume from last position: got %q, want ALPHA idea", got)
	}
	// A subsequent N from here steps back to the reply again.
	m.searchNext(-1)
	if got := m.currentItem().DisplayText(); got != "claude: alpha reply here" {
		t.Errorf("N after resume = %q, want the reply", got)
	}
}

// TestSearchPrefillReplacedByTyping: typing a rune over a recalled term replaces
// it wholesale rather than appending.
func TestSearchPrefillReplacedByTyping(t *testing.T) {
	m := newTestModel(searchBoard, 80, 40)
	typeSearch(m, "alpha")
	m.handleSearchKey(tea.KeyMsg{Type: tea.KeyEnter})
	m.handleBoardKey(tea.KeyMsg{Type: tea.KeyEsc})
	m.startSearch() // recalls "alpha", prefilled
	m.handleSearchKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}})
	if got := m.input.Value(); got != "b" {
		t.Errorf("typing over a recalled term = %q, want %q (wholesale replace)", got, "b")
	}
}

// TestSearchInputUnknownKeyNoop: an unhandled key in the prompt (input mode)
// must not exit search — it stays in modeSearch.
func TestSearchInputUnknownKeyNoop(t *testing.T) {
	m := newTestModel(searchBoard, 80, 40)
	m.startSearch()
	// ctrl+g is not enter/esc and not printable: it must be swallowed, staying
	// in the prompt rather than dropping to the board.
	m.handleSearchKey(tea.KeyMsg{Type: tea.KeyCtrlG})
	if m.mode != modeSearch {
		t.Errorf("unknown key exited search prompt: mode=%v", m.mode)
	}
}

func TestSearchHighlightMap(t *testing.T) {
	forceColor(t)
	m := newMapModel(t, searchBoard, 120, 40)
	typeSearch(m, "alpha") // focus lands on a match; other matches tint
	if out := m.viewMap(); !strings.Contains(out, searchTint) {
		t.Errorf("expected search tint (%s) in map during active search:\n%q", searchTint, out)
	}
	m.handleSearchKey(tea.KeyMsg{Type: tea.KeyEsc})
	if out := m.viewMap(); strings.Contains(out, searchTint) {
		t.Errorf("expected no search tint in map after esc:\n%q", out)
	}
}

func TestSearchMapJumpFocusesMatch(t *testing.T) {
	m := newMapModel(t, searchBoard, 120, 40)
	typeSearch(m, "gamma")
	it := m.itemForKey(m.mapFocusKey)
	if it == nil || it.DisplayText() != "gamma topic" {
		t.Fatalf("map focus key %q did not resolve to gamma topic (got %v)", m.mapFocusKey, it)
	}
}

// TestSearchEscRestoresMapRoot: Esc after a search restores the pre-search map
// zoom (focus root), even when the search jumped to a match outside the focused
// subtree (which clears mapFocusRoot on the way in). Web-parity with cancelSearch.
func TestSearchEscRestoresMapRoot(t *testing.T) {
	m := newMapModel(t, searchBoard, 120, 40)
	focusMapSection(t, m, "Plan")
	m.focusIn() // zoom into the Plan subtree
	savedRoot := m.mapFocusRoot
	savedKey := m.mapFocusKey
	if savedRoot == "" {
		t.Fatal("focusIn did not set a map focus root")
	}

	// gamma lives in Threads, outside the Plan subtree, so jumpToMatch clears root.
	typeSearch(m, "gamma")
	if m.mapFocusRoot == savedRoot {
		t.Fatal("search to an out-of-subtree match did not clear the focus root (test precondition)")
	}
	m.handleSearchKey(tea.KeyMsg{Type: tea.KeyEsc})
	if m.mapFocusRoot != savedRoot {
		t.Errorf("Esc left focus root = %q, want %q", m.mapFocusRoot, savedRoot)
	}
	if m.mapFocusKey != savedKey {
		t.Errorf("Esc left focus key = %q, want %q", m.mapFocusKey, savedKey)
	}
}

// TestMapUrgentToggle: `!` on a map item toggles urgent (list/web parity), and
// `S` opens the surprise recorder instead.
func TestMapUrgentToggle(t *testing.T) {
	m := newMapModel(t, "## Threads\n- [ ] task\n", 80, 24)
	focusMapItem(t, m, "task")
	m.handleMapKey(keyPress("!"))
	if it := m.itemForKey(m.mapFocusKey); it == nil || !it.Urgent {
		t.Fatalf("! did not set urgent on the map item: %+v", it)
	}
	if m.mode == modeMapFeedback {
		t.Fatal("! should toggle urgent, not open the feedback recorder")
	}
	m.handleMapKey(keyPress("!"))
	if it := m.itemForKey(m.mapFocusKey); it == nil || it.Urgent {
		t.Fatalf("second ! did not clear urgent: %+v", it)
	}
}

func TestSearchMapRevealsFoldedReply(t *testing.T) {
	m := newMapModel(t, searchBoard, 120, 40)
	typeSearch(m, "alpha reply")
	it := m.itemForKey(m.mapFocusKey)
	if it == nil || it.DisplayText() != "claude: alpha reply here" {
		t.Fatalf("map focus did not reveal the folded reply (got %v)", it)
	}
}
