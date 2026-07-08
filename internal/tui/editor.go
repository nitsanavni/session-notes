package tui

import (
	"os"
	"os/exec"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/nitsanavni/session-notes/internal/board"
)

// openEditor opens the board in $EDITOR — but on a TEMP COPY, not the board
// file, because $EDITOR is not lock-aware and would clobber Claude's concurrent
// writes on save. editorBase records the board content at launch; on close,
// finishEditorMerge 3-way merges the user's buffer with Claude's writes.
func (m *model) openEditor() tea.Cmd {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	base, _ := os.ReadFile(m.path)
	m.editorBase = string(base)
	tmp, err := os.CreateTemp(filepath.Dir(m.path), ".board-edit-*.md")
	if err != nil {
		// Can't make a temp copy: fall back to editing the board directly (no
		// merge). editorBuf stays empty so editorDoneMsg just reloads.
		m.editorBuf = ""
		c := exec.Command(editor, m.path) // #nosec G204 -- user's own $EDITOR
		return tea.ExecProcess(c, func(error) tea.Msg { return editorDoneMsg{} })
	}
	_, _ = tmp.Write(base)
	_ = tmp.Close()
	m.editorBuf = tmp.Name()
	c := exec.Command(editor, tmp.Name()) // #nosec G204 -- user's own $EDITOR
	return tea.ExecProcess(c, func(error) tea.Msg { return editorDoneMsg{} })
}

// finishEditorMerge runs when the board $EDITOR (editing a temp copy) returns.
// It recovers the user's buffer ("mine") and 3-way merges it with any writes
// Claude made to the real board while the editor was open, never clobbering
// either side. Non-auto-mergeable conflicts are written as markers with an
// urgent @claude item (see board.SaveEditorMerge) rather than defaulting to the
// user. The E-handler snapshot is the pre-editor undo base; when Claude wrote
// meanwhile, that write is recorded as a second undo step.
func (m *model) finishEditorMerge() {
	buf := m.editorBuf
	m.editorBuf = ""
	mine, rerr := os.ReadFile(buf)
	_ = os.Remove(buf)
	if rerr != nil {
		m.reload()
		m.status = "editor buffer lost: " + rerr.Error()
		return
	}
	res, err := board.SaveEditorMerge(m.path, m.editorBase, string(mine))
	if err != nil {
		m.reload()
		m.status = "editor save failed: " + err.Error()
		return
	}
	if res.Theirs != m.editorBase {
		// Claude wrote to the board while the editor was open: record that state
		// so undo steps back over it before reaching the pre-editor snapshot.
		m.hist.record(res.Theirs)
	}
	m.editorBase = ""
	m.reload()
	if res.Conflicted {
		m.status = "editor merge conflict — asked @claude to reconcile"
	} else {
		m.status = "editor changes merged"
	}
}
