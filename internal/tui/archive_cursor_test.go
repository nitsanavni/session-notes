package tui

import (
	"path/filepath"
	"testing"
)

// Archiving (d) and deleting (D) keep the selection close to where it was:
// the cursor lands on the next item in document order (the one that visually
// takes the removed item's place); the last item in a section falls back to
// the previous item; only an emptied section drops the cursor to its header.

const archiveCursorSrc = "## Plan\n- [ ] first\n- [ ] second\n  - [ ] second child\n- [ ] third\n\n## Threads\n- [ ] only\n"

// newRemovalModel builds an outline model over archiveCursorSrc with a fresh
// per-test board path, so the shared newTestModel temp file's stale content
// can't trigger a save-time rebase that would replace the tree under the test.
func newRemovalModel(t *testing.T) *model {
	t.Helper()
	m := newTestModel(archiveCursorSrc, 100, 40)
	m.path = filepath.Join(t.TempDir(), "board.md")
	m.board.Path = m.path
	return m
}

func removalCursorText(t *testing.T, m *model) string {
	t.Helper()
	if it := m.currentItem(); it != nil {
		return it.Text
	}
	if sec, ok := m.onHeader(); ok {
		return "## " + sec.Title
	}
	return "<none>"
}

func TestArchiveMidSectionSelectsNextSibling(t *testing.T) {
	m := newRemovalModel(t)
	focus(t, m, "second")
	m.handleBoardKey(keyPress("d")) // archives second + its child subtree
	if got := removalCursorText(t, m); got != "third" {
		t.Fatalf("cursor after mid-section archive = %q, want %q", got, "third")
	}
}

func TestArchiveLastItemSelectsPrevious(t *testing.T) {
	m := newRemovalModel(t)
	focus(t, m, "third")
	m.handleBoardKey(keyPress("d"))
	// The subtree under "second" precedes "third"; the previous stop is its child.
	if got := removalCursorText(t, m); got != "second child" {
		t.Fatalf("cursor after last-item archive = %q, want %q", got, "second child")
	}
}

func TestArchiveOnlyItemFallsBackToSectionHead(t *testing.T) {
	m := newRemovalModel(t)
	focus(t, m, "only")
	m.handleBoardKey(keyPress("d"))
	if got := removalCursorText(t, m); got != "## Threads" {
		t.Fatalf("cursor after only-item archive = %q, want %q", got, "## Threads")
	}
}

func TestArchiveSkipsRemovedSubtree(t *testing.T) {
	// Archiving a parent must not try to land on its own (also removed) child.
	m := newRemovalModel(t)
	focus(t, m, "first")
	m.handleBoardKey(keyPress("d"))
	if got := removalCursorText(t, m); got != "second" {
		t.Fatalf("cursor after archive = %q, want %q", got, "second")
	}
}

func TestDeleteMidSectionSelectsNextSibling(t *testing.T) {
	m := newRemovalModel(t)
	focus(t, m, "second")
	m.handleBoardKey(keyPress("D")) // deletes second + subtree
	if got := removalCursorText(t, m); got != "third" {
		t.Fatalf("cursor after mid-section delete = %q, want %q", got, "third")
	}
}

func TestDeleteLastItemSelectsPrevious(t *testing.T) {
	m := newRemovalModel(t)
	focus(t, m, "third")
	m.handleBoardKey(keyPress("D"))
	if got := removalCursorText(t, m); got != "second child" {
		t.Fatalf("cursor after last-item delete = %q, want %q", got, "second child")
	}
}

func TestDeleteOnlyItemFallsBackToSectionHead(t *testing.T) {
	m := newRemovalModel(t)
	focus(t, m, "only")
	m.handleBoardKey(keyPress("D"))
	if got := removalCursorText(t, m); got != "## Threads" {
		t.Fatalf("cursor after only-item delete = %q, want %q", got, "## Threads")
	}
}

// ---- map view: d/D keep focus on a nearby node ----

