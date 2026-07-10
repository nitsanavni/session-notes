package tui

import (
	"strings"
	"testing"

	"github.com/nitsanavni/session-notes/internal/board"
)

const statusBoard = `---
session: footsess
cwd: /tmp/x
---

## Plan

- [ ] do a thing
`

func TestStatusFooterRendersFreshSidecar(t *testing.T) {
	t.Setenv("SESSION_NOTES_DIR", t.TempDir())
	board.UpdateStatus("footsess", func(s *board.SessionStatus) {
		s.Model = "Opus"
		s.ContextPct = 62
		s.Activity = "working"
	})

	m := newTestModel(statusBoard, 120, 24)
	seg := stripANSI(m.statusFooter())
	if !strings.Contains(seg, "Opus") {
		t.Fatalf("footer missing model: %q", seg)
	}
	if !strings.Contains(seg, "62%") {
		t.Fatalf("footer missing context pct: %q", seg)
	}
	if !strings.Contains(seg, "working") {
		t.Fatalf("footer missing activity: %q", seg)
	}
	// The bar should be partially filled at 62% (3 of 5 cells).
	if !strings.Contains(seg, "▰") || !strings.Contains(seg, "▱") {
		t.Fatalf("footer missing progress bar: %q", seg)
	}
	// And it must appear in the full View footer.
	if !strings.Contains(stripANSI(m.View()), "Opus") {
		t.Fatal("View() should include the status footer")
	}
}

func TestStatusFooterHiddenWhenNoSidecar(t *testing.T) {
	t.Setenv("SESSION_NOTES_DIR", t.TempDir())
	m := newTestModel(statusBoard, 120, 24)
	if seg := m.statusFooter(); seg != "" {
		t.Fatalf("expected empty footer without sidecar, got %q", seg)
	}
}

func TestStatusFooterHiddenWhenNoSession(t *testing.T) {
	t.Setenv("SESSION_NOTES_DIR", t.TempDir())
	m := newTestModel("## Plan\n\n- [ ] x\n", 120, 24)
	if seg := m.statusFooter(); seg != "" {
		t.Fatalf("expected empty footer without session id, got %q", seg)
	}
}

func TestContextBarClamps(t *testing.T) {
	cases := map[float64]string{0: "▱▱▱▱▱", 100: "▰▰▰▰▰", 40: "▰▰▱▱▱", 50: "▰▰▰▱▱", -5: "▱▱▱▱▱", 200: "▰▰▰▰▰"}
	for pct, want := range cases {
		if got := contextBar(pct); got != want {
			t.Errorf("contextBar(%v) = %q, want %q", pct, got, want)
		}
	}
}
