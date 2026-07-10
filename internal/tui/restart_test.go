package tui

import (
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// fakeInfo is a minimal fs.FileInfo carrying just the size and mtime the binary
// change-detector reads.
type fakeInfo struct {
	size  int64
	mtime time.Time
}

func (f fakeInfo) Name() string       { return "session-notes" }
func (f fakeInfo) Size() int64        { return f.size }
func (f fakeInfo) Mode() fs.FileMode  { return 0o755 }
func (f fakeInfo) ModTime() time.Time { return f.mtime }
func (f fakeInfo) IsDir() bool        { return false }
func (f fakeInfo) Sys() any           { return nil }

// withStubbedBinary swaps exePathFn/binStatFn for the duration of the test.
func withStubbedBinary(t *testing.T, path string, stat func(string) (os.FileInfo, error)) {
	t.Helper()
	origPath, origStat := exePathFn, binStatFn
	exePathFn = func() (string, error) { return path, nil }
	binStatFn = stat
	t.Cleanup(func() { exePathFn, binStatFn = origPath, origStat })
}

func TestCheckBinaryFlagsOnChange(t *testing.T) {
	base := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	cur := fakeInfo{size: 100, mtime: base}
	withStubbedBinary(t, "/bin/sn", func(string) (os.FileInfo, error) { return cur, nil })

	m := &model{}
	m.baselineBinary()
	if !m.exeBase.ok {
		t.Fatal("baseline should have succeeded")
	}

	m.checkBinary()
	if m.binaryStale {
		t.Fatal("unchanged binary should not be stale")
	}

	// Same size, newer mtime -> stale.
	cur = fakeInfo{size: 100, mtime: base.Add(time.Minute)}
	m.checkBinary()
	if !m.binaryStale {
		t.Fatal("changed mtime should flag stale")
	}
}

func TestCheckBinaryFlagsOnSizeChange(t *testing.T) {
	base := time.Now()
	cur := fakeInfo{size: 100, mtime: base}
	withStubbedBinary(t, "/bin/sn", func(string) (os.FileInfo, error) { return cur, nil })
	m := &model{}
	m.baselineBinary()
	cur = fakeInfo{size: 200, mtime: base}
	m.checkBinary()
	if !m.binaryStale {
		t.Fatal("changed size should flag stale")
	}
}

// A stat failure mid-check (file deleted, not yet replaced) must not nag.
func TestCheckBinaryIgnoresDeletion(t *testing.T) {
	info := fakeInfo{size: 100, mtime: time.Now()}
	statErr := false
	withStubbedBinary(t, "/bin/sn", func(string) (os.FileInfo, error) {
		if statErr {
			return nil, os.ErrNotExist
		}
		return info, nil
	})
	m := &model{}
	m.baselineBinary()
	statErr = true
	m.checkBinary()
	if m.binaryStale {
		t.Fatal("a transient stat failure must not flag stale")
	}
}

// When we could not baseline the executable at startup, detection stays off.
func TestCheckBinaryNoBaselineNeverNags(t *testing.T) {
	withStubbedBinary(t, "/bin/sn", func(string) (os.FileInfo, error) { return nil, os.ErrNotExist })
	m := &model{}
	m.baselineBinary()
	if m.exeBase.ok {
		t.Fatal("baseline should have failed")
	}
	// Now stat starts working, but without a baseline we must not flip to stale.
	binStatFn = func(string) (os.FileInfo, error) { return fakeInfo{size: 1, mtime: time.Now()}, nil }
	m.checkBinary()
	if m.binaryStale {
		t.Fatal("no baseline means no nagging")
	}
}

func TestBinaryNotice(t *testing.T) {
	m := &model{}
	if m.binaryNotice() != "" {
		t.Fatal("no notice before stale")
	}
	m.binaryStale = true
	if !strings.Contains(m.binaryNotice(), "U restarts") {
		t.Fatalf("notice should mention the U restart, got %q", m.binaryNotice())
	}
}

// U produces the restart sentinel (and a Quit) only when the binary is stale.
func TestURestartDispatch(t *testing.T) {
	uKey := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("U")}
	m := newModel()
	// Not stale: U is a no-op.
	m.binaryStale = false
	if _, cmd := m.handleBoardKey(uKey); cmd != nil {
		t.Fatal("U with a fresh binary should not quit")
	}
	if m.restart {
		t.Fatal("U with a fresh binary should not set restart")
	}
	// Stale: U sets restart and returns a Quit command.
	m.binaryStale = true
	_, cmd := m.handleBoardKey(uKey)
	if !m.restart {
		t.Fatal("U on a stale binary should set restart")
	}
	if cmd == nil {
		t.Fatal("U on a stale binary should return tea.Quit")
	}
}
