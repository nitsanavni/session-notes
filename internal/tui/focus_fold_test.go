package tui

import (
	"strings"
	"testing"

	"github.com/nitsanavni/session-notes/internal/board"
)

// z (focus-fold / zoom) in the TUI outline: collapse every section except the
// cursor's; a second z restores the exact pre-zoom collapse state; the cursor
// stays on the same item across both directions.

const zoomSrc = "## Plan\n- [ ] one\n## Threads\n- [ ] talk\n  - claude: sure\n## Ideas\n- spark\n"

func TestFocusFoldCollapsesOtherSections(t *testing.T) {
	m := newTestModel(zoomSrc, 100, 40)
	m.handleBoardKey(keyPress("tab")) // Threads header
	m.handleBoardKey(keyPress("j"))   // "talk"
	m.handleBoardKey(keyPress("z"))
	if m.sectionCollapsed("Threads") {
		t.Fatal("the cursor's section must stay expanded")
	}
	if !m.sectionCollapsed("Plan") || !m.sectionCollapsed("Ideas") {
		t.Fatalf("other sections should collapse: Plan=%v Ideas=%v",
			m.sectionCollapsed("Plan"), m.sectionCollapsed("Ideas"))
	}
	if it := m.currentItem(); it == nil || it.Text != "talk" {
		t.Fatalf("cursor should stay on the zoom anchor, got %v", m.currentItem())
	}
}

func TestFocusFoldToggleRestoresPriorFolds(t *testing.T) {
	m := newTestModel(zoomSrc, 100, 40)
	// Pre-existing manual fold: collapse Plan (a non-default state).
	m.setCollapsed("Plan", true)
	m.rebuildPositions()
	m.handleBoardKey(keyPress("tab")) // from the Plan header to the Threads header
	m.handleBoardKey(keyPress("j"))   // "talk"
	m.handleBoardKey(keyPress("z"))   // zoom
	if !m.sectionCollapsed("Ideas") {
		t.Fatal("zoom should collapse Ideas")
	}
	m.handleBoardKey(keyPress("z")) // restore
	if m.focusFoldPrev != nil {
		t.Fatal("snapshot should be cleared after restore")
	}
	if !m.sectionCollapsed("Plan") {
		t.Fatal("restore must bring back the manual Plan fold")
	}
	if m.sectionCollapsed("Ideas") || m.sectionCollapsed("Threads") {
		t.Fatalf("restore must re-expand the zoom-collapsed sections: Ideas=%v Threads=%v",
			m.sectionCollapsed("Ideas"), m.sectionCollapsed("Threads"))
	}
	if it := m.currentItem(); it == nil || it.Text != "talk" {
		t.Fatalf("cursor should still sit on the anchor after restore, got %v", m.currentItem())
	}
}

func TestFocusFoldOnHeaderKeepsHeaderSelected(t *testing.T) {
	m := newTestModel(zoomSrc, 100, 40)
	// Cursor starts on the Plan header (position 0).
	m.handleBoardKey(keyPress("z"))
	if sec, ok := m.onHeader(); !ok || sec.Title != "Plan" {
		t.Fatalf("cursor should stay on the Plan header, onHeader=%v", ok)
	}
	if m.sectionCollapsed("Plan") || !m.sectionCollapsed("Threads") {
		t.Fatalf("Plan open, Threads folded expected: Plan=%v Threads=%v",
			m.sectionCollapsed("Plan"), m.sectionCollapsed("Threads"))
	}
}

// Regression guards for the FULL key dispatch path (Update -> handleKey ->
// handleBoardKey), not handleBoardKey directly: a mode-gating change (e.g. a
// sticky-search consume, an input-mode misclassification) could swallow the
// fold keys while every handler-level test stays green. Prompted by a live
// report of "collapse/expand and z do nothing" that turned out to be a stale
// binary — this pins the dispatch so a real recurrence is caught here.

