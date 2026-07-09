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
