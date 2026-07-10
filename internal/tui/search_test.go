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

// TestSearchNoRevealUntilEnter: while typing, the panel populates but the board
// does NOT move (no-reveal-until-jump). Enter jumps to the selected (best-ranked)
// match.
func TestSearchNoRevealUntilEnter(t *testing.T) {
	m := newTestModel(searchBoard, 80, 40)
	m.cursor = 0 // on the Plan header
	typeSearch(m, "beta")
	if len(m.searchResults) == 0 {
		t.Fatal("panel should be populated while typing")
	}
	if m.cursor != 0 {
		t.Errorf("board moved while typing (cursor=%d); no-reveal violated", m.cursor)
	}
	// Enter jumps to the selected panel row.
	m.handleSearchKey(tea.KeyMsg{Type: tea.KeyEnter})
	it := m.currentItem()
	if it == nil || it.DisplayText() != "beta task" {
		t.Fatalf("Enter did not land on the match; on %v", it)
	}
}

func TestSearchNextWrapsAndReportsCount(t *testing.T) {
	m := newTestModel(searchBoard, 80, 40)
	typeSearch(m, "alpha")
	// Enter selects the best-ranked row and CLOSES search (commit); n/N recall
	// then resumes stepping from that landing position.
	m.handleSearchKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.searchActive {
		t.Fatal("selecting a row should close search (not stay in results mode)")
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
	// No-reveal: typing must NOT expand the collapsed section.
	if !m.sectionCollapsed("Ideas") {
		t.Error("typing revealed the collapsed section; no-reveal violated")
	}
	// Enter jumps: only now does the collapsed section reveal + land.
	m.handleSearchKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.sectionCollapsed("Ideas") {
		t.Error("Ideas still collapsed after jumping into it")
	}
	if it := m.currentItem(); it == nil || it.DisplayText() != "ALPHA idea" {
		t.Fatalf("cursor did not reveal+land on the collapsed match; on %v", it)
	}
}

// A search jump into a folded subtree un-folds the ancestor items so the match
// becomes a nav stop and the cursor lands on it.
func TestSearchRevealsThroughItemFold(t *testing.T) {
	src := "## Threads\n- [ ] parent\n  - [ ] child\n    - claude: BURIED treasure\n"
	m := newTestModel(src, 80, 40)
	parent := m.board.Sections[0].Items[0]
	m.setItemFolded("Threads", parent, true)
	m.rebuildPositions()
	for _, p := range m.positions {
		if p.item != nil && p.item.DisplayText() == "claude: BURIED treasure" {
			t.Fatal("precondition: buried match should be hidden while parent folded")
		}
	}
	typeSearch(m, "BURIED")
	if !m.itemFolded("Threads", parent) {
		t.Error("typing revealed the fold; no-reveal violated")
	}
	m.handleSearchKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.itemFolded("Threads", parent) {
		t.Error("jump should un-fold the ancestor item")
	}
	if it := m.currentItem(); it == nil || it.DisplayText() != "claude: BURIED treasure" {
		t.Fatalf("cursor did not reveal+land on the folded match; on %v", it)
	}
}

func TestSearchEscRestoresCursor(t *testing.T) {
	m := newTestModel(searchBoard, 80, 40)
	m.cursor = 1 // some deliberate starting stop
	start := m.cursor
	typeSearch(m, "idea") // populates the panel; board stays put (no-reveal)
	m.movePanel(1)        // navigate the panel — still no board move
	if m.cursor != start {
		t.Fatalf("panel navigation moved the board (cursor=%d); no-reveal violated", m.cursor)
	}
	m.handleSearchKey(tea.KeyMsg{Type: tea.KeyEsc})
	if m.cursor != start {
		t.Errorf("esc left cursor at %d, want restored %d", m.cursor, start)
	}
	if m.searchActive {
		t.Error("esc should leave search inactive")
	}
}

// TestFuzzyScoreTierOrder: an exact substring beats a word-boundary subsequence,
// which beats a loose subsequence. All three still match "abc".
func TestFuzzyScoreTierOrder(t *testing.T) {
	exact, _, ok1 := fuzzyScore("abc here", "abc")
	wordBnd, _, ok2 := fuzzyScore("abx bce cxx", "abc") // a..b at word starts, subsequence
	loose, _, ok3 := fuzzyScore("xaxbxc", "abc")        // buried subsequence, no boundary
	if !ok1 || !ok2 || !ok3 {
		t.Fatalf("all should match: %v %v %v", ok1, ok2, ok3)
	}
	if !(exact > wordBnd && wordBnd > loose) {
		t.Errorf("tier order wrong: exact=%d wordBnd=%d loose=%d", exact, wordBnd, loose)
	}
	if _, _, ok := fuzzyScore("no match", "abc"); ok {
		t.Error("non-subsequence should not match")
	}
}

// TestFuzzyScoreEarlierPositionWins: for equal tier/contiguity, an earlier match
// position scores higher.
func TestFuzzyScoreEarlierPositionWins(t *testing.T) {
	early, _, _ := fuzzyScore("foo bar", "foo")
	late, _, _ := fuzzyScore("xxxxxxx foo", "foo")
	if early <= late {
		t.Errorf("earlier match should win: early=%d late=%d", early, late)
	}
}

// TestSearchRankedExactBeatsLoose: the ranked panel orders an exact substring
// match ahead of a merely-subsequence one, regardless of document order.
func TestSearchRankedExactBeatsLoose(t *testing.T) {
	src := "---\ntitle: T\n---\n" +
		"## Plan\n" +
		"- [ ] a big cat sat\n" + // "abc" only as a loose subsequence
		"- [ ] abcdef exact\n" // contains "abc" verbatim
	b := board.Parse(src)
	got := searchRanked(b, "abc", false)
	if len(got) != 2 {
		t.Fatalf("want 2 ranked results, got %d", len(got))
	}
	if got[0].item.DisplayText() != "abcdef exact" {
		t.Errorf("exact substring should rank first, got %q", got[0].item.DisplayText())
	}
}

// TestSearchRankedWorkingAboveArchive: with scope=all, working-set sections rank
// above Archive/Log even when the Archive match scores higher on position.
func TestSearchRankedWorkingAboveArchive(t *testing.T) {
	src := "---\ntitle: T\n---\n" +
		"## Archive\n- widget\n" + // exact, position 0
		"## Plan\n- [ ] a widget here\n"
	b := board.Parse(src)
	got := searchRanked(b, "widget", true)
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	if !got[0].working || got[0].section != "Plan" {
		t.Errorf("working section should rank first, got section=%q working=%v", got[0].section, got[0].working)
	}
}

// TestSearchPanelNavAndEnterSyncsCursor: Up/Down move the panel selection without
// moving the board; Enter on the selected row jumps the board cursor to it.
func TestSearchPanelNavAndEnterSyncsCursor(t *testing.T) {
	m := newTestModel(searchBoard, 80, 40)
	m.cursor = 0
	typeSearch(m, "alpha") // 3 ranked results
	if len(m.searchResults) != 3 {
		t.Fatalf("want 3 results, got %d", len(m.searchResults))
	}
	m.handleSearchKey(tea.KeyMsg{Type: tea.KeyDown}) // panel -> row 1
	if m.searchPanelIdx != 1 {
		t.Errorf("Down did not move panel selection: idx=%d", m.searchPanelIdx)
	}
	if m.cursor != 0 {
		t.Errorf("panel navigation moved the board (cursor=%d)", m.cursor)
	}
	want := m.searchResults[1].item.DisplayText()
	m.handleSearchKey(tea.KeyMsg{Type: tea.KeyEnter})
	if it := m.currentItem(); it == nil || it.DisplayText() != want {
		t.Errorf("Enter did not sync board to selected row %q, got %v", want, it)
	}
}

// TestSearchNextSyncsPanel: in results mode, n steps the board (document order)
// and keeps the panel's highlighted row pointed at the same item.
func TestSearchNextSyncsPanel(t *testing.T) {
	m := newTestModel(searchBoard, 80, 40)
	typeSearch(m, "alpha")
	m.handleSearchKey(tea.KeyMsg{Type: tea.KeyEnter})
	m.searchNext(1)
	cur := m.currentItem()
	if cur == nil {
		t.Fatal("no current item after n")
	}
	if m.searchResults[m.searchPanelIdx].item != cur {
		t.Errorf("panel row out of sync with board after n: panel=%q board=%q",
			m.searchResults[m.searchPanelIdx].item.DisplayText(), cur.DisplayText())
	}
}

// TestSearchTempUnwrapCurrentMatch: when a match's text is long enough that the
// matched substring is truncated off-screen, stepping onto it temporarily
// unwraps that node; stepping away restores its wrapped state.
func TestSearchTempUnwrapCurrentMatch(t *testing.T) {
	long := strings.Repeat("x", 100) + " needle"
	src := "---\ntitle: T\n---\n## Plan\n- [ ] " + long + "\n- [ ] needle short\n"
	m := newTestModel(src, 40, 40) // narrow: the first needle is beyond the clip
	typeSearch(m, "needle")
	m.handleSearchKey(tea.KeyMsg{Type: tea.KeyEnter}) // jump to best match
	// Find the long item and step onto it via n/N until current.
	var longRaw string
	for _, p := range m.positions {
		if p.item != nil && strings.Contains(p.item.DisplayText(), "xxxx") {
			longRaw = p.item.Raw()
		}
	}
	if longRaw == "" {
		t.Fatal("could not find the long item")
	}
	// Step through matches until the long item is current.
	unwrapped := false
	for i := 0; i < 4; i++ {
		if m.currentItem() != nil && m.currentItem().Raw() == longRaw {
			if m.listExpanded[longRaw] {
				unwrapped = true
			}
			break
		}
		m.searchNext(1)
	}
	if m.currentItem() != nil && m.currentItem().Raw() == longRaw && !m.listExpanded[longRaw] {
		t.Error("current long match should be temporarily unwrapped to reveal the needle")
	}
	_ = unwrapped
	// Step away: the temporary unwrap is restored.
	m.searchNext(1)
	if m.currentItem() != nil && m.currentItem().Raw() != longRaw && m.listExpanded[longRaw] {
		t.Error("temporary unwrap should be restored after stepping past the match")
	}
}

// TestSearchHitCountInStatus: a term occurring several times inside one node is
// counted as multiple "hits" even though it is one match node, and the status
// surfaces the hit tally when it exceeds the node count.
func TestSearchHitCountInStatus(t *testing.T) {
	src := "---\ntitle: T\n---\n## Plan\n- [ ] widget widget widget here\n- [ ] one widget\n"
	m := newTestModel(src, 80, 40)
	typeSearch(m, "widget") // 2 nodes, 3+1 = 4 literal hits
	if got := searchHitCount(m.searchResults, "widget"); got != 4 {
		t.Errorf("hit count = %d, want 4", got)
	}
	if !strings.Contains(m.status, "· 4 hits") {
		t.Errorf("status should surface the hit tally: %q", m.status)
	}
	// When hits == node count, the hits clause is omitted.
	typeSearch(m, "one")
	if strings.Contains(m.status, "hits") {
		t.Errorf("status should omit hits when they equal the node count: %q", m.status)
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

// While TYPING at the search prompt the footer already shows the live match
// tally (web parity: the bar counts as you type), not only after Enter.
func TestSearchPromptShowsLiveCount(t *testing.T) {
	m := newTestModel(searchBoard, 80, 40)
	typeSearch(m, "alpha") // 3 matches in the working set
	out := stripANSI(m.View())
	if !strings.Contains(out, "/3 matches") {
		t.Fatalf("prompt view should show the live match count, got:\n%s", out)
	}
	if !strings.Contains(out, "(working set)") {
		t.Errorf("prompt view should carry the scope indicator, got:\n%s", out)
	}
	// A no-match query says so, live.
	typeSearch(m, "zzzznope")
	if out := stripANSI(m.View()); !strings.Contains(out, "no matches") {
		t.Errorf("prompt view should say 'no matches' live, got:\n%s", out)
	}
}

// On a LIGHT terminal theme the search accents adapt: the matched-substring
// accent renders magenta 126 (bright yellow 228 is invisible on white) and the
// whole-line tint a dark green 28. Guards the theme-adaptive palette.
func TestSearchHighlightAdaptsToLightTheme(t *testing.T) {
	forceColor(t)
	origDark := lipgloss.HasDarkBackground()
	lipgloss.SetHasDarkBackground(false)
	t.Cleanup(func() { lipgloss.SetHasDarkBackground(origDark) })
	m := newTestModel(searchBoard, 80, 40)
	typeSearch(m, "alpha")
	out := m.viewBoard()
	if !strings.Contains(out, "38;5;126") {
		t.Errorf("light theme should use the magenta match accent (38;5;126):\n%q", out)
	}
	if !strings.Contains(out, "38;5;28m") {
		t.Errorf("light theme should use the dark-green line tint (38;5;28):\n%q", out)
	}
	if strings.Contains(out, "38;5;228") {
		t.Errorf("light theme must not use the dark-theme yellow 228:\n%q", out)
	}
}

// Stepping matches with n/N highlights the matched term inside the current
// match's rendered line — the substring accent survives the recall + step path,
// so cycling always shows WHERE the term is, not just WHICH row is current.
func TestSearchNextAccentsCurrentMatchTerm(t *testing.T) {
	forceColor(t)
	m := newTestModel(searchBoard, 80, 40)
	typeSearch(m, "alpha")
	m.handleSearchKey(tea.KeyMsg{Type: tea.KeyEnter}) // commit + close
	// n recalls the term into sticky results mode and steps to the next match.
	m.handleBoardKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	out := m.viewBoard()
	if !strings.Contains(out, searchAccent) {
		t.Fatalf("expected the matched term accented (%s) on the current match after n:\n%q", searchAccent, out)
	}
	// N steps back and must keep the accent live.
	m.handleBoardKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'N'}})
	if !strings.Contains(m.viewBoard(), searchAccent) {
		t.Fatal("the term accent must survive an N step")
	}
}

