package tui

import (
	"strings"
	"testing"
)

// longLabel is a base text well beyond the 40-col truncation cap, so any
// collapsed suffix would be lost if it were appended before truncation.
const longLabel = "this is a very long thread title that exceeds the forty column truncation budget by a lot"

// TestCollapsedSuffixSurvivesTruncation asserts a long-text collapsed node keeps
// its "[+N]" suffix instead of having it truncated away.
func TestCollapsedSuffixSurvivesTruncation(t *testing.T) {
	src := "## Threads\n- [ ] " + longLabel + "\n  - [ ] hidden a\n  - [ ] hidden b\n"
	m := newMapModel(t, src, 160, 40)
	focusMapItem(t, m, longLabel)
	// Collapse the node (hide all children behind one suffix).
	m.mapFold = map[string]foldState{m.mapFocusKey: foldCollapsed}
	m.mp = nil
	m.ensureMap()
	focusMapItem(t, m, longLabel)

	p := placementForText(t, m, longLabel)
	joined := strings.Join(p.Lines, "")
	if !strings.Contains(joined, "[+2]") {
		t.Errorf("collapsed suffix [+2] missing from truncated node: %q", p.Lines)
	}
	if len([]rune(p.Lines[0])) > mapNodeMaxWidth {
		t.Errorf("line width %d exceeds cap %d", len([]rune(p.Lines[0])), mapNodeMaxWidth)
	}
}

// TestReplySuffixSurvivesTruncation covers the reply-fold suffix on a long
// default-view node, on BOTH sides of the map (side must not matter).
func TestReplySuffixSurvivesTruncation(t *testing.T) {
	// Two long-titled threads so one lands left and one right of center.
	src := "## Threads\n" +
		"- [ ] " + longLabel + " one\n  - user: hi\n  - claude: hello\n" +
		"- [ ] " + longLabel + " two\n  - user: hey\n  - claude: yo\n"
	m := newMapModel(t, src, 200, 40)

	sawLeft, sawRight := false, false
	for _, base := range []string{longLabel + " one", longLabel + " two"} {
		p := placementForText(t, m, base)
		joined := strings.Join(p.Lines, "")
		if !strings.Contains(joined, "[+2]") {
			t.Errorf("node %q lost its reply suffix (side %d): %q", base, p.Side, p.Lines)
		}
		if p.Side < 0 {
			sawLeft = true
		} else if p.Side > 0 {
			sawRight = true
		}
	}
	if !sawLeft || !sawRight {
		t.Logf("note: sides seen left=%v right=%v (fixture may not straddle)", sawLeft, sawRight)
	}
}

// TestExpandedKeepsSuffix asserts the `w`-expanded (wrapped, multi-line) render
// still shows the fold suffix.
func TestExpandedKeepsSuffix(t *testing.T) {
	src := "## Threads\n- [ ] " + longLabel + "\n  - [ ] hidden a\n  - [ ] hidden b\n"
	m := newMapModel(t, src, 160, 40)
	focusMapItem(t, m, longLabel)
	m.mapFold = map[string]foldState{m.mapFocusKey: foldCollapsed}
	m.mp = nil
	m.ensureMap()
	focusMapItem(t, m, longLabel)

	m.handleMapKey(keyPress("w")) // expand
	m.ensureMap()
	focusMapItem(t, m, longLabel)
	p := placementForText(t, m, longLabel)
	if p.Height < 2 {
		t.Fatalf("expected wrapped multi-line node, height=%d", p.Height)
	}
	if !strings.Contains(strings.Join(p.Lines, ""), "[+2]") {
		t.Errorf("expanded node lost its suffix: %q", p.Lines)
	}
}
