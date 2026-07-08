package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFinishEditorMergeClean drives the editor-done flow: the editor edited a
// temp copy (mine) while Claude wrote a disjoint region on the real board. The
// merge adopts both, reloads, records Claude's write for undo, and reports clean.
func TestFinishEditorMergeClean(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "b.md")
	base := "## Plan\n- [ ] step one\n\n## Log\n- 10:00 start: began\n"
	if err := os.WriteFile(path, []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}
	m := newModel()
	if err := m.openBoard(path); err != nil {
		t.Fatal(err)
	}
	m.snapshot() // the E-handler's pre-editor snapshot

	// Claude appended to Log on the real board while the editor was open.
	theirs := "## Plan\n- [ ] step one\n\n## Log\n- 10:00 start: began\n- 10:05 claude: work\n"
	if err := os.WriteFile(path, []byte(theirs), 0o644); err != nil {
		t.Fatal(err)
	}
	// The user's editor buffer (temp copy) edited the Plan step.
	buf := filepath.Join(dir, ".board-edit-x.md")
	mine := "## Plan\n- [ ] step one EDITED\n\n## Log\n- 10:00 start: began\n"
	if err := os.WriteFile(buf, []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}
	m.editorBase = base
	m.editorBuf = buf

	m.finishEditorMerge()

	if m.editorBuf != "" {
		t.Errorf("editorBuf not cleared: %q", m.editorBuf)
	}
	if _, err := os.Stat(buf); !os.IsNotExist(err) {
		t.Errorf("editor buffer temp file not removed")
	}
	if m.status != "editor changes merged" {
		t.Errorf("status = %q", m.status)
	}
	got := m.board.Render()
	if !strings.Contains(got, "step one EDITED") || !strings.Contains(got, "10:05 claude: work") {
		t.Errorf("merge dropped a side: %q", got)
	}
	// Claude's write differed from base, so it must be recorded for undo.
	if prev, ok := m.hist.undoTo(m.currentContent()); !ok {
		t.Fatal("expected an undo entry for Claude's external write")
	} else if !strings.Contains(prev, "10:05 claude: work") {
		t.Errorf("first undo should reveal Claude's write: %q", prev)
	}
}

// TestFinishEditorMergeConflict: both sides changed the same line; the flow
// writes markers, keeps both, and reports the conflict status.
func TestFinishEditorMergeConflict(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "b.md")
	base := "## Plan\n- [ ] shared\n"
	if err := os.WriteFile(path, []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}
	m := newModel()
	if err := m.openBoard(path); err != nil {
		t.Fatal(err)
	}
	theirs := "## Plan\n- [ ] shared claude\n"
	if err := os.WriteFile(path, []byte(theirs), 0o644); err != nil {
		t.Fatal(err)
	}
	buf := filepath.Join(dir, ".board-edit-y.md")
	mine := "## Plan\n- [ ] shared user\n"
	if err := os.WriteFile(buf, []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}
	m.editorBase = base
	m.editorBuf = buf

	m.finishEditorMerge()

	if m.status != "editor merge conflict — asked @claude to reconcile" {
		t.Errorf("status = %q", m.status)
	}
	got := m.board.Render()
	if !strings.Contains(got, "shared user") || !strings.Contains(got, "shared claude") {
		t.Errorf("a side dropped: %q", got)
	}
	if !strings.Contains(got, "<<<<<<<") {
		t.Errorf("conflict markers missing: %q", got)
	}
	if len(m.board.UrgentOpenItems()) == 0 {
		t.Errorf("urgent @claude item missing: %q", got)
	}
}
