package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nitsanavni/session-notes/internal/board"
)

// sampleBoard is a small board used across the edit tests. It has the canonical
// sections plus a couple of items to target by query.
const sampleBoard = `---
session: s1
cwd: /tmp/proj
---
## Plan
- [ ] build the widget
- [>] wire the frobnicator

## Questions
- [ ] should we ship @claude

## Log
- 09:00 start: session started
`

func writeSample(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "board.md")
	if err := os.WriteFile(p, []byte(sampleBoard), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

func read(t *testing.T, p string) string {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(data)
}

func TestEditSubcommands(t *testing.T) {
	tests := []struct {
		name     string
		args     []string // after "edit", before --board
		wantCode int
		contains []string // substrings expected in the resulting file
		absent   []string // substrings that must NOT be in the file
	}{
		{
			name:     "add appends item to section",
			args:     []string{"add", "Plan", "polish the docs"},
			contains: []string{"- [ ] polish the docs"},
		},
		{
			name:     "reply nests under first match",
			args:     []string{"reply", "frobnicator", "claude: done"},
			contains: []string{"  - claude: done"},
		},
		{
			name:     "status sets checkbox",
			args:     []string{"status", "build the widget", "done"},
			contains: []string{"- [x] build the widget"},
			absent:   []string{"- [ ] build the widget"},
		},
		{
			name:     "status none clears checkbox",
			args:     []string{"status", "build the widget", "none"},
			contains: []string{"- build the widget"},
		},
		{
			name:     "log appends claude line",
			args:     []string{"log", "shipped it"},
			contains: []string{"claude: shipped it"},
		},
		{
			name:     "log honours --as",
			args:     []string{"log", "--as", "bot", "hi"},
			contains: []string{"bot: hi"},
		},
		{
			name:     "title sets frontmatter",
			args:     []string{"title", "widget work"},
			contains: []string{"title: widget work"},
		},
		{
			name:     "replace swaps first occurrence",
			args:     []string{"replace", "wire the frobnicator", "rewire it"},
			contains: []string{"- [>] rewire it"},
			absent:   []string{"frobnicator"},
		},
		{
			name:     "add missing section errors and lists sections",
			args:     []string{"add", "Nope", "x"},
			wantCode: 2,
			absent:   []string{"Nope"},
		},
		{
			name:     "reply no match errors",
			args:     []string{"reply", "no-such-item", "hi"},
			wantCode: 2,
		},
		{
			name:     "replace missing string errors",
			args:     []string{"replace", "not present", "x"},
			wantCode: 2,
		},
		{
			name:     "status bad state errors",
			args:     []string{"status", "build the widget", "sideways"},
			wantCode: 2,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := writeSample(t)
			args := append(append([]string{}, tc.args...), "--board", p)
			if code := runEdit(args); code != tc.wantCode {
				t.Fatalf("runEdit code=%d want %d", code, tc.wantCode)
			}
			got := read(t, p)
			for _, s := range tc.contains {
				if !strings.Contains(got, s) {
					t.Errorf("result missing %q\n---\n%s", s, got)
				}
			}
			for _, s := range tc.absent {
				if strings.Contains(got, s) {
					t.Errorf("result unexpectedly contains %q\n---\n%s", s, got)
				}
			}
		})
	}
}

