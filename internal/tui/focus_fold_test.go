package tui

import "testing"

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
