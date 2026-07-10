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

// Item-level l/h follow the map's expand-or-descend / collapse-or-ascend
// convention in the outline: l descends into the first child, h steps out to
// the parent (section header for a top-level item).

func TestOutlineLDescendsIntoChild(t *testing.T) {
	m := newTestModel("## Threads\n- [ ] parent\n  - claude: a reply\n", 100, 40)
	m.handleBoardKey(keyPress("j")) // onto "parent"
	if it := m.currentItem(); it == nil || it.Text != "parent" {
		t.Fatalf("expected cursor on parent, got %v", m.currentItem())
	}
	m.handleBoardKey(keyPress("l")) // descend to the first child
	if it := m.currentItem(); it == nil || it.Text != "claude: a reply" {
		t.Fatalf("l should descend to the first child, got %v", m.currentItem())
	}
}

func TestOutlineLNoOpOnLeafItem(t *testing.T) {
	m := newTestModel("## Plan\n- [ ] only\n", 100, 40)
	m.handleBoardKey(keyPress("j")) // onto "only"
	m.handleBoardKey(keyPress("l")) // leaf: nothing to descend into
	if it := m.currentItem(); it == nil || it.Text != "only" {
		t.Fatalf("l on a leaf item must stay put, got %v", m.currentItem())
	}
}

// Child memory: h/left records the child it ascended from, and l/right returns
// to that child instead of the first (web outline parity, a317278).

func TestOutlineChildMemoryOnItem(t *testing.T) {
	src := "## Threads\n- [ ] parent\n  - [ ] first child\n  - [ ] second child\n"
	m := newTestModel(src, 100, 40)
	m.handleBoardKey(keyPress("j")) // parent
	m.handleBoardKey(keyPress("j")) // first child
	m.handleBoardKey(keyPress("j")) // second child
	if it := m.currentItem(); it == nil || it.Text != "second child" {
		t.Fatalf("setup: expected second child, got %v", m.currentItem())
	}
	m.handleBoardKey(keyPress("h")) // ascend, remembering "second child"
	if it := m.currentItem(); it == nil || it.Text != "parent" {
		t.Fatalf("h should land on parent, got %v", m.currentItem())
	}
	m.handleBoardKey(keyPress("l")) // re-enter the remembered child
	if it := m.currentItem(); it == nil || it.Text != "second child" {
		t.Fatalf("l should return to the ascended-from child, got %v", m.currentItem())
	}
}

func TestOutlineChildMemoryOnSectionHeader(t *testing.T) {
	src := "## Plan\n- [ ] one\n- [ ] two\n"
	m := newTestModel(src, 100, 40)
	m.handleBoardKey(keyPress("j")) // one
	m.handleBoardKey(keyPress("j")) // two
	m.handleBoardKey(keyPress("h")) // ascend to the Plan header, remembering "two"
	if _, ok := m.onHeader(); !ok {
		t.Fatal("h should land on the header")
	}
	m.handleBoardKey(keyPress("enter")) // collapse the section
	m.handleBoardKey(keyPress("l"))     // expand + land on the remembered child
	if it := m.currentItem(); it == nil || it.Text != "two" {
		t.Fatalf("l should re-enter the remembered child, got %v", m.currentItem())
	}
}

func TestOutlineChildMemoryFallsBackToFirst(t *testing.T) {
	src := "## Threads\n- [ ] parent\n  - [ ] first child\n  - [ ] second child\n"
	m := newTestModel(src, 100, 40)
	m.handleBoardKey(keyPress("j")) // parent
	// Stale memory: remember a child raw line that no longer exists.
	m.outlineChildMem = map[string]string{m.currentItem().Raw(): "- [ ] gone"}
	m.handleBoardKey(keyPress("l"))
	if it := m.currentItem(); it == nil || it.Text != "first child" {
		t.Fatalf("stale memory should fall back to the first child, got %v", m.currentItem())
	}
}

func TestOutlineHAscendsToParent(t *testing.T) {
	m := newTestModel("## Threads\n- [ ] parent\n  - claude: a reply\n", 100, 40)
	m.handleBoardKey(keyPress("j")) // parent
	m.handleBoardKey(keyPress("j")) // the reply (child)
	if it := m.currentItem(); it == nil || it.Text != "claude: a reply" {
		t.Fatalf("expected cursor on the reply, got %v", m.currentItem())
	}
	m.handleBoardKey(keyPress("h")) // step out to the parent
	if it := m.currentItem(); it == nil || it.Text != "parent" {
		t.Fatalf("h should ascend to the parent, got %v", m.currentItem())
	}
}

func TestOutlineHOnTopLevelGoesToHeader(t *testing.T) {
	m := newTestModel("## Plan\n- [ ] first\n", 100, 40)
	m.handleBoardKey(keyPress("j")) // onto "first" (top-level)
	m.handleBoardKey(keyPress("h")) // step out to the section header
	if _, ok := m.onHeader(); !ok {
		t.Fatal("h on a top-level item should land on the section header")
	}
}
