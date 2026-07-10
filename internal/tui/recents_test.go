package tui

import (
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// writeBoardFile points the model at a real temp file so reloadExternal reads
// fresh disk content and feeds the recents ring, and returns the path.
func recentsModel(t *testing.T, src string) *model {
	t.Helper()
	m := newTestModel(src, 100, 40)
	f, err := os.CreateTemp(t.TempDir(), "recents-*.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(src); err != nil {
		t.Fatal(err)
	}
	f.Close()
	m.path = f.Name()
	m.board.Path = f.Name()
	m.lastDisk = src
	return m
}

// An external write (a new item on disk) accumulates a recents entry keyed by
// the changed item's trimmed raw line.
func TestRecentsAccumulateOnExternalReload(t *testing.T) {
	src := "## Plan\n- [ ] one\n"
	m := recentsModel(t, src)
	if len(m.recents) != 0 {
		t.Fatalf("expected no recents initially, got %d", len(m.recents))
	}
	// Simulate an external writer appending a claude reply.
	next := "## Plan\n- [ ] one\n- claude: fresh news\n"
	if err := os.WriteFile(m.path, []byte(next), 0o644); err != nil {
		t.Fatal(err)
	}
	m.reloadExternal()
	if len(m.recents) != 1 {
		t.Fatalf("expected 1 recents entry, got %d", len(m.recents))
	}
	e := m.recents[0]
	if !strings.Contains(e.primary, "fresh news") {
		t.Errorf("headline should carry the changed text, got %q", e.primary)
	}
	if e.meta != "claude replied" {
		t.Errorf("meta should read 'claude replied', got %q", e.meta)
	}
}

// Newest changes land on top and the ring is capped at recentsMax.
func TestRecentsNewestFirstAndCap(t *testing.T) {
	m := newTestModel("## Plan\n- [ ] a\n", 100, 40)
	// Feed more than the cap, each a distinct item line.
	for i := 0; i < recentsMax+10; i++ {
		m.recordRecents([]string{"- [ ] item-" + string(rune('A'+i%26)) + strings.Repeat("x", i)})
	}
	if len(m.recents) != recentsMax {
		t.Fatalf("ring should cap at %d, got %d", recentsMax, len(m.recents))
	}
	// The last fed entry is newest -> on top.
	last := recentsMax + 9
	wantKey := "- [ ] item-" + string(rune('A'+last%26)) + strings.Repeat("x", last)
	if m.recents[0].key != wantKey {
		t.Errorf("newest entry should be on top; got key %q", m.recents[0].key)
	}
}

// Enter in the overlay jumps the cursor onto the changed item and clears its
// fresh mark.
func TestRecentsEnterJumpsAndClearsFresh(t *testing.T) {
	src := "## Plan\n- [ ] one\n- claude: target line\n"
	m := newTestModel(src, 100, 40)
	key := "- claude: target line"
	m.fresh[key] = true
	m.recents = []recentEntry{recentEntryFor(key)}
	m.openRecents()
	m.handleRecentsKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode == modeRecents {
		t.Fatal("enter should close the overlay")
	}
	if m.fresh[key] {
		t.Error("enter should clear the jumped item's fresh mark")
	}
	if it := m.currentItem(); it == nil || it.DisplayText() != "claude: target line" {
		t.Fatalf("enter should land the cursor on the change, on %v", m.currentItem())
	}
}

// Enter reveals a match buried in a collapsed section before landing.
func TestRecentsJumpRevealsCollapsedSection(t *testing.T) {
	src := "## Plan\n- [ ] one\n## Ideas\n- [ ] buried spark\n"
	m := newTestModel(src, 100, 40)
	m.setCollapsed("Ideas", true)
	m.rebuildPositions()
	key := "- [ ] buried spark"
	m.recents = []recentEntry{recentEntryFor(key)}
	m.openRecents()
	m.handleRecentsKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.sectionCollapsed("Ideas") {
		t.Error("jump should reveal the collapsed owning section")
	}
	if it := m.currentItem(); it == nil || it.DisplayText() != "buried spark" {
		t.Fatalf("cursor should land on the revealed change, on %v", m.currentItem())
	}
}
