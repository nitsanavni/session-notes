package tui

import "testing"

// Single-press expand-and-nav: descending into a collapsed parent must be ONE
// keystroke — the same press expands the fold AND lands the selection on the
// child (remembered child where child memory exists, else the first). It must
// never take a first press that only expands and a second that moves.

func TestMapSinglePressDescendsClosedNode(t *testing.T) {
	src := "## Threads\n- [ ] parent\n  - [ ] child one\n  - [ ] child two\n"
	m := newMapModel(t, src, 120, 40)
	focusMapItem(t, m, "parent")
	m.mapFold = map[string]foldState{m.mapFocusKey: foldCollapsed}
	m.mp = nil
	m.ensureMap()
	focusMapItem(t, m, "parent") // re-resolve the node after the rebuild

	m.mapLateral(m.mp.sideOf(m.mp.focus)) // ONE outward press
	ref := m.mp.refs[m.mp.focus]
	if ref.kind != refItem || ref.item.Text != "child one" {
		t.Fatalf("one press should expand and land on the first child, got %+v", ref)
	}
}

func TestMapSinglePressDescendsClosedRepliesOnlyNode(t *testing.T) {
	// A closed node whose children are all conversational replies used to need
	// TWO presses (closed -> default reveals nothing, default -> all moves).
	src := "## Threads\n- [ ] parent\n  - claude: reply one\n  - claude: reply two\n- [ ] other\n"
	m := newMapModel(t, src, 120, 40)
	focusMapItem(t, m, "parent")
	m.mapFold = map[string]foldState{m.mapFocusKey: foldCollapsed}
	m.mp = nil
	m.ensureMap()
	focusMapItem(t, m, "parent")

	m.mapLateral(m.mp.sideOf(m.mp.focus)) // ONE outward press
	ref := m.mp.refs[m.mp.focus]
	if ref.kind != refItem || ref.item.Text != "claude: reply one" {
		t.Fatalf("one press should expand the reply thread and land on the first reply, got %+v", ref)
	}
}

func TestOutlineLDescendsIntoCollapsedSection(t *testing.T) {
	m := newTestModel("## Plan\n- [ ] first\n- [ ] second\n", 100, 40)
	// Cursor starts on the Plan header (position 0); collapse it.
	m.handleBoardKey(keyPress("enter"))
	if _, ok := m.onHeader(); !ok {
		t.Fatal("cursor should sit on the collapsed header")
	}
	m.handleBoardKey(keyPress("l")) // ONE press: expand AND land on the first item
	if m.sectionCollapsed("Plan") {
		t.Fatal("l should have expanded the section")
	}
	if it := m.currentItem(); it == nil || it.Text != "first" {
		t.Fatalf("one press should land on the first item, got %v", m.cursor)
	}
}

func TestOutlineLNoOpOnExpandedHeader(t *testing.T) {
	m := newTestModel("## Plan\n- [ ] first\n", 100, 40)
	m.handleBoardKey(keyPress("l")) // expanded header: l never collapses (web parity)
	if m.sectionCollapsed("Plan") {
		t.Fatal("l on an expanded header must not collapse it")
	}
	if _, ok := m.onHeader(); !ok {
		t.Fatal("cursor should stay on the header")
	}
}
