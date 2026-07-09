package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nitsanavni/session-notes/internal/board"
)

// newModelAt builds a board-view model over src with an ISOLATED temp path (its
// own dir), so the shared journal / lock sidecars don't collide between tests.
func newModelAt(t *testing.T, src string, w, h int) *model {
	t.Helper()
	m := newModel()
	m.board = board.Parse(src)
	m.path = filepath.Join(t.TempDir(), "parity-test.md")
	m.board.Path = m.path
	if err := os.WriteFile(m.path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	m.lastDisk = src
	m.mode = modeBoard
	m.width, m.height = w, h
	m.rebuildPositions()
	return m
}

// cursorToText moves the outline cursor onto the first item whose text equals want.
func cursorToText(t *testing.T, m *model, want string) {
	t.Helper()
	for i, p := range m.positions {
		if !p.header && p.item != nil && p.item.Text == want {
			m.cursor = i
			m.selSec = p.sec
			return
		}
	}
	t.Fatalf("no outline item with text %q", want)
}

// TestOutlineBlockedToggle: `b` sets blocked [?] from the outline (the space
// cycle never reaches it) and toggles back to open. Web-parity.
func TestOutlineBlockedToggle(t *testing.T) {
	m := newModelAt(t, "## Threads\n- [ ] task\n", 80, 24)
	cursorToText(t, m, "task")

	m.handleBoardKey(keyPress("b"))
	if got := m.currentItem().Status; got != board.StatusBlocked {
		t.Fatalf("after b, status = %v, want blocked", got)
	}
	m.handleBoardKey(keyPress("b"))
	if got := m.currentItem().Status; got != board.StatusOpen {
		t.Fatalf("after second b, status = %v, want open", got)
	}
	// Plain (StatusNone) bullets are left alone.
	m2 := newModelAt(t, "## Ideas\n- plain\n", 80, 24)
	cursorToText(t, m2, "plain")
	m2.handleBoardKey(keyPress("b"))
	if got := m2.currentItem().Status; got != board.StatusNone {
		t.Fatalf("b on a plain bullet changed status to %v", got)
	}
}

// TestOutlineGotoFirstLast: g/G jump the cursor to the first / last visible stop.
func TestOutlineGotoFirstLast(t *testing.T) {
	m := newModelAt(t, "## A\n- [ ] one\n- [ ] two\n\n## B\n- [ ] three\n", 80, 24)
	m.cursor = 2
	m.handleBoardKey(keyPress("g"))
	if m.cursor != 0 {
		t.Fatalf("g -> cursor %d, want 0", m.cursor)
	}
	m.handleBoardKey(keyPress("G"))
	if m.cursor != len(m.positions)-1 {
		t.Fatalf("G -> cursor %d, want %d", m.cursor, len(m.positions)-1)
	}
	if m.selSec != m.positions[m.cursor].sec {
		t.Fatalf("G did not sync selSec")
	}
}

// TestAddHonorsLeadingStatus: typing "[>] foo" in the outline add creates an
// in-progress item with text "foo" (marker parsed + stripped), matching web/CLI.
func TestAddHonorsLeadingStatus(t *testing.T) {
	m := newModelAt(t, "## Threads\n", 80, 24)
	m.selSec = 0
	m.startInput(modeInputAdd, "", "add")
	m.input.SetValue("[>] foo")
	m.handleInputKey(keyPress("enter"))

	sec := m.board.Section("Threads")
	if sec == nil {
		t.Fatal("Threads section missing")
	}
	var it *board.Item
	for _, x := range sec.Items {
		if x.IsItem() {
			it = x
			break
		}
	}
	if it == nil {
		t.Fatalf("no parsed item added to Threads: %+v", sec.Items)
	}
	if it.Status != board.StatusInProgress {
		t.Errorf("status = %v, want in-progress", it.Status)
	}
	if it.Text != "foo" {
		t.Errorf("text = %q, want %q", it.Text, "foo")
	}
}

// TestUndoSharedJournal: the TUI's u reverts the last journaled edit on the
// shared on-disk timeline (board.Undo), not just its own in-memory history.
func TestUndoSharedJournal(t *testing.T) {
	m := newModelAt(t, "## Threads\n- [ ] task\n", 80, 24)
	cursorToText(t, m, "task")

	m.handleBoardKey(keyPress("!")) // urgent -> journaled write
	if !m.currentItem().Urgent {
		t.Fatal("! did not set urgent")
	}
	if u, _ := board.UndoDepths(m.path); u == 0 {
		t.Fatal("edit was not journaled to the shared timeline")
	}

	m.undo()
	if it := m.currentItem(); it == nil || it.Urgent {
		t.Fatalf("undo did not revert the urgent flag: %+v", it)
	}
	// The undo moved the entry onto the redo stack (shared timeline).
	if _, r := board.UndoDepths(m.path); r == 0 {
		t.Fatal("undo did not populate the shared redo stack")
	}
	m.redo()
	if !m.currentItem().Urgent {
		t.Fatal("redo did not re-apply the urgent flag")
	}
}

// TestHistoryView: H opens a read-only overlay listing journaled edits; a key
// press closes it. The board is never mutated by viewing.
func TestHistoryView(t *testing.T) {
	m := newModelAt(t, "## Threads\n- [ ] task\n", 80, 24)
	cursorToText(t, m, "task")
	m.handleBoardKey(keyPress("!")) // create a journal entry
	before := m.board.Render()

	m.handleBoardKey(keyPress("H"))
	if m.mode != modeHistory {
		t.Fatalf("H did not open history, mode = %v", m.mode)
	}
	out := stripANSI(m.viewHistory())
	if !strings.Contains(out, "history") {
		t.Errorf("history overlay missing title:\n%s", out)
	}
	if m.board.Render() != before {
		t.Error("viewing history mutated the board")
	}
	// Any non-scroll key closes it.
	m.handleHistoryKey(keyPress("q"))
	if m.mode == modeHistory {
		t.Error("history overlay did not close on a key press")
	}
}
