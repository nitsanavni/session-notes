package tui

import "testing"

// childMemBoard: two right/left sections each with several items, so descent
// memory (which child we came from) is observable.
//
//	## Plan     (s0)  items a,b,c
//	## Threads  (s1)  items x,y,z
const childMemBoard = "---\ntitle: T\n---\n" +
	"## Plan\n- [ ] a\n- [ ] b\n- [ ] c\n\n" +
	"## Threads\n- [ ] x\n- [ ] y\n- [ ] z\n"

func newChildMem(t *testing.T) *model {
	t.Helper()
	m := newMapModel(t, childMemBoard, 120, 40)
	m.mp = nil
	m.ensureMap()
	return m
}

// sideKeys returns the (outward, inward) nav runes for the side the node with
// the given key sits on: right side descends with 'l', left with 'h'.
func sideKeys(t *testing.T, m *model, key string) (out, in rune) {
	t.Helper()
	n, ok := m.mp.nodeByKey[key]
	if !ok {
		t.Fatalf("no node for key %q", key)
	}
	if m.mp.sideOf(n) == 1 {
		return 'l', 'h'
	}
	return 'h', 'l'
}

// TestMapDescendRemembersChild: after ascending from a non-first child to its
// parent, descending back returns to that child, not the first one.
func TestMapDescendRemembersChild(t *testing.T) {
	m := newChildMem(t)
	out, in := sideKeys(t, m, "s0")
	focusMapKey(t, m, "s0/1") // Plan > b
	m.mapMove(in)             // up to Plan (records child s0/1)
	if m.mapFocusKey != "s0" {
		t.Fatalf("inward: focus = %q, want s0", m.mapFocusKey)
	}
	m.mapMove(out) // back down into Plan
	if m.mapFocusKey != "s0/1" {
		t.Errorf("descend: focus = %q, want s0/1 (remembered), not first child", m.mapFocusKey)
	}
}

// TestMapDescendFallsBackWhenChildGone: a remembered child that no longer maps
// falls back to the first child.
func TestMapDescendFallsBackWhenChildGone(t *testing.T) {
	m := newChildMem(t)
	out, _ := sideKeys(t, m, "s0")
	m.rememberChild("s0", m.mp.sideOf(m.mp.nodeByKey["s0"]), "s0/9") // stale
	focusMapKey(t, m, "s0")
	m.mapMove(out)
	if m.mapFocusKey != "s0/0" {
		t.Errorf("descend with stale memory: focus = %q, want s0/0 (first child)", m.mapFocusKey)
	}
}

// TestMapChildMemPerParent: each parent remembers its own child independently.
func TestMapChildMemPerParent(t *testing.T) {
	m := newChildMem(t)
	// Remember b under Plan.
	out0, in0 := sideKeys(t, m, "s0")
	focusMapKey(t, m, "s0/2") // Plan > c
	m.mapMove(in0)            // -> Plan (records s0/2)
	// Remember y under Threads.
	out1, in1 := sideKeys(t, m, "s1")
	focusMapKey(t, m, "s1/1") // Threads > y
	m.mapMove(in1)            // -> Threads (records s1/1)

	focusMapKey(t, m, "s0")
	m.mapMove(out0)
	if m.mapFocusKey != "s0/2" {
		t.Errorf("Plan descend = %q, want s0/2", m.mapFocusKey)
	}
	focusMapKey(t, m, "s1")
	m.mapMove(out1)
	if m.mapFocusKey != "s1/1" {
		t.Errorf("Threads descend = %q, want s1/1", m.mapFocusKey)
	}
}

// TestMapCenterRemembersPerSide: the center remembers the last section entered
// on each side independently (mm's two-sided center).
func TestMapCenterRemembersPerSide(t *testing.T) {
	m := newChildMem(t)
	// s0 and s1 are on opposite sides (one right, one left).
	if m.mp.sideOf(m.mp.nodeByKey["s0"]) == m.mp.sideOf(m.mp.nodeByKey["s1"]) {
		t.Skip("layout put both sections on the same side")
	}
	// Descend to a section on each side, then ascend to center to record it.
	focusMapKey(t, m, "s0")
	m.mapMove('h') // whichever way is inward lands on center for one side
	// Re-seed deterministically: go center, into each side, back to center.
	focusMapKey(t, m, "s0")
	sideS0 := m.mp.sideOf(m.mp.nodeByKey["s0"])
	inward0 := 'h'
	if sideS0 == -1 {
		inward0 = 'l'
	}
	m.mapMove(inward0) // s0 -> center (records center[sideS0] = s0)
	if m.mapFocusKey != "" {
		t.Fatalf("expected center, got %q", m.mapFocusKey)
	}
	// Enter that side again from center; should land back on s0.
	outward0 := 'l'
	if sideS0 == -1 {
		outward0 = 'h'
	}
	m.mapMove(outward0)
	if m.mapFocusKey != "s0" {
		t.Errorf("center descend to side = %q, want s0", m.mapFocusKey)
	}
}
