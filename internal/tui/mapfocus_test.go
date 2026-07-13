package tui

import (
	"strings"
	"testing"

	"github.com/nitsanavni/session-notes/internal/board"
	"github.com/nitsanavni/session-notes/internal/mindmap"
)

// placementForText returns the placement of the map item whose board text ==
// want (searching the current layout).
func placementForText(t *testing.T, m *model, want string) *mindmap.Placement {
	t.Helper()
	for n, ref := range m.mp.refs {
		if ref.kind == refItem && ref.item.Text == want {
			if p := m.mp.placement(n); p != nil {
				return p
			}
		}
	}
	t.Fatalf("no placed map item with text %q", want)
	return nil
}

// TestMapFocusIn re-roots the map on the focused subtree: the focused node
// becomes the center and focus lands on its first child, with a breadcrumb.
func TestMapFocusIn(t *testing.T) {
	src := "## Threads\n- [ ] parent\n  - [ ] child one\n  - [ ] child two\n"
	m := newMapModel(t, src, 120, 40)
	focusMapItem(t, m, "parent")
	parentKey := m.mapFocusKey

	m.handleMapKey(keyPress("f"))
	m.ensureMap()

	if m.mapFocusRoot != parentKey {
		t.Fatalf("focus root = %q, want %q", m.mapFocusRoot, parentKey)
	}
	// The center of the re-rooted view is the parent node.
	if got := m.mp.refs[m.mp.root]; got.kind != refItem || got.item.Text != "parent" {
		t.Fatalf("re-rooted center is not parent: %+v", got)
	}
	// Focus lands on the first child.
	if got := m.mp.refs[m.mp.focus]; got.kind != refItem || got.item.Text != "child one" {
		t.Errorf("focus after f = %+v, want child one", got)
	}
	// The board root (Threads/title) is no longer placed in the view.
	for _, ref := range m.mp.refs {
		if ref.kind == refSection {
			if p := placementByRefSection(m, ref.section); p != nil {
				t.Errorf("section %q still placed after focus-in", ref.section)
			}
		}
	}
	// Breadcrumb names the path back and includes the focus root's label.
	crumbs := m.mapCrumbs()
	if len(crumbs) == 0 || !strings.Contains(strings.Join(crumbs, " › "), "parent") {
		t.Errorf("breadcrumb %v missing focus root", crumbs)
	}
}

func placementByRefSection(m *model, title string) *mindmap.Placement {
	for n, ref := range m.mp.refs {
		if ref.kind == refSection && ref.section == title {
			return m.mp.placement(n)
		}
	}
	return nil
}

// TestMapFocusOut steps focus out one level, resting on the node stepped out of,
// and eventually returns to the whole board.
func TestMapFocusOut(t *testing.T) {
	src := "## Threads\n- [ ] parent\n  - [ ] child\n"
	m := newMapModel(t, src, 120, 40)
	focusMapItem(t, m, "parent")
	parentKey := m.mapFocusKey

	m.handleMapKey(keyPress("f")) // focus into parent
	m.ensureMap()
	if m.mapFocusRoot != parentKey {
		t.Fatalf("focus root = %q, want %q", m.mapFocusRoot, parentKey)
	}

	// esc steps out one level (b now toggles blocked — web convention 9729cad).
	m.handleMapKey(keyPress("esc")) // step out one level -> the Threads section
	m.ensureMap()
	if m.mapFocusRoot != parentKeyOf(parentKey) {
		t.Fatalf("focus root after esc = %q, want %q (one level out)", m.mapFocusRoot, parentKeyOf(parentKey))
	}
	// Cursor rests on the node we stepped out of (parent).
	if got := m.mp.refs[m.mp.focus]; got.kind != refItem || got.item.Text != "parent" {
		t.Errorf("focus after esc = %+v, want parent", got)
	}

	m.handleMapKey(keyPress("esc")) // out again -> whole board
	m.ensureMap()
	if m.mapFocusRoot != "" {
		t.Fatalf("focus root after 2nd esc = %q, want whole board", m.mapFocusRoot)
	}
	// b at the top level toggles blocked, never re-roots.
	m.handleMapKey(keyPress("b"))
	if m.mapFocusRoot != "" {
		t.Errorf("focus root should stay empty at top level")
	}
}