func mapFocusText(m *model) string {
	ref := m.mp.refs[m.mp.focus]
	switch ref.kind {
	case refItem:
		return ref.item.Text
	case refSection:
		return "## " + ref.section
	}
	return "<center>"
}

func TestMapArchiveSelectsNextSibling(t *testing.T) {
	m := newMapModel(t, archiveCursorSrc, 120, 40)
	focusMapItem(t, m, "second")
	m.handleMapKey(keyPress("d"))
	m.ensureMap()
	if got := mapFocusText(m); got != "third" {
		t.Fatalf("map focus after mid archive = %q, want %q", got, "third")
	}
}

func TestMapArchiveLastSelectsPrevious(t *testing.T) {
	m := newMapModel(t, archiveCursorSrc, 120, 40)
	focusMapItem(t, m, "third")
	m.handleMapKey(keyPress("d"))
	m.ensureMap()
	// "third"'s previous top-level sibling node is "second".
	if got := mapFocusText(m); got != "second" {
		t.Fatalf("map focus after last archive = %q, want %q", got, "second")
	}
}

func TestMapArchiveOnlyChildFallsBackToParent(t *testing.T) {
	m := newMapModel(t, archiveCursorSrc, 120, 40)
	focusMapItem(t, m, "only")
	m.handleMapKey(keyPress("d"))
	m.ensureMap()
	if got := mapFocusText(m); got != "## Threads" {
		t.Fatalf("map focus after only-child archive = %q, want %q", got, "## Threads")
	}
}

func TestMapDeleteSelectsNextSibling(t *testing.T) {
	m := newMapModel(t, archiveCursorSrc, 120, 40)
	focusMapItem(t, m, "first")
	m.handleMapKey(keyPress("D"))
	m.ensureMap()
	if got := mapFocusText(m); got != "second" {
		t.Fatalf("map focus after delete = %q, want %q", got, "second")
	}
}

// A board where every top-level sibling owns children, so the previous/next
// sibling of a removed node is a subtree parent (its own last descendant is a
// leaf several levels down). The map must land on the sibling NODE, never
// descend into that sibling's leaf — the reported "sent to the leaf of my
// sibling instead of my sibling" surprise.
const siblingWithKidsSrc = "## Plan\n- [ ] alpha\n  - [ ] a1\n  - [ ] a2\n- [ ] beta\n  - [ ] b1\n    - [ ] b1a\n  - [ ] b2\n- [ ] gamma\n  - [ ] g1\n"

func TestMapArchiveLastLandsOnSiblingNodeNotItsLeaf(t *testing.T) {
	// gamma is the last top-level item; its previous sibling beta has a deep
	// subtree ending in b1a. Archiving gamma must focus beta, not b1a.
	m := newMapModel(t, siblingWithKidsSrc, 120, 40)
	focusMapItem(t, m, "gamma")
	m.handleMapKey(keyPress("d"))
	m.ensureMap()
	if got := mapFocusText(m); got != "beta" {
		t.Fatalf("map focus after archiving last sibling = %q, want %q (leaf-descent bug)", got, "beta")
	}
}

func TestMapDeleteLastLandsOnSiblingNodeNotItsLeaf(t *testing.T) {
	m := newMapModel(t, siblingWithKidsSrc, 120, 40)
	focusMapItem(t, m, "gamma")
	m.handleMapKey(keyPress("D"))
	m.ensureMap()
	if got := mapFocusText(m); got != "beta" {
		t.Fatalf("map focus after deleting last sibling = %q, want %q (leaf-descent bug)", got, "beta")
	}
}

func TestMapArchiveMidLandsOnNextSiblingNodeNotItsLeaf(t *testing.T) {
	// Archiving alpha (subtree) lands on the next sibling beta — its node, not
	// beta's first child b1.
	m := newMapModel(t, siblingWithKidsSrc, 120, 40)
	focusMapItem(t, m, "alpha")
	m.handleMapKey(keyPress("d"))
	m.ensureMap()
	if got := mapFocusText(m); got != "beta" {
		t.Fatalf("map focus after archiving mid sibling = %q, want %q", got, "beta")
	}
}