func TestUpdateDispatchesFoldAndZoomKeys(t *testing.T) {
	m := newTestModel(zoomSrc, 100, 40)
	// Cursor on the Plan header. enter through Update() must collapse.
	m.Update(keyPress("enter"))
	if !m.sectionCollapsed("Plan") {
		t.Fatal("enter via Update should collapse the section")
	}
	m.Update(keyPress("enter"))
	if m.sectionCollapsed("Plan") {
		t.Fatal("second enter via Update should expand it again")
	}
	// z through Update() must zoom and toggle back.
	m.Update(keyPress("z"))
	if !m.sectionCollapsed("Threads") || !m.sectionCollapsed("Ideas") || m.sectionCollapsed("Plan") {
		t.Fatalf("z via Update should zoom onto Plan: Plan=%v Threads=%v Ideas=%v",
			m.sectionCollapsed("Plan"), m.sectionCollapsed("Threads"), m.sectionCollapsed("Ideas"))
	}
	m.Update(keyPress("z"))
	if m.sectionCollapsed("Threads") || m.sectionCollapsed("Ideas") {
		t.Fatal("second z via Update should restore the folds")
	}
}

// --- item-level folding (enter / l / h on items) ---

const foldSrc = "## Threads\n- [ ] parent\n  - [ ] child a\n    - claude: deep\n  - [ ] child b\n## Ideas\n- spark\n"

// enter on an item with children folds its subtree: the descendants stop being
// nav stops; a second enter unfolds them.
func TestEnterFoldsItemSubtree(t *testing.T) {
	m := newTestModel(foldSrc, 100, 40)
	m.handleBoardKey(keyPress("j")) // onto "parent"
	if it := m.currentItem(); it == nil || it.Text != "parent" {
		t.Fatalf("expected cursor on parent, got %v", m.currentItem())
	}
	m.handleBoardKey(keyPress("enter")) // fold parent
	if !m.itemFolded("Threads", m.currentItem()) {
		t.Fatal("enter should fold the item's subtree")
	}
	// The children must no longer be nav stops.
	for _, p := range m.positions {
		if p.item != nil && (p.item.Text == "child a" || p.item.Text == "child b" || p.item.Text == "claude: deep") {
			t.Fatalf("folded subtree item %q should not be a nav stop", p.item.Text)
		}
	}
	// Cursor stays on parent.
	if it := m.currentItem(); it == nil || it.Text != "parent" {
		t.Fatalf("cursor should stay on parent after folding, got %v", m.currentItem())
	}
	m.handleBoardKey(keyPress("enter")) // unfold
	if m.itemFolded("Threads", m.currentItem()) {
		t.Fatal("second enter should unfold the subtree")
	}
	found := false
	for _, p := range m.positions {
		if p.item != nil && p.item.Text == "child a" {
			found = true
		}
	}
	if !found {
		t.Fatal("unfolding should restore the child nav stops")
	}
}

// enter on a leaf item is a no-op (no children to fold).
func TestEnterOnLeafItemIsNoOp(t *testing.T) {
	m := newTestModel("## Plan\n- [ ] only\n", 100, 40)
	m.handleBoardKey(keyPress("j"))
	m.handleBoardKey(keyPress("enter"))
	if m.itemFolded("Plan", m.currentItem()) {
		t.Fatal("a leaf item cannot fold")
	}
}

// l on a folded item unfolds it in place (keeping the cursor); a second l then
// descends to the first child.
func TestLUnfoldsThenDescends(t *testing.T) {
	m := newTestModel(foldSrc, 100, 40)
	m.handleBoardKey(keyPress("j"))     // parent
	m.handleBoardKey(keyPress("enter")) // fold
	m.handleBoardKey(keyPress("l"))     // unfold in place
	if m.itemFolded("Threads", m.currentItem()) {
		t.Fatal("l should unfold a folded item")
	}
	if it := m.currentItem(); it == nil || it.Text != "parent" {
		t.Fatalf("l unfolding should keep the cursor on parent, got %v", m.currentItem())
	}
	m.handleBoardKey(keyPress("l")) // now descend
	if it := m.currentItem(); it == nil || it.Text != "child a" {
		t.Fatalf("second l should descend to the first child, got %v", m.currentItem())
	}
}

