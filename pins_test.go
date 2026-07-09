package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nitsanavni/session-notes/internal/hooks"
)

// captureStdout runs fn with os.Stdout redirected to a pipe and returns whatever
// it printed.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = orig
	out, _ := io.ReadAll(r)
	return string(out)
}

func writePinBoard(t *testing.T, dir, name, session string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	src := "---\nsession: " + session + "\n---\n## Working Agreements\n- !pin keep tests green\n"
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatalf("write board: %v", err)
	}
	return p
}

// TestPinsAlwaysPrints checks `pins --board` prints the shared block, and a board
// with no pins prints nothing (both exit 0).
func TestPinsAlwaysPrints(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SESSION_NOTES_DIR", dir)
	p := writePinBoard(t, dir, "b.md", "s1")

	var code int
	out := captureStdout(t, func() { code = runPins([]string{"--board", p}) })
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	want := "Pinned board items (reinjected on cadence):\n- keep tests green\n"
	if out != want {
		t.Errorf("out = %q, want %q", out, want)
	}

	// No-pins board: silent, exit 0.
	np := filepath.Join(dir, "np.md")
	os.WriteFile(np, []byte("## Plan\n- [ ] todo\n"), 0o644)
	out = captureStdout(t, func() { code = runPins([]string{"--board", np}) })
	if code != 0 || out != "" {
		t.Errorf("no-pins pins = (%q, %d), want empty/0", out, code)
	}
}

// TestPinsDueSharesHookState verifies the hook and `pins --due` observe one
// cadence via the shared .pins state file: after one fires, the other is silent
// until the content changes or the cadence elapses — in both orderings.
func TestPinsDueSharesHookState(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SESSION_NOTES_DIR", dir)

	// Ordering A: `pins --due` fires first, then a hook-style PinsDue is silent.
	pA := writePinBoard(t, dir, "a.md", "sA")
	var code int
	out := captureStdout(t, func() { code = runPins([]string{"--due", "--board", pA}) })
	if code != 0 || out == "" {
		t.Fatalf("first pins --due = (%q,%d), want it to fire", out, code)
	}
	// The hook shares hooks.PinsStatePath("sA"); same content, fresh -> not due.
	if hooks.PinsDue(hooks.PinsStatePath("sA"), []string{"keep tests green"}, hooks.DefaultPinCadence, time.Now()) {
		t.Error("hook fired again after pins --due already injected (state not shared)")
	}

	// Ordering B: the hook fires first, then `pins --due` is silent.
	pB := writePinBoard(t, dir, "b.md", "sB")
	if !hooks.PinsDue(hooks.PinsStatePath("sB"), []string{"keep tests green"}, hooks.DefaultPinCadence, time.Now()) {
		t.Fatal("hook's first injection should be due")
	}
	out = captureStdout(t, func() { code = runPins([]string{"--due", "--board", pB}) })
	if code != 0 || out != "" {
		t.Errorf("pins --due after hook fired = (%q,%d), want silent/0", out, code)
	}
}
