package tui

import (
	"testing"
)

// z (focus-fold / zoom) in the map view: collapse every subtree except the
// ancestor path down to the focused node; the focus's direct children stay
// visible but each collapsed (one level open); a second z restores the exact
// pre-zoom fold snapshot. Mirrors the outline's z and the web map's z.

const mapZoomSrc = "## Plan\n- [ ] extract middleware\n  - [ ] sub plan\n" +
	"## Threads\n- [ ] auth refactor\n  - [ ] deep child\n    - claude: deepest\n- [ ] other topic\n  - [ ] other child\n" +
	"## Ideas\n- spark\n"

func TestMapFocusFoldCollapsesToAncestorPath(t *testing.T) {
	m := newMapModel(t, mapZoomSrc, 140, 40)
	focusMapItem(t, m, "deep child") // s1/0/0 — nested under "auth refactor"
	m.mapFocusFoldToggle()

	// Ancestor path (Threads, auth refactor, deep child) opens fully.
	for _, key := range []string{"s1", "s1/0", "s1/0/0"} {
		if m.mapFold[key] != foldRepliesExpanded {
			t.Errorf("ancestor %s should be fully open, got %v", key, m.mapFold[key])
		}
	}
	// Non-ancestor subtrees collapse: Plan section, other topic.
	for _, key := range []string{"s0", "s1/1"} {
		if m.mapFold[key] != foldCollapsed {
			t.Errorf("non-ancestor %s should collapse, got %v", key, m.mapFold[key])
		}
	}
	// The focus's own child subtree ("deepest" is a reply, so s1/0/0's replies
	// show via the ancestor state; deeper foldable nodes off the path collapse).
	// Focus stays put and visible.
	if ref := m.mp.refs[m.mp.focus]; ref.kind != refItem || ref.item.Text != "deep child" {
		t.Fatalf("focus should stay on the zoom anchor, got %+v", m.mp.refs[m.mp.focus])
	}
	// Hidden branch: "sub plan" (inside the collapsed Plan section) builds no node.
	if _, ok := findMapNodeByText(m, "sub plan"); ok {
		t.Error("a collapsed branch's child must not render")
	}
	// Visible: the whole ancestor path.
	if _, ok := findMapNodeByText(m, "deep child"); !ok {
		t.Error("the zoom anchor must render")
	}
}

func TestMapFocusFoldSecondZRestoresSnapshot(t *testing.T) {
	m := newMapModel(t, mapZoomSrc, 140, 40)
	// Pre-existing manual fold: collapse "other topic" (a non-default state).
	m.mapFold = map[string]foldState{"s1/1": foldCollapsed}
	m.mp = nil
	m.ensureMap()
	focusMapItem(t, m, "auth refactor")
	m.mapFocusFoldToggle() // zoom
	if m.mapFoldPrev == nil {
		t.Fatal("zoom should snapshot the prior folds")
	}
	if m.mapFold["s0"] != foldCollapsed {
		t.Fatal("zoom should collapse the Plan branch")
	}
	m.mapFocusFoldToggle() // restore
	if m.mapFoldPrev != nil {
		t.Fatal("snapshot should be cleared after restore")
	}
	if len(m.mapFold) != 1 || m.mapFold["s1/1"] != foldCollapsed {
		t.Fatalf("restore must bring back the exact prior folds, got %v", m.mapFold)
	}
}

func TestMapZoomOneLevelOpenChildrenCollapsed(t *testing.T) {
	m := newMapModel(t, mapZoomSrc, 140, 40)
	focusMapItem(t, m, "auth refactor") // s1/0; child "deep child" has its own child
	m.mapFocusFoldToggle()
	// Direct child of the focus is a non-ancestor with children -> collapsed:
	// visible itself, its own subtree hidden.
	if m.mapFold["s1/0/0"] != foldCollapsed {
		t.Errorf("the focus's child subtree should be collapsed, got %v", m.mapFold["s1/0/0"])
	}
	if _, ok := findMapNodeByText(m, "deep child"); !ok {
		t.Error("the focus's direct child must stay visible")
	}
	if _, ok := findMapNodeByText(m, "claude: deepest"); ok {
		t.Error("the grandchild must be hidden behind the child's fold")
	}
}

// z must dispatch through the map key handler (and V must open recents there).
func TestMapKeyDispatchesZoomAndRecents(t *testing.T) {
	m := newMapModel(t, mapZoomSrc, 140, 40)
	focusMapItem(t, m, "auth refactor")
	m.handleMapKey(keyPress("z"))
	if m.mapFoldPrev == nil || m.mapFold["s0"] != foldCollapsed {
		t.Fatal("z via handleMapKey should zoom")
	}
	m.handleMapKey(keyPress("z"))
	if m.mapFoldPrev != nil {
		t.Fatal("second z via handleMapKey should restore")
	}
	m.handleMapKey(keyPress("V"))
	if m.mode != modeRecents {
		t.Fatal("V in map mode should open the recents overlay")
	}
}

// Enter in the recents overlay opened from the map jumps map focus onto the
// changed node (jumpToMatch is map-aware) and clears its fresh mark.
func TestMapRecentsEnterJumpsInMap(t *testing.T) {
	m := newMapModel(t, mapZoomSrc, 140, 40)
	key := "- [ ] other topic"
	m.fresh[key] = true
	m.recents = []recentEntry{recentEntryFor(key)}
	m.handleMapKey(keyPress("V"))
	m.handleRecentsKey(keyPress("enter"))
	if m.mode == modeRecents {
		t.Fatal("enter should close the overlay")
	}
	if m.fresh[key] {
		t.Error("enter should clear the fresh mark")
	}
	if ref := m.mp.refs[m.mp.focus]; ref.kind != refItem || ref.item.Text != "other topic" {
		t.Fatalf("map focus should land on the changed node, got %+v", ref)
	}
}

// findMapNodeByText reports whether any rendered map node carries the text.
func findMapNodeByText(m *model, text string) (string, bool) {
	for n, ref := range m.mp.refs {
		if ref.kind == refItem && ref.item.Text == text {
			return m.mp.keys[n], true
		}
	}
	return "", false
}
