package tui

import (
	"strings"
	"testing"
)

// fiveSectionBoard lays out (verified) as:
//
//	right side: Plan (s0, row 0), Ideas (s3, row 1)
//	left side:  Threads (s1, row -1), Questions (s2, row 0), Log (s4, row 1)
//
// so same-side vertical neighbors are: Plan↕Ideas on the right, and
// Threads↕Questions↕Log on the left. This is the layout behind the reported
// "k on Log surprises me" recording.
const fiveSectionBoard = "---\ntitle: T\n---\n" +
	"## Plan\n- [ ] a\n\n## Threads\n- [ ] b\n\n## Questions\n- [ ] c\n\n" +
	"## Ideas\n- [ ] d\n\n## Log\n- [ ] e\n"

func newFiveSection(t *testing.T) *model {
	t.Helper()
	m := newMapModel(t, fiveSectionBoard, 120, 40)
	m.mapShowLog = true
	m.mp = nil
	m.ensureMap()
	return m
}

// TestMapStructuralNav drives the tree-structural navigation through a real
// layout: up/down walk same-side siblings, left/right cross the parent/child
// axis, and every move is deterministic.
func TestMapStructuralNav(t *testing.T) {
	cases := []struct {
		name  string
		start string // starting focus key
		key   rune
		want  string // expected focus key after the move
	}{
		// The reported surprise: k on Log must land on the section visually
		// above it on the same (left) side — Questions, not across the map.
		{"k from Log lands on Questions above it", "s4", 'k', "s2"},
		{"k from Questions lands on Threads", "s2", 'k', "s1"},
		{"j from Questions lands on Log", "s2", 'j', "s4"},
		{"k at top of a side stays put", "s1", 'k', "s1"},
		{"j at bottom of a side stays put", "s4", 'j', "s4"},
		// Right side has its own independent vertical column.
		{"j from Plan lands on Ideas", "s0", 'j', "s3"},
		{"k from Ideas lands on Plan", "s3", 'k', "s0"},
		// Center enters each side; h/l cross sides sensibly via the center.
		{"l from center enters the first right section", "", 'l', "s0"},
		{"h from center enters the first left section", "", 'h', "s1"},
		{"h from a right section returns to the center", "s0", 'h', ""},
		{"l from a left section returns to the center", "s1", 'l', ""},
		// Away from the center descends into the branch's first child.
		{"l from a right section enters its first item", "s0", 'l', "s0/0"},
		{"h from a left section enters its first item", "s1", 'h', "s1/0"},
		// An item crosses back toward the center via its parent section.
		{"h from a right item returns to its section", "s0/0", 'h', "s0"},
		{"l from a left item returns to its section", "s1/0", 'l', "s1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := newFiveSection(t)
			focusMapKey(t, m, c.start)
			m.mapMove(c.key)
			if got := m.mapFocusKey; got != c.want {
				t.Errorf("%c from %q: focus = %q, want %q", c.key, c.start, got, c.want)
			}
		})
	}
}

// focusMapKey points map focus at the node with the given stable key.
func focusMapKey(t *testing.T, m *model, key string) {
	t.Helper()
	for n, k := range m.mp.keys {
		if k == key {
			m.mp.focus = n
			m.mapFocusKey = key
			return
		}
	}
	t.Fatalf("no map node with key %q", key)
}

// TestMapCenterVertical checks j/k off the center step to the nearest first-ring
// section in that vertical direction (deterministic).
func TestMapCenterVertical(t *testing.T) {
	m := newFiveSection(t)
	focusMapKey(t, m, "")                  // center
	m.mapMove('k')                         // up: nearest section above the center row
	if got := m.mapFocusKey; got != "s1" { // Threads at row -1
		t.Errorf("k from center = %q, want s1 (Threads)", got)
	}
	focusMapKey(t, m, "")
	m.mapMove('j') // down: nearest section below the center row
	// Ties at row 1 (Ideas s3 right, Log s4 left) resolve to document order.
	if got := m.mapFocusKey; got != "s3" {
		t.Errorf("j from center = %q, want s3 (Ideas)", got)
	}
}

// TestMapExpandRoundTrip asserts `w` expands the focused node into a multi-line
// block and `w` again collapses it back to a single truncated line.
func TestMapExpandRoundTrip(t *testing.T) {
	long := "this is a deliberately long item that exceeds forty display columns and should wrap"
	src := "## Threads\n- [ ] " + long + "\n"
	m := newMapModel(t, src, 120, 40)
	focusMapItem(t, m, long)

	// Collapsed by default: one truncated row, width within the cap.
	p := m.mp.placement(m.mp.focus)
	if p.Height != 1 {
		t.Fatalf("default height = %d, want 1", p.Height)
	}
	if p.Width > mapNodeMaxWidth {
		t.Errorf("truncated width %d exceeds cap %d", p.Width, mapNodeMaxWidth)
	}

	m.handleMapKey(keyPress("w")) // expand
	m.ensureMap()
	focusMapItem(t, m, long)
	pe := m.mp.placement(m.mp.focus)
	if pe.Height < 2 {
		t.Fatalf("expanded height = %d, want >= 2", pe.Height)
	}
	for _, l := range pe.Lines {
		if got := len([]rune(l)); got > mapNodeMaxWidth {
			t.Errorf("wrapped line %q exceeds cap", l)
		}
	}

	m.handleMapKey(keyPress("w")) // collapse
	m.ensureMap()
	focusMapItem(t, m, long)
	pc := m.mp.placement(m.mp.focus)
	if pc.Height != 1 {
		t.Errorf("collapsed height = %d, want 1 (round-trip failed)", pc.Height)
	}
}

// TestMapFooterShowsFullText asserts the focused node's FULL (untruncated) text
// stays visible in the detail footer even though the map body truncates it.
func TestMapFooterShowsFullText(t *testing.T) {
	long := "an item long enough to be truncated on the map body but shown in full below"
	src := "## Threads\n- [ ] " + long + "\n"
	m := newMapModel(t, src, 120, 40)
	focusMapItem(t, m, long)

	out := stripANSI(m.viewMap())
	if !strings.Contains(out, long) {
		t.Errorf("footer detail is missing the full node text:\n%s", out)
	}
}
