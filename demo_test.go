package main

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// startDemo seeds a throwaway board, serves it on a free port, and reports a
// subtree root to suggest. The served board page answers 200 and stop() cleans
// up the temp dir.
func TestDemoStartsServesAndTearsDown(t *testing.T) {
	inst, err := startDemo("127.0.0.1:0")
	if err != nil {
		t.Fatalf("startDemo: %v", err)
	}

	// The seeded board must exist, live outside ~/.claude/boards, and carry a
	// subtree root (an item with children) for the carve-out suggestion.
	if _, err := os.Stat(inst.boardPath); err != nil {
		t.Fatalf("board not seeded: %v", err)
	}
	if strings.Contains(inst.boardPath, ".claude/boards") {
		t.Errorf("demo board should be a temp file, not in ~/.claude/boards: %s", inst.boardPath)
	}
	if inst.rootID == "" {
		t.Errorf("expected a subtree root id to suggest")
	}

	// The board page answers 200 within a short budget.
	var status int
	var body string
	for i := 0; i < 100; i++ {
		resp, err := http.Get(inst.url)
		if err == nil {
			status = resp.StatusCode
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			body = string(b)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status != 200 {
		t.Fatalf("GET %s = %d, want 200", inst.url, status)
	}
	if !strings.Contains(body, "<") {
		t.Errorf("board page looks empty")
	}

	// stop() closes the server and removes the temp board.
	inst.stop()
	if _, err := os.Stat(inst.boardPath); !os.IsNotExist(err) {
		t.Errorf("stop() should remove the temp board, stat err = %v", err)
	}
	// The server is closed: the port no longer answers.
	if _, err := http.Get(inst.url); err == nil {
		t.Errorf("server should be closed after stop()")
	}
}

// runDemo --help exits 0 without starting anything.
func TestDemoHelp(t *testing.T) {
	if code := runDemo([]string{"--help"}); code != 0 {
		t.Errorf("demo --help exit = %d, want 0", code)
	}
}
