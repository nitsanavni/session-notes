package tui

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMapOpenSingleLink opens the sole [[link]] on the focused item, creating
// the note file, and returns a command (the editor round-trip).
func TestMapOpenSingleLink(t *testing.T) {
	notesRoot := t.TempDir()
	t.Setenv("SESSION_NOTES_DIR", notesRoot) // keep note creation off ~/.claude/boards
	src := "---\nsession: s-test\n---\n## Threads\n- [ ] see [[design]] doc\n"
	m := newMapModel(t, src, 120, 40)
	focusMapItem(t, m, "see [[design]] doc")

	t.Setenv("EDITOR", "true") // a no-op editor
	_, cmd := m.handleMapKey(keyPress("o"))
	if cmd == nil {
		t.Fatal("o on a single-link item returned no command")
	}
	// The note file should have been seeded in the session's notes dir.
	note := filepath.Join(notesRoot, "s-test.notes", "design.md")
	if _, err := os.Stat(note); err != nil {
		t.Errorf("note file not created: %v", err)
	}
}

// TestMapOpenMultiLinkChooser opens the modeLinkPick chooser when the item has
// several links, and returns to the map after picking.
func TestMapOpenMultiLinkChooser(t *testing.T) {
	t.Setenv("SESSION_NOTES_DIR", t.TempDir())
	src := "---\nsession: s-test\n---\n## Threads\n- [ ] [[alpha]] and [[beta]]\n"
	m := newMapModel(t, src, 120, 40)
	focusMapItem(t, m, "[[alpha]] and [[beta]]")

	m.handleMapKey(keyPress("o"))
	if m.mode != modeLinkPick {
		t.Fatalf("mode = %v, want modeLinkPick", m.mode)
	}
	if len(m.linkOpts) != 2 {
		t.Fatalf("linkOpts = %v, want 2", m.linkOpts)
	}
	t.Setenv("EDITOR", "true")
	m.handleLinkPickKey(keyPress("esc"))
	if m.mode != modeBoard {
		t.Errorf("cancel did not return to board mode (%v)", m.mode)
	}
	if !m.mapView {
		t.Errorf("map view lost after link chooser")
	}
}

// TestMapOpenNoLink and section/center produce a status message, no crash.
func TestMapOpenNoLink(t *testing.T) {
	src := "## Threads\n- [ ] plain item\n"
	m := newMapModel(t, src, 120, 40)
	focusMapItem(t, m, "plain item")
	if _, cmd := m.handleMapKey(keyPress("o")); cmd != nil {
		t.Errorf("o on a linkless item should not open anything")
	}
	if m.status == "" {
		t.Errorf("expected a status message on linkless o")
	}

	focusMapSection(t, m, "Threads")
	m.handleMapKey(keyPress("o"))
	if m.status == "" {
		t.Errorf("expected a status message opening a section")
	}
}
