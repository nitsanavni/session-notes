package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/nitsanavni/session-notes/internal/board"
)

// Web<->TUI parity for the map view: live status footer, g/G / 1-9 /
// tab section nav, b = blocked toggle (esc steps out), fresh-node accent,
// user:-stamped child adds, and the recents overlay's x verb.

const parityMapSrc = "## Plan\n- [ ] alpha\n- [ ] beta\n- [ ] gamma\n## Threads\n- [ ] topic\n  - claude: reply\n## Ideas\n- spark\n"

func TestMapFooterIncludesStatusSegment(t *testing.T) {
	t.Setenv("SESSION_NOTES_DIR", t.TempDir())
	board.UpdateStatus("mapsess", func(s *board.SessionStatus) {
		s.Model = "Opus"
		s.Activity = "working"
	})
	src := "---\nsession: mapsess\ncwd: /tmp/x\n---\n\n## Plan\n- [ ] x\n"
	m := newMapModel(t, src, 120, 40)
	out := stripANSI(m.viewMapFooter())
	if !strings.Contains(out, "Opus") || !strings.Contains(out, "working") {
		t.Fatalf("map footer should carry the live status segment, got:\n%s", out)
	}
}

func TestMapGAndGSiblingEnds(t *testing.T) {
	m := newMapModel(t, parityMapSrc, 140, 40)
	focusMapItem(t, m, "beta")
	m.handleMapKey(keyPress("G"))
	if ref := m.mp.refs[m.mp.focus]; ref.kind != refItem || ref.item.Text != "gamma" {
		t.Fatalf("G should focus the last sibling, got %+v", ref)
	}
	m.handleMapKey(keyPress("g"))
	if ref := m.mp.refs[m.mp.focus]; ref.kind != refItem || ref.item.Text != "alpha" {
		t.Fatalf("g should focus the first sibling, got %+v", ref)
	}
}

func TestMapNumberJumpAndTabCycle(t *testing.T) {
	m := newMapModel(t, parityMapSrc, 140, 40)
	m.handleMapKey(keyPress("2"))
	if ref := m.mp.refs[m.mp.focus]; ref.kind != refSection || ref.section != "Threads" {
		t.Fatalf("2 should focus the Threads section node, got %+v", ref)
	}
	m.handleMapKey(keyPress("tab"))
	if ref := m.mp.refs[m.mp.focus]; ref.kind != refSection || ref.section != "Ideas" {
		t.Fatalf("tab should cycle to the next section, got %+v", ref)
	}
	m.handleMapKey(keyPress("tab")) // wraps
	if ref := m.mp.refs[m.mp.focus]; ref.kind != refSection || ref.section != "Plan" {
		t.Fatalf("tab should wrap to the first section, got %+v", ref)
	}
	m.handleMapKey(tea.KeyMsg{Type: tea.KeyShiftTab})
	if ref := m.mp.refs[m.mp.focus]; ref.kind != refSection || ref.section != "Ideas" {
		t.Fatalf("shift+tab should cycle back, got %+v", ref)
	}
}

func TestMapBTogglesBlocked(t *testing.T) {
	m := newMapModel(t, parityMapSrc, 140, 40)
	focusMapItem(t, m, "alpha")
	it := m.mp.refs[m.mp.focus].item
	m.handleMapKey(keyPress("b"))
	if it.Status != board.StatusBlocked {
		t.Fatalf("b should set blocked, got %v", it.Status)
	}
	// The toggle re-layouts; re-focus the same item and toggle back.
	m.ensureMap()
	focusMapItem(t, m, "alpha")
	m.handleMapKey(keyPress("b"))
	if it.Status != board.StatusOpen {
		t.Fatalf("second b should clear blocked back to open, got %v", it.Status)
	}
}

func TestMapFreshNodeAccent(t *testing.T) {
	forceColor(t)
	m := newMapModel(t, parityMapSrc, 140, 40)
	// Mark "gamma" fresh (not the focused node — focus sits on the center).
	var gammaRaw string
	for _, it := range m.board.Sections[0].Items {
		if it.Text == "gamma" {
			gammaRaw = strings.TrimSpace(it.Raw())
		}
	}
	m.fresh[gammaRaw] = true
	out := m.viewMap()
	if !strings.Contains(out, "38;5;215") {
		t.Fatalf("a fresh node should carry the fresh accent (38;5;215):\n%q", out)
	}
	// Landing focus on it settles the mark.
	focusMapItem(t, m, "gamma")
	m.viewMap()
	if m.fresh[gammaRaw] {
		t.Fatal("focusing a fresh node should clear its mark")
	}
}

func TestMapAddChildStampsUserAuthor(t *testing.T) {
	m := newMapModel(t, parityMapSrc, 140, 40)
	focusMapItem(t, m, "topic")
	m.handleMapKey(keyPress("a")) // add child under "topic"
	if m.mode != modeMapAdd || m.mapInputParent == nil {
		t.Fatalf("a should open the add-child input, mode=%v", m.mode)
	}
	m.input.SetValue("sounds good")
	m.handleMapInputKey(tea.KeyMsg{Type: tea.KeyEnter})
	parent := m.mapInputParent
	kid := parent.Children[len(parent.Children)-1]
	if kid.Text != "user: sounds good" {
		t.Fatalf("map add-child should stamp the user: author tag, got %q", kid.Text)
	}
}

func TestRecentsXDismissesAll(t *testing.T) {
	m := newTestModel(parityMapSrc, 100, 40)
	m.recents = []recentEntry{recentEntryFor("- [ ] alpha"), recentEntryFor("- [ ] beta")}
	m.openRecents()
	m.handleRecentsKey(keyPress("x"))
	if len(m.recents) != 0 {
		t.Fatalf("x should dismiss all entries, %d left", len(m.recents))
	}
	if m.mode == modeRecents {
		t.Fatal("x should close the overlay")
	}
	// A later change starts a fresh list (no persistent suppression).
	m.recordRecents([]string{"- [ ] gamma"})
	if len(m.recents) != 1 {
		t.Fatal("a later change must resurface in recents after dismiss")
	}
}