// TestSearchResultsModeSticky: selecting a row (Enter) CLOSES search (highlights
// clear). Pressing n then re-activates via recall into a persistent results
// mode where ordinary navigation (n/N, and plain moves like j) keeps the
// highlight + query alive and only Esc clears it.
func TestSearchResultsModeSticky(t *testing.T) {
	forceColor(t)
	m := newTestModel(searchBoard, 80, 40)
	typeSearch(m, "alpha")
	m.handleSearchKey(tea.KeyMsg{Type: tea.KeyEnter}) // select + close
	if m.searchActive {
		t.Fatal("Enter should close search (commit), not enter results mode")
	}
	if strings.Contains(m.viewBoard(), searchTint) {
		t.Fatal("highlights should clear on select")
	}
	// n re-activates the last term (recall) into sticky results mode.
	m.handleBoardKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if !m.searchActive || !strings.Contains(m.viewBoard(), searchTint) {
		t.Fatal("n should re-activate sticky results mode with highlights")
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
	// Enter reuses the recalled term as-is (selects + closes; term remembered).
	m.handleSearchKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.searchActive || m.searchLastTerm != "alpha" {
		t.Errorf("Enter on recalled term did not commit alpha (active=%v last=%q)", m.searchActive, m.searchLastTerm)
	}
	// n with no active search re-activates the last term.
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
	m.handleSearchKey(tea.KeyMsg{Type: tea.KeyEnter})
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

	// gamma lives in Threads, outside the Plan subtree, so jumping clears root.
	typeSearch(m, "gamma")
	m.handleSearchKey(tea.KeyMsg{Type: tea.KeyEnter})
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
	m.handleSearchKey(tea.KeyMsg{Type: tea.KeyEnter})
	it := m.itemForKey(m.mapFocusKey)
	if it == nil || it.DisplayText() != "claude: alpha reply here" {
		t.Fatalf("map focus did not reveal the folded reply (got %v)", it)
	}
}

// TestPanelSnippet checks the head-trim that keeps a deep match visible.
func TestPanelSnippet(t *testing.T) {
	// No trim when the match is near the start.
	if got := panelSnippet("alpha here", [][2]int{{0, 5}}, 18); got != "alpha here" {
		t.Fatalf("near-start should be unchanged, got %q", got)
	}
	// No ranges -> unchanged.
	if got := panelSnippet("some text", nil, 18); got != "some text" {
		t.Fatalf("no ranges should be unchanged, got %q", got)
	}
	// Deep match trims the head, prepends "…", keeps ~lead columns of context,
	// and keeps the matched substring visible.
	long := "the quick brown fox jumps over the lazy dog and then finds NEEDLE at the end"
	idx := strings.Index(long, "NEEDLE")
	got := panelSnippet(long, [][2]int{{idx, idx + len("NEEDLE")}}, 18)
	if !strings.HasPrefix(got, "…") {
		t.Fatalf("deep match should lead with an ellipsis, got %q", got)
	}
	if !strings.Contains(got, "NEEDLE") {
		t.Fatalf("snippet must still contain the match, got %q", got)
	}
	if len(got) >= len(long) {
		t.Fatalf("snippet should be shorter than the original, got %q", got)
	}
}
