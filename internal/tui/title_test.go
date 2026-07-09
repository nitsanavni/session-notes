package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/nitsanavni/session-notes/internal/board"
)

// titleBoard is a small frontmatter board carrying an unknown key, so tests can
// assert the key survives a title edit.
const titleBoard = `---
session: abc-12345678
cwd: /home/nitsan/code/foo
started: 2026-07-06T21:30:00+03:00
extra-key: kept verbatim
---

## Plan
- [ ] step one
`

// openTitleBoard writes titleBoard to a temp file and returns a model over it.
func openTitleBoard(t *testing.T) (*model, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "b.md")
	if err := os.WriteFile(path, []byte(titleBoard), 0o644); err != nil {
		t.Fatal(err)
	}
	m := newModel()
	if err := m.openBoard(path); err != nil {
		t.Fatal(err)
	}
	return m, path
}

// editTitle drives the inline title editor to completion with the given value.
func editTitle(m *model, value string) {
	m.startTitleInput()
	m.input.SetValue(value)
	m.handleTitleInputKey(tea.KeyMsg{Type: tea.KeyEnter})
}

func TestTitleEditPersistsAndKeepsUnknownKeys(t *testing.T) {
	m, path := openTitleBoard(t)
	editTitle(m, "auth refactor")

	if m.board.Frontmatter.Title != "auth refactor" {
		t.Errorf("in-memory title = %q", m.board.Frontmatter.Title)
	}
	data, _ := os.ReadFile(path)
	got := string(data)
	if !strings.Contains(got, "title: auth refactor") {
		t.Errorf("title not persisted to disk:\n%s", got)
	}
	if !strings.Contains(got, "extra-key: kept verbatim") {
		t.Errorf("unknown frontmatter key lost:\n%s", got)
	}
	if m.lastDisk != got {
		t.Error("lastDisk out of sync with disk")
	}
}

func TestTitleEditEmptyClears(t *testing.T) {
	m, path := openTitleBoard(t)
	editTitle(m, "temporary name")
	editTitle(m, "   ") // whitespace-only clears

	if m.board.Frontmatter.Title != "" {
		t.Errorf("title not cleared: %q", m.board.Frontmatter.Title)
	}
	data, _ := os.ReadFile(path)
	got := string(data)
	if strings.Contains(got, "title:") {
		t.Errorf("title line still on disk after clear:\n%s", got)
	}
	// The header now falls back to the session id.
	if boardTitle(m.board) != "abc-12345678" {
		t.Errorf("header did not fall back to session id: %q", boardTitle(m.board))
	}
}

func TestTitleUndoRestores(t *testing.T) {
	m, _ := openTitleBoard(t)
	editTitle(m, "first title")
	editTitle(m, "second title")
	if m.board.Frontmatter.Title != "second title" {
		t.Fatalf("setup: title = %q", m.board.Frontmatter.Title)
	}
	m.undo()
	if m.board.Frontmatter.Title != "first title" {
		t.Errorf("undo did not restore prior title: %q", m.board.Frontmatter.Title)
	}
	m.undo()
	if m.board.Frontmatter.Title != "" {
		t.Errorf("second undo did not restore empty title: %q", m.board.Frontmatter.Title)
	}
}

// The map center node's e used to be refused ("only items are editable"); it now
// opens the title editor.
func TestMapCenterEditOpensTitleInput(t *testing.T) {
	m, _ := openTitleBoard(t)
	m.enterMap()
	// Focus the center (the board title node).
	m.setMapFocus(m.mp.root)
	m.status = ""
	m.startMapEdit()
	if m.mode != modeInputTitle {
		t.Errorf("map center e did not open title input; mode = %v, status = %q", m.mode, m.status)
	}
	if m.status != "" {
		t.Errorf("map center e was refused: %q", m.status)
	}
	// The input is pre-filled with the current (empty) title.
	if m.input.Value() != "" {
		t.Errorf("title input not pre-filled with current title: %q", m.input.Value())
	}
}

// applyOp's opSetTitle sets the title absolutely on a fresh disk tree while
// leaving every other line (including unknown frontmatter keys) intact.
func TestApplyOpSetTitleKeepsUnknownKeys(t *testing.T) {
	fresh := board.Parse(titleBoard)
	msg := applyOp(fresh, pendingOp{typ: opSetTitle, payload: "rebased title"})
	if fresh.Frontmatter.Title != "rebased title" {
		t.Errorf("title not set on rebase: %q", fresh.Frontmatter.Title)
	}
	if !strings.Contains(fresh.Render(), "extra-key: kept verbatim") {
		t.Errorf("unknown key lost on rebase:\n%s", fresh.Render())
	}
	if !strings.Contains(msg, "title") {
		t.Errorf("msg = %q", msg)
	}
}
