package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/nitsanavni/session-notes/internal/board"
)

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
	got := searchMatches(b, "alpha")
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
	if got := searchMatches(b, "   "); got != nil {
		t.Errorf("whitespace query matched %d items, want none", len(got))
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
	if m.status != "1/3 matches" {
		t.Errorf("status = %q, want 1/3 matches", m.status)
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

func TestSearchMapJumpFocusesMatch(t *testing.T) {
	m := newMapModel(t, searchBoard, 120, 40)
	typeSearch(m, "gamma")
	it := m.itemForKey(m.mapFocusKey)
	if it == nil || it.DisplayText() != "gamma topic" {
		t.Fatalf("map focus key %q did not resolve to gamma topic (got %v)", m.mapFocusKey, it)
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
