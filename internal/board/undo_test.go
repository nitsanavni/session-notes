package board

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func journalBoard(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "b.md")
	if err := os.WriteFile(path, []byte("## Plan\n- [ ] one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func edit(t *testing.T, path, author, old, new string) {
	t.Helper()
	err := EditUnderLockJournaled(path, "", author, func(c string) (string, error) {
		return strings.Replace(c, old, new, 1), nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestUndoRedoJournal(t *testing.T) {
	path := journalBoard(t)
	orig := read(t, path)

	edit(t, path, "web", "one", "two")
	afterOne := read(t, path)
	edit(t, path, "claude", "two", "three")

	// Two clients, one timeline: undo walks back both.
	if err := Undo(path); err != nil {
		t.Fatal(err)
	}
	if read(t, path) != afterOne {
		t.Fatal("first undo did not restore the middle state")
	}
	if err := Undo(path); err != nil {
		t.Fatal(err)
	}
	if read(t, path) != orig {
		t.Fatal("second undo did not restore the original")
	}
	if err := Undo(path); !errors.Is(err, ErrUndoEmpty) {
		t.Fatalf("undo on empty stack: want ErrUndoEmpty, got %v", err)
	}

	// Redo replays in order.
	if err := Redo(path); err != nil {
		t.Fatal(err)
	}
	if read(t, path) != afterOne {
		t.Fatal("redo did not reapply the first edit")
	}

	// A fresh journaled edit clears the redo line.
	edit(t, path, "web", "two", "four")
	if err := Redo(path); !errors.Is(err, ErrUndoEmpty) {
		t.Fatalf("redo after new edit: want ErrUndoEmpty, got %v", err)
	}
}

func TestUndoConflictWithUnjournaledWriter(t *testing.T) {
	path := journalBoard(t)
	edit(t, path, "web", "one", "two")
	// A TUI-style direct write (not journaled) intervenes.
	if err := os.WriteFile(path, []byte("## Plan\n- [ ] two\n- external\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Undo(path); !errors.Is(err, ErrUndoConflict) {
		t.Fatalf("want ErrUndoConflict, got %v", err)
	}
	if !strings.Contains(read(t, path), "external") {
		t.Fatal("conflict must not clobber the external write")
	}
}

func TestJournalSkipsNoopsAndTrims(t *testing.T) {
	path := journalBoard(t)
	// A transform that changes nothing journals nothing.
	if err := EditUnderLockJournaled(path, "", "web", func(c string) (string, error) { return c, nil }); err != nil {
		t.Fatal(err)
	}
	if u, _ := UndoDepths(path); u != 0 {
		t.Fatalf("noop journaled: depth %d", u)
	}
	// The stack trims to the last undoLimit entries.
	for i := 0; i < undoLimit+10; i++ {
		edit(t, path, "web", "\n", fmt.Sprintf("\n<!-- %d -->\n", i))
	}
	if u, _ := UndoDepths(path); u != undoLimit {
		t.Fatalf("depth %d, want %d", u, undoLimit)
	}
}

func TestCorruptJournalDegradesGracefully(t *testing.T) {
	path := journalBoard(t)
	if err := os.WriteFile(UndoPath(path), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if u, r := UndoDepths(path); u != 0 || r != 0 {
		t.Fatal("corrupt journal should read as empty")
	}
	edit(t, path, "web", "one", "two") // must not fail
	if u, _ := UndoDepths(path); u != 1 {
		t.Fatal("journaling after corruption failed")
	}
}
