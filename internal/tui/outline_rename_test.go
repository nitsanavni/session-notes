package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// openOutlineBoard writes src to a throwaway board file and opens it in the list
// (outline) view, so key handlers persist through the real locked save path.
func openOutlineBoard(t *testing.T, src string) (*model, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "b.md")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	m := newModel()
	if err := m.openBoard(path); err != nil {
		t.Fatal(err)
	}
	return m, path
}

// cursorToHeader puts the cursor on the header stop of the named section.
func cursorToHeader(t *testing.T, m *model, title string) {
	t.Helper()
	for i, p := range m.positions {
		if p.header && m.board.Sections[p.sec].Title == title {
			m.cursor = i
			m.selSec = p.sec
			return
		}
	}
	t.Fatalf("no header stop for section %q", title)
}

// TestOutlineRenameSectionPersists renames a section via e on its header in the
// outline view and asserts the heading changes, contents survive, and it lands
// on disk — returning to the outline view (not the map).
func TestOutlineRenameSectionPersists(t *testing.T) {
	m, path := openOutlineBoard(t, "## Threads\n- [ ] keep me\n  - user: a reply\n")
	cursorToHeader(t, m, "Threads")

	m.handleBoardKey(keyPress("e"))
	if m.mode != modeInputRename {
		t.Fatalf("e on a header did not enter rename mode, got %v", m.mode)
	}
	if m.input.Value() != "Threads" {
		t.Fatalf("rename input not pre-filled, got %q", m.input.Value())
	}
	m.input.SetValue("Discussion")
	m.handleInputKey(tea.KeyMsg{Type: tea.KeyEnter})

	if m.mapView {
		t.Errorf("rename from outline should not switch to the map view")
	}
	if m.board.Section("Discussion") == nil {
		t.Fatalf("section not renamed in memory: %+v", m.board.Sections)
	}
	if m.board.Section("Threads") != nil {
		t.Errorf("old section title still present")
	}
	sec := m.board.Section("Discussion")
	if len(sec.Items) == 0 || sec.Items[0].Text != "keep me" {
		t.Errorf("section contents lost on rename: %+v", sec.Items)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "## Discussion") || !strings.Contains(string(data), "keep me") {
		t.Errorf("rename not persisted with contents:\n%s", data)
	}
}

// TestOutlineRenameMigratesCollapseState asserts the per-title collapse override
// follows a rename done from the outline view and leaves no stale key behind.
func TestOutlineRenameMigratesCollapseState(t *testing.T) {
	m, _ := openOutlineBoard(t, "## Threads\n- [ ] x\n\n## Ideas\n- [ ] y\n")
	m.setCollapsed("Threads", true)
	m.rebuildPositions()
	cursorToHeader(t, m, "Threads")

	m.handleBoardKey(keyPress("e"))
	m.input.SetValue("Discussion")
	m.handleInputKey(tea.KeyMsg{Type: tea.KeyEnter})

	if !m.sectionCollapsed("Discussion") {
		t.Errorf("collapse state did not migrate to the renamed section")
	}
	if _, ok := m.collapsed["Threads"]; ok {
		t.Errorf("stale collapse state left under old title")
	}
}

// TestOutlineRenameDuplicateRefused asserts renaming onto an existing section's
// name is refused (no merge) and leaves both sections intact.
func TestOutlineRenameDuplicateRefused(t *testing.T) {
	m, _ := openOutlineBoard(t, "## Threads\n- [ ] a\n\n## Ideas\n- [ ] b\n")
	cursorToHeader(t, m, "Threads")

	m.handleBoardKey(keyPress("e"))
	m.input.SetValue("Ideas")
	m.handleInputKey(tea.KeyMsg{Type: tea.KeyEnter})

	if m.board.Section("Threads") == nil {
		t.Errorf("Threads was lost on a refused rename")
	}
	count := 0
	for _, s := range m.board.Sections {
		if s.Title == "Ideas" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly one Ideas section, got %d", count)
	}
	if m.status == "" {
		t.Errorf("expected a status error on refused rename")
	}
}

// TestOutlineEditItemStillEdits asserts e on an item (not a header) still opens
// the inline item editor, unchanged by the header-rename addition.
func TestOutlineEditItemStillEdits(t *testing.T) {
	m, _ := openOutlineBoard(t, "## Threads\n- [ ] original\n")
	// Move cursor to the item (positions[0] is the header).
	for i, p := range m.positions {
		if p.item != nil && p.item.Text == "original" {
			m.cursor = i
			break
		}
	}
	m.handleBoardKey(keyPress("e"))
	if m.mode != modeInputEdit {
		t.Fatalf("e on an item did not enter edit mode, got %v", m.mode)
	}
	m.input.SetValue("changed")
	m.handleInputKey(tea.KeyMsg{Type: tea.KeyEnter})

	sec := m.board.Section("Threads")
	if len(sec.Items) == 0 || sec.Items[0].Text != "changed" {
		t.Errorf("item edit did not apply: %+v", sec.Items)
	}
	if m.board.Section("changed") != nil {
		t.Errorf("item edit unexpectedly created a section")
	}
}
