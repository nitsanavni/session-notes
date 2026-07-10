package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOutlineYankLinkPath: `O` on an item with a [[link]] copies the note's
// absolute path (revealed in the status when no clipboard tool ran) and creates
// the note file so the copied path points at something real.
func TestOutlineYankLinkPath(t *testing.T) {
	notesRoot := t.TempDir()
	t.Setenv("SESSION_NOTES_DIR", notesRoot)
	src := "---\nsession: s-test\n---\n## Threads\n- [ ] see [[design]] doc\n"
	m := newModelAt(t, src, 80, 24)
	cursorToText(t, m, "see [[design]] doc")

	m.handleBoardKey(keyPress("O"))

	note := filepath.Join(notesRoot, "s-test.notes", "design.md")
	if _, err := os.Stat(note); err != nil {
		t.Fatalf("O did not create the note file: %v", err)
	}
	// The status always reveals the absolute path (whether or not pbcopy ran).
	if !strings.Contains(m.status, "design.md") || !strings.Contains(m.status, notesRoot) {
		t.Fatalf("status %q should contain the absolute note path under %s", m.status, notesRoot)
	}
}

// TestOutlineYankLinkPathNoLink: `O` on a linkless item reports it, copies nothing.
func TestOutlineYankLinkPathNoLink(t *testing.T) {
	m := newModelAt(t, "## Threads\n- [ ] plain item\n", 80, 24)
	cursorToText(t, m, "plain item")
	m.handleBoardKey(keyPress("O"))
	if !strings.Contains(m.status, "no [[link]]") {
		t.Errorf("status %q should note there is no link", m.status)
	}
}

// TestLinkPickerYankPath: in the multi-link picker, `y` copies the highlighted
// note's absolute path and keeps the picker open.
func TestLinkPickerYankPath(t *testing.T) {
	notesRoot := t.TempDir()
	t.Setenv("SESSION_NOTES_DIR", notesRoot)
	src := "---\nsession: s-test\n---\n## Threads\n- [ ] [[alpha]] and [[beta]]\n"
	m := newModelAt(t, src, 80, 24)
	cursorToText(t, m, "[[alpha]] and [[beta]]")

	m.handleBoardKey(keyPress("o")) // opens the chooser (2 links)
	if m.mode != modeLinkPick {
		t.Fatalf("mode = %v, want modeLinkPick", m.mode)
	}
	m.handleLinkPickKey(keyPress("y"))
	if m.mode != modeLinkPick {
		t.Errorf("y left the picker (mode=%v), should stay to allow open/copy", m.mode)
	}
	if !strings.Contains(m.status, "alpha.md") {
		t.Errorf("status %q should mention the highlighted note path", m.status)
	}
}
