package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// init seeds a fresh board with the canonical sections and prints its path.
func TestInitCreatesBoard(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "boards", "dev.md")
	if code := runInit([]string{"--board", p, "--cwd", "/tmp/proj", "--title", "hello"}); code != 0 {
		t.Fatalf("init exit = %d, want 0", code)
	}
	body, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("board not created: %v", err)
	}
	for _, want := range []string{"session: dev", "cwd: /tmp/proj", "title: hello", "## Plan", "## Log"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("board missing %q:\n%s", want, body)
		}
	}
}

// A second init on the same path refuses instead of clobbering.
func TestInitRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "dev.md")
	if code := runInit([]string{"--board", p}); code != 0 {
		t.Fatalf("first init exit = %d, want 0", code)
	}
	before, _ := os.ReadFile(p)
	if code := runInit([]string{"--board", p, "--title", "clobber"}); code == 0 {
		t.Fatalf("second init exit = 0, want nonzero")
	}
	after, _ := os.ReadFile(p)
	if string(before) != string(after) {
		t.Errorf("second init modified the board")
	}
}

// Flags need values; a target is required.
func TestInitFlagErrors(t *testing.T) {
	if code := runInit(nil); code == 0 {
		t.Errorf("no target: exit = 0, want nonzero")
	}
	if code := runInit([]string{"--board"}); code == 0 {
		t.Errorf("dangling --board: exit = 0, want nonzero")
	}
	if code := runInit([]string{"--frob"}); code == 0 {
		t.Errorf("unknown flag: exit = 0, want nonzero")
	}
}