// TestEditResolveBySession checks --session resolves via the boards dir.
func TestEditUndoRedo(t *testing.T) {
	p := writeSample(t)

	if code := runEdit([]string{"add", "Plan", "revert me", "--board", p}); code != 0 {
		t.Fatalf("add: exit %d", code)
	}
	if !strings.Contains(read(t, p), "revert me") {
		t.Fatal("add missing")
	}
	if code := runEdit([]string{"undo", "--board", p}); code != 0 {
		t.Fatal("undo failed")
	}
	if strings.Contains(read(t, p), "revert me") {
		t.Fatal("undo did not remove the add")
	}
	if code := runEdit([]string{"redo", "--board", p}); code != 0 {
		t.Fatal("redo failed")
	}
	if !strings.Contains(read(t, p), "revert me") {
		t.Fatal("redo did not restore the add")
	}

	// The journal is shared: an entry recorded through the board package
	// directly (as the web server records) is undoable from the CLI.
	if err := board.EditUnderLockJournaled(p, "", "web", func(c string) (string, error) {
		return strings.Replace(c, "revert me", "web edit", 1), nil
	}); err != nil {
		t.Fatal(err)
	}
	if code := runEdit([]string{"undo", "--board", p}); code != 0 {
		t.Fatal("undo of web edit failed")
	}
	if !strings.Contains(read(t, p), "revert me") {
		t.Fatal("CLI undo did not revert the web edit")
	}

	// An unjournaled write (TUI-style) makes undo refuse, not clobber.
	if err := os.WriteFile(p, []byte(read(t, p)+"- external\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := runEdit([]string{"undo", "--board", p}); code == 0 {
		t.Fatal("undo over external write must fail")
	}
	if !strings.Contains(read(t, p), "external") {
		t.Fatal("external write clobbered")
	}
}

func TestHistoryCommand(t *testing.T) {
	p := writeSample(t)
	if code := runEdit([]string{"add", "Plan", "step one", "--board", p}); code != 0 {
		t.Fatal("add 1")
	}
	if code := runEdit([]string{"status", "step one", "done", "--board", p}); code != 0 {
		t.Fatal("status")
	}

	out := captureStdout(t, func() {
		if code := runHistory([]string{"--board", p}); code != 0 {
			t.Fatal("history failed")
		}
	})
	if !strings.Contains(out, "claude") ||
		!strings.Contains(out, "+ - [ ] step one") ||
		!strings.Contains(out, "- - [ ] step one") ||
		!strings.Contains(out, "+ - [x] step one") {
		t.Fatalf("history output:\n%s", out)
	}

	// -n limits to the tail.
	out = captureStdout(t, func() { runHistory([]string{"-n", "1", "--board", p}) })
	if strings.Contains(out, "+ - [ ] step one") || !strings.Contains(out, "+ - [x] step one") {
		t.Fatalf("history -n 1 output:\n%s", out)
	}
}

func TestEditResolveBySession(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SESSION_NOTES_DIR", dir)
	p := board.BoardPath("sess-x")
	if err := os.WriteFile(p, []byte(sampleBoard), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if code := runEdit([]string{"log", "via session", "--session", "sess-x"}); code != 0 {
		t.Fatalf("runEdit code=%d want 0", code)
	}
	if !strings.Contains(read(t, p), "claude: via session") {
		t.Errorf("log line not written")
	}
}

// TestEditMissingBoard errors when neither flag is given, and when the board
// does not exist.
func TestEditMissingBoard(t *testing.T) {
	if code := runEdit([]string{"log", "x"}); code == 0 {
		t.Errorf("expected non-zero without a board flag")
	}
	if code := runEdit([]string{"log", "x", "--board", "/no/such/board.md"}); code == 0 {
		t.Errorf("expected non-zero for a missing board file")
	}
}

// TestEditRefreshSnapshot asserts the snapshot is refreshed with the new
// content inside the write, matching the board byte-for-byte.
func TestEditRefreshSnapshot(t *testing.T) {
	p := writeSample(t)
	snap := filepath.Join(filepath.Dir(p), "snap")
	if err := os.WriteFile(snap, []byte("stale"), 0o644); err != nil {
		t.Fatalf("write snap: %v", err)
	}
	if code := runEdit([]string{"add", "Plan", "snap item", "--board", p, "--refresh-snapshot", snap}); code != 0 {
		t.Fatalf("runEdit code=%d want 0", code)
	}
	if read(t, snap) != read(t, p) {
		t.Errorf("snapshot not refreshed to match board")
	}
	if !strings.Contains(read(t, snap), "snap item") {
		t.Errorf("snapshot missing the new item")
	}
}

// TestEditDeliversPendingExternalChange reproduces the monitor swallow race:
// the watcher snapshots the board, the user edits (external write, snapshot now
// stale), and the agent writes with --refresh-snapshot before any watch poll
// runs. The edit must print the user's not-yet-delivered change as a
// watch-style diff instead of silently snapshotting it as seen.
func TestEditDeliversPendingExternalChange(t *testing.T) {
	p := writeSample(t)
	snap := filepath.Join(filepath.Dir(p), "snap")
	if err := os.WriteFile(snap, []byte(read(t, p)), 0o644); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}
	// External (user) edit lands while no watch poll is armed.
	if err := board.EditUnderLock(p, "", func(c string) (string, error) {
		return strings.Replace(c, "## Questions", "- [ ] user slipped this in\n\n## Questions", 1), nil
	}); err != nil {
		t.Fatalf("external edit: %v", err)
	}
	out := captureStdout(t, func() {
		if code := runEdit([]string{"reply", "widget", "claude: on it", "--board", p, "--refresh-snapshot", snap}); code != 0 {
			t.Fatal("reply failed")
		}
	})
	if !strings.Contains(out, "+- [ ] user slipped this in") {
		t.Fatalf("undelivered user edit not printed; stdout:\n%s", out)
	}
	// The edit's own line must NOT be reported — it is a self-edit.
	if strings.Contains(out, "on it") {
		t.Fatalf("self-edit leaked into the delivery diff:\n%s", out)
	}
	// The snapshot ends up current, so the next watch poll stays quiet.
	if read(t, snap) != read(t, p) {
		t.Errorf("snapshot not refreshed to the post-edit board")
	}
}

// TestEditConcurrentWritersSerialize runs many concurrent adds against one
// board; every add must survive (the lock prevents lost updates from
// read-modify-write races).
func TestEditConcurrentWritersSerialize(t *testing.T) {
	p := writeSample(t)
	const n = 20
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			txt := "concurrent-" + string(rune('A'+i))
			if code := runEdit([]string{"add", "Plan", txt, "--board", p}); code != 0 {
				t.Errorf("add %d failed with code %d", i, code)
			}
		}(i)
	}
	wg.Wait()
	got := read(t, p)
	for i := 0; i < n; i++ {
		txt := "concurrent-" + string(rune('A'+i))
		if !strings.Contains(got, txt) {
			t.Errorf("lost update: %q missing (lock not serializing)", txt)
		}
	}
}

func TestEditAddLeadingStatusMarker(t *testing.T) {
	path := writeSample(t)
	if code := runEdit([]string{"add", "Plan", "[>] already in progress", "--board", path}); code != 0 {
		t.Fatalf("add exited %d", code)
	}
	data := read(t, path)
	if !strings.Contains(data, "- [>] already in progress") {
		t.Fatalf("leading marker not honored:\n%s", data)
	}
	if strings.Contains(data, "[ ] [>]") {
		t.Fatalf("double marker present:\n%s", data)
	}
}
