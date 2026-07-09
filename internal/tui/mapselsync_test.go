package tui

import (
	"testing"

	"github.com/nitsanavni/session-notes/internal/board"
)

// cursorText is the outline cursor's item text, or "" when off an item.
func cursorText(m *model) string {
	if it := m.currentItem(); it != nil {
		return it.Text
	}
	return ""
}

// TestEnterMapSeedsFocusFromCursor asserts that entering the map focuses the
// same item the outline cursor was on (outline -> map selection carry-over).
func TestEnterMapSeedsFocusFromCursor(t *testing.T) {
	src := "## Threads\n- one\n- two\n- three\n"
	m := newTestModel(src, 120, 40)
	// Put the outline cursor on "two".
	for i, p := range m.positions {
		if p.item != nil && p.item.Text == "two" {
			m.cursor = i
		}
	}
	m.enterMap()
	ref := m.mp.refs[m.mp.focus]
	if ref.kind != refItem || ref.item.Text != "two" {
		t.Fatalf("map focus did not carry over cursor: got %+v", ref)
	}
}

// TestExitMapSyncsCursor asserts that leaving the map moves the outline cursor
// onto the map's focused item (map -> outline selection carry-over).
func TestExitMapSyncsCursor(t *testing.T) {
	src := "## Threads\n- one\n- two\n- three\n"
	m := newMapModel(t, src, 120, 40)
	focusMapItem(t, m, "three")
	m.exitMap()
	if m.mapView {
		t.Fatal("exitMap should leave the map view")
	}
	if got := cursorText(m); got != "three" {
		t.Fatalf("outline cursor did not sync from map focus: got %q, want %q", got, "three")
	}
}

// TestSelectionRoundTrip asserts a full outline-index -> map-ref -> outline-index
// round trip lands back on the same item, across a nested item.
func TestSelectionRoundTrip(t *testing.T) {
	src := "## Threads\n- parent\n  - child a\n  - child b\n## Ideas\n- lone\n"
	m := newTestModel(src, 120, 40)
	for _, want := range []string{"parent", "child a", "child b", "lone"} {
		start := -1
		for i, p := range m.positions {
			if p.item != nil && p.item.Text == want {
				start = i
			}
		}
		if start < 0 {
			t.Fatalf("item %q not found in outline positions", want)
		}
		m.cursor = start
		m.enterMap()
		ref := m.mp.refs[m.mp.focus]
		if ref.kind != refItem || ref.item.Text != want {
			t.Fatalf("map focus mismatch for %q: got %+v", want, ref)
		}
		m.exitMap()
		if m.cursor != start {
			t.Fatalf("round trip for %q: cursor %d != start %d (%q)", want, m.cursor, start, cursorText(m))
		}
	}
}

// TestEnterMapRevealsCollapsedItem asserts that seeding focus on an item hidden
// behind a collapsed ancestor un-collapses the ancestor so focus can land on it.
func TestEnterMapRevealsCollapsedItem(t *testing.T) {
	src := "## Threads\n- parent\n  - child a\n  - child b\n"
	m := newMapModel(t, src, 120, 40)
	// Collapse "parent" in the map so its children are hidden.
	pk, ok := m.itemMapKey(findItem(t, m, "parent"))
	if !ok {
		t.Fatal("no key for parent")
	}
	m.mapFold = map[string]foldState{pk: foldCollapsed}
	m.mp = nil
	m.ensureMap()
	// Now go to outline, put cursor on the hidden child, and re-enter the map.
	m.exitMap()
	for i, p := range m.positions {
		if p.item != nil && p.item.Text == "child b" {
			m.cursor = i
		}
	}
	m.enterMap()
	ref := m.mp.refs[m.mp.focus]
	if ref.kind != refItem || ref.item.Text != "child b" {
		t.Fatalf("collapsed child not revealed on enter: focus %+v", ref)
	}
	if m.mapFold[pk] == foldCollapsed {
		t.Fatal("parent should have been un-collapsed to reveal the child")
	}
}

// TestEnterMapRevealsReply asserts a seeded reply item expands its parent's
// reply thread so focus lands on it.
func TestEnterMapRevealsReply(t *testing.T) {
	src := "## Threads\n- parent\n  - user: a question\n  - claude: an answer\n"
	m := newTestModel(src, 120, 40)
	for i, p := range m.positions {
		if p.item != nil && p.item.Text == "claude: an answer" {
			m.cursor = i
		}
	}
	m.enterMap()
	ref := m.mp.refs[m.mp.focus]
	if ref.kind != refItem || ref.item.Text != "claude: an answer" {
		t.Fatalf("reply not revealed on enter: focus %+v", ref)
	}
}

// TestEnterMapDropsStaleFocusRoot asserts a persisted focus root that no longer
// contains the seeded node is cleared so the seeded item stays visible.
func TestEnterMapDropsStaleFocusRoot(t *testing.T) {
	src := "## Threads\n- parent\n  - child a\n## Ideas\n- lone\n"
	m := newMapModel(t, src, 120, 40)
	// Focus into "parent" so the map is re-rooted on its subtree.
	pk, ok := m.itemMapKey(findItem(t, m, "parent"))
	if !ok {
		t.Fatal("no key for parent")
	}
	m.mapFocusRoot = pk
	m.mp = nil
	m.ensureMap()
	// Cursor to "lone" (a different subtree) and re-enter.
	m.exitMap()
	for i, p := range m.positions {
		if p.item != nil && p.item.Text == "lone" {
			m.cursor = i
		}
	}
	m.enterMap()
	if m.mapFocusRoot != "" {
		t.Fatalf("stale focus root not dropped: %q", m.mapFocusRoot)
	}
	ref := m.mp.refs[m.mp.focus]
	if ref.kind != refItem || ref.item.Text != "lone" {
		t.Fatalf("focus did not land on the seeded node: %+v", ref)
	}
}

// findItem returns the first board item whose text equals want.
func findItem(t *testing.T, m *model, want string) *board.Item {
	t.Helper()
	for _, p := range m.positions {
		if p.item != nil && p.item.Text == want {
			return p.item
		}
	}
	t.Fatalf("item %q not found", want)
	return nil
}