// h never folds an item — it ascends to the parent even when the item has an
// expanded subtree.
func TestHNeverFoldsItem(t *testing.T) {
	m := newTestModel(foldSrc, 100, 40)
	m.handleBoardKey(keyPress("j")) // parent
	m.handleBoardKey(keyPress("h")) // ascend to header, does not fold
	if m.itemFolded("Threads", m.board.Sections[0].Items[0]) {
		t.Fatal("h must not fold the item")
	}
	if _, ok := m.onHeader(); !ok {
		t.Fatal("h on a top-level item should land on the section header")
	}
}

// z item-level zoom: on an item, collapse other sections AND fold sibling
// subtrees, keeping the cursor's direct children visible-but-folded; a second z
// restores the exact prior item folds.
func TestFocusFoldItemLevelZoomAndRestore(t *testing.T) {
	src := "## Threads\n- [ ] parent\n  - [ ] child a\n    - claude: deep\n  - [ ] child b\n- [ ] sibling\n  - [ ] s1\n## Ideas\n- spark\n"
	m := newTestModel(src, 100, 40)
	m.handleBoardKey(keyPress("j")) // parent
	m.handleBoardKey(keyPress("j")) // child a
	if it := m.currentItem(); it == nil || it.Text != "child a" {
		t.Fatalf("expected cursor on child a, got %v", m.currentItem())
	}
	m.handleBoardKey(keyPress("z")) // zoom onto child a
	if !m.sectionCollapsed("Ideas") {
		t.Fatal("zoom should collapse other sections")
	}
	// "sibling" is a non-ancestor subtree in the focused section -> folded.
	var sibling *board.Item
	for _, it := range m.board.Sections[0].Items {
		if it.Text == "sibling" {
			sibling = it
		}
	}
	if sibling == nil || !m.itemFolded("Threads", sibling) {
		t.Fatal("zoom should fold non-ancestor sibling subtrees")
	}
	// child a stays open (it's the cursor); its child "deep" is visible.
	if it := m.currentItem(); it == nil || it.Text != "child a" || m.itemFolded("Threads", it) {
		t.Fatalf("cursor's item must stay open, got %v", m.currentItem())
	}
	m.handleBoardKey(keyPress("z")) // restore
	if m.focusFoldPrev != nil || m.focusFoldPrevItems != nil {
		t.Fatal("snapshots should be cleared after restore")
	}
	if m.sectionCollapsed("Ideas") || m.itemFolded("Threads", sibling) {
		t.Fatal("restore should undo the zoom's section + item folds")
	}
}

// A folded item renders a "▸ N" hidden-descendant tally.
func TestFoldedItemRendersHiddenCount(t *testing.T) {
	m := newTestModel(foldSrc, 100, 40)
	m.handleBoardKey(keyPress("j"))     // parent
	m.handleBoardKey(keyPress("enter")) // fold (3 hidden: child a, deep, child b)
	out := stripANSI(m.View())
	if !strings.Contains(out, "▸ 3") {
		t.Fatalf("folded item should show a '▸ 3' tally, got:\n%s", out)
	}
}

func TestUpdateDispatchesFoldKeysDuringStickySearch(t *testing.T) {
	m := newTestModel(zoomSrc, 100, 40)
	// Simulate a confirmed search (persistent results mode): fold keys must
	// still reach the board handler — only Esc consumes.
	m.searchActive = true
	m.searchQuery = "talk"
	m.Update(keyPress("enter"))
	if !m.sectionCollapsed("Plan") {
		t.Fatal("enter must fold the section while search results are sticky")
	}
	m.Update(keyPress("z"))
	if !m.sectionCollapsed("Threads") {
		t.Fatal("z must zoom while search results are sticky")
	}
	if !m.searchActive {
		t.Fatal("fold keys must not drop the sticky search")
	}
}
