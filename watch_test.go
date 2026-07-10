package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newWatchFixture writes a board file in a temp dir and returns a watcher with a
// default snapshot alongside it (notes disabled unless the caller sets it).
func newWatchFixture(t *testing.T, content string) (*watcher, string) {
	t.Helper()
	dir := t.TempDir()
	boardPath := filepath.Join(dir, "board.md")
	if err := os.WriteFile(boardPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	w := &watcher{board: boardPath, snapshot: defaultSnapshotPath(boardPath)}
	return w, boardPath
}

func TestWatchInitThenNoChange(t *testing.T) {
	w, _ := newWatchFixture(t, "# board\n- [ ] one\n")
	if err := w.init(); err != nil {
		t.Fatal(err)
	}
	out, changed, err := w.poll()
	if err != nil {
		t.Fatal(err)
	}
	if changed || out != "" {
		t.Fatalf("expected no change right after init, got changed=%v out=%q", changed, out)
	}
}

func TestWatchDetectsChangeAsDiff(t *testing.T) {
	w, boardPath := newWatchFixture(t, "# board\n- [ ] one\n")
	if err := w.init(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(boardPath, []byte("# board\n- [ ] one\n- [ ] two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, changed, err := w.poll()
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected change to be detected")
	}
	if !strings.Contains(out, "+- [ ] two") {
		t.Fatalf("expected added line in diff, got %q", out)
	}
	// Snapshot advanced: a second poll with no further edit reports nothing.
	out2, changed2, err := w.poll()
	if err != nil {
		t.Fatal(err)
	}
	if changed2 || out2 != "" {
		t.Fatalf("expected snapshot to be advanced, got changed=%v out=%q", changed2, out2)
	}
}

func TestWatchRemovalDiff(t *testing.T) {
	w, boardPath := newWatchFixture(t, "# board\n- [ ] one\n- [ ] two\n")
	if err := w.init(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(boardPath, []byte("# board\n- [ ] one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, changed, _ := w.poll()
	if !changed || !strings.Contains(out, "-- [ ] two") {
		t.Fatalf("expected removed line in diff, got changed=%v out=%q", changed, out)
	}
}

// TestWatchSnapshotFiltersSelfEdit mimics `edit --refresh-snapshot`: a writer
// that updates board AND snapshot together must be invisible to the watcher.
func TestWatchSnapshotFiltersSelfEdit(t *testing.T) {
	w, boardPath := newWatchFixture(t, "# board\n- [ ] one\n")
	if err := w.init(); err != nil {
		t.Fatal(err)
	}
	selfEdit := "# board\n- [ ] one\n- [ ] self\n"
	if err := os.WriteFile(boardPath, []byte(selfEdit), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(w.snapshot, []byte(selfEdit), 0o644); err != nil {
		t.Fatal(err)
	}
	out, changed, err := w.poll()
	if err != nil {
		t.Fatal(err)
	}
	if changed || out != "" {
		t.Fatalf("self-edit should be filtered, got changed=%v out=%q", changed, out)
	}
}

// TestWatchInitPreservesPendingChange: a change that landed before init (i.e.
// between two --once arms) is still reported because init does not clobber an
// existing snapshot.
func TestWatchInitPreservesPendingChange(t *testing.T) {
	w, boardPath := newWatchFixture(t, "# board\n- [ ] one\n")
	// Pre-seed a snapshot at the old content, then change the board before init.
	if err := os.WriteFile(w.snapshot, []byte("# board\n- [ ] one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(boardPath, []byte("# board\n- [ ] one\n- [ ] pending\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := w.init(); err != nil {
		t.Fatal(err)
	}
	out, changed, _ := w.poll()
	if !changed || !strings.Contains(out, "+- [ ] pending") {
		t.Fatalf("pending change should survive init, got changed=%v out=%q", changed, out)
	}
}

func TestWatchNotesDir(t *testing.T) {
	w, boardPath := newWatchFixture(t, "# board\n")
	notesDir := notesDirFor(boardPath)
	if err := os.MkdirAll(notesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	w.notesDir = notesDir
	w.notesState = w.snapshot + ".notes.json"
	if err := w.init(); err != nil {
		t.Fatal(err)
	}
	// Add a note file.
	if err := os.WriteFile(filepath.Join(notesDir, "design.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, changed, err := w.poll()
	if err != nil {
		t.Fatal(err)
	}
	if !changed || !strings.Contains(out, "note added: design.md") {
		t.Fatalf("expected note-added report, got changed=%v out=%q", changed, out)
	}
	// Modify it.
	if err := os.WriteFile(filepath.Join(notesDir, "design.md"), []byte("hi there\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, changed, _ = w.poll()
	if !changed || !strings.Contains(out, "note changed: design.md") {
		t.Fatalf("expected note-changed report, got %q", out)
	}
	// Remove it.
	if err := os.Remove(filepath.Join(notesDir, "design.md")); err != nil {
		t.Fatal(err)
	}
	out, changed, _ = w.poll()
	if !changed || !strings.Contains(out, "note removed: design.md") {
		t.Fatalf("expected note-removed report, got %q", out)
	}
}

func TestDiffItemsEmptyWhenEqual(t *testing.T) {
	if d := diffItems("a\nb\n", "a\nb\n"); d != "" {
		t.Fatalf("expected empty diff, got %q", d)
	}
}

func TestNotesDirFor(t *testing.T) {
	if got := notesDirFor("/x/y/sess.md"); got != "/x/y/sess.notes" {
		t.Fatalf("got %q", got)
	}
}