// TestMapFocusEscStepsOut asserts esc focuses out one level (rather than
// leaving the map) while focused, and folds persist across re-rooting.
func TestMapFocusEscStepsOut(t *testing.T) {
	src := "## Threads\n- [ ] parent\n  - [ ] child\n"
	m := newMapModel(t, src, 120, 40)
	focusMapItem(t, m, "parent")

	m.handleMapKey(keyPress("f"))
	m.ensureMap()
	if m.mapFocusRoot == "" {
		t.Fatal("expected to be focused")
	}
	before := m.mapFocusRoot
	if _, cmd := m.handleMapKey(keyPress("esc")); cmd != nil {
		t.Fatalf("esc while focused should not quit (cmd=%v)", cmd)
	}
	if m.mapFocusRoot == before {
		t.Errorf("esc did not step focus out (root still %q)", m.mapFocusRoot)
	}
}

// TestMapFocusCollapsedRootRevealsChildren asserts focusing into a collapsed
// node un-collapses it so its children show (matching mm).
func TestMapFocusCollapsedRootRevealsChildren(t *testing.T) {
	src := "## Threads\n- [ ] parent\n  - [ ] child\n"
	m := newMapModel(t, src, 120, 40)
	focusMapItem(t, m, "parent")
	key := m.mapFocusKey
	m.mapFold = map[string]foldState{key: foldCollapsed}
	m.mp = nil
	m.ensureMap()
	focusMapItem(t, m, "parent")

	m.handleMapKey(keyPress("f"))
	m.ensureMap()
	if m.mapFold[key] == foldCollapsed {
		t.Errorf("focus root stayed collapsed")
	}
	if placementForText(t, m, "child") == nil {
		t.Errorf("child not visible under focused (un-collapsed) root")
	}
}

// TestMapFocusRecorded asserts f/b are in the surprise recorder's key set and
// that focusRoot is captured.
func TestMapFocusRecorded(t *testing.T) {
	if !recordedMapKeys["f"] || !recordedMapKeys["b"] {
		t.Fatal("f/b not in recordedMapKeys")
	}
	src := "## Threads\n- [ ] parent\n  - [ ] child\n"
	m := newMapModel(t, src, 120, 40)
	focusMapItem(t, m, "parent")
	m.handleMapKey(keyPress("f"))
	m.ensureMap()
	st := m.captureMapState()
	if st.FocusRoot == "" {
		t.Errorf("captured state missing focusRoot")
	}
	// A recorded focus event is buffered.
	if len(m.feedbackEvents) == 0 || m.feedbackEvents[len(m.feedbackEvents)-1].Key != "f" {
		t.Errorf("f was not recorded as a feedback event")
	}
}

// TestMapFocusSurvivesEdit verifies the zoom root is keyed by node id (M2): after
// an edit that shifts positional keys (inserting a sibling above the focused
// subtree), the map stays re-rooted on the same node.
func TestMapFocusSurvivesEdit(t *testing.T) {
	src := "## Threads\n- [ ] alpha\n  - [ ] a-child\n- [ ] beta\n  - [ ] b-child\n"
	m := newMapModel(t, src, 120, 40)
	m.board.EnsureIDs()
	m.mp = nil
	m.ensureMap()

	focusMapItem(t, m, "beta")
	m.handleMapKey(keyPress("f"))
	m.ensureMap()
	if got := m.mp.refs[m.mp.root]; got.kind != refItem || got.item.Text != "beta" {
		t.Fatalf("re-rooted center is not beta: %+v", got)
	}
	betaID := m.mapFocusRootID
	if betaID == "" {
		t.Fatal("focus root id was not recorded")
	}

	// Insert a new item at the top of the section: every positional key below
	// shifts, but the durable id must keep the zoom on beta.
	sec := m.board.Section("Threads")
	newItem := m.board.AddItem("Threads", "zzz-new-top")
	// Move it to the front so keys below it shift.
	sec.Items = append([]*board.Item{newItem}, sec.Items[:len(sec.Items)-1]...)
	m.board.EnsureIDs()
	m.rebuildPositions()
	m.mp = nil
	m.ensureMap()

	if m.mapFocusRootID != betaID {
		t.Errorf("focus root id changed across edit: %q -> %q", betaID, m.mapFocusRootID)
	}
	if got := m.mp.refs[m.mp.root]; got.kind != refItem || got.item.Text != "beta" {
		t.Errorf("zoom did not survive edit; center is %+v, want beta", got)
	}
}
