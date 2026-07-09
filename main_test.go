package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nitsanavni/session-notes/internal/board"
)

// writeBoard drops a minimal board file (with the given cwd frontmatter) into
// dir and stamps its mtime so newest-first ordering is deterministic.
func writeBoard(t *testing.T, dir, name, cwd string, mtime time.Time) string {
	t.Helper()
	p := filepath.Join(dir, name)
	src := "---\ncwd: " + cwd + "\n---\n## Plan\n"
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatalf("write board: %v", err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	return p
}

// TestResolvePaneFallbackByCwd asserts that a pane with no mapping resolves to
// the newest board whose frontmatter cwd matches the pane's current path,
// instead of failing over to the picker. Uses a temp boards dir so it never
// touches ~/.claude/boards.
func TestResolvePaneFallbackByCwd(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SESSION_NOTES_DIR", dir)
	t.Setenv("TMUX_PANE", "")

	proj := "/tmp/some/project"
	old := writeBoard(t, dir, "old.md", proj, time.Now().Add(-time.Hour))
	newer := writeBoard(t, dir, "new.md", proj, time.Now())
	_ = old

	// Pane %9 has no mapping file; its current path is the project dir.
	orig := paneCurrentPath
	paneCurrentPath = func(id string) string {
		if id == "%9" {
			return proj
		}
		return ""
	}
	defer func() { paneCurrentPath = orig }()

	path, ok := resolveBoard("", "%9")
	if !ok {
		t.Fatalf("resolveBoard fell through to the picker, want a cwd fallback")
	}
	if path != newer {
		t.Errorf("resolved %q, want the newest matching board %q", path, newer)
	}
}

// TestResolvePaneFallbackNoMatchPicker asserts that when the pane's cwd matches
// no board, resolution falls through to the picker (ok=false) rather than
// picking an unrelated board.
func TestResolvePaneFallbackNoMatchPicker(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SESSION_NOTES_DIR", dir)
	t.Setenv("TMUX_PANE", "")

	writeBoard(t, dir, "other.md", "/tmp/unrelated", time.Now())

	orig := paneCurrentPath
	paneCurrentPath = func(string) string { return "/tmp/no/board/here" }
	defer func() { paneCurrentPath = orig }()

	if _, ok := resolveBoard("", "%9"); ok {
		t.Errorf("resolveBoard resolved a board, want picker fallback (ok=false)")
	}
}

// TestResolvePaneMappingWins asserts an existing, valid pane mapping still takes
// precedence over the cwd fallback.
func TestResolvePaneMappingWins(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SESSION_NOTES_DIR", dir)
	t.Setenv("TMUX_PANE", "")

	mapped := writeBoard(t, dir, "mapped.md", "/tmp/proj", time.Now().Add(-time.Hour))
	writeBoard(t, dir, "newer.md", "/tmp/proj", time.Now())

	if err := board.WritePaneMapping("%9", &board.PaneMapping{Board: mapped}); err != nil {
		t.Fatalf("write pane mapping: %v", err)
	}
	orig := paneCurrentPath
	paneCurrentPath = func(string) string { return "/tmp/proj" }
	defer func() { paneCurrentPath = orig }()

	path, ok := resolveBoard("", "%9")
	if !ok || path != mapped {
		t.Errorf("resolveBoard = %q,%v; want the mapped board %q", path, ok, mapped)
	}
}
