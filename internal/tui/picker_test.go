package tui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/nitsanavni/session-notes/internal/board"
)

// writeTestBoard creates a board file for a session with the given cwd.
func writeTestBoard(t *testing.T, session, cwd string) string {
	t.Helper()
	path := board.BoardPath(session)
	if err := os.WriteFile(path, []byte(board.Template(session, cwd, time.Now())), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// touchTranscript creates a session transcript with the given mtime so
// board.Liveness sees activity at that time.
func touchTranscript(t *testing.T, cwd, session string, mtime time.Time) {
	t.Helper()
	p := board.TranscriptPath(cwd, session)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func TestFindBoardByCwd(t *testing.T) {
	const cwd = "/proj/x"
	now := time.Now()

	setup := func(t *testing.T) {
		t.Setenv("SESSION_NOTES_DIR", t.TempDir())
		t.Setenv("SESSION_NOTES_PROJECTS_DIR", t.TempDir())
	}

	t.Run("single match resolves without any transcript", func(t *testing.T) {
		setup(t)
		want := writeTestBoard(t, "s1", cwd)
		if got, ok := FindBoardByCwd(cwd); !ok || got != want {
			t.Fatalf("got %q, %v; want %q, true", got, ok, want)
		}
	})

	t.Run("several matches narrow to the one live session", func(t *testing.T) {
		setup(t)
		writeTestBoard(t, "s1", cwd)
		want := writeTestBoard(t, "s2", cwd)
		writeTestBoard(t, "s3", cwd)
		touchTranscript(t, cwd, "s1", now.Add(-3*time.Hour)) // past idleWindow: dead
		touchTranscript(t, cwd, "s2", now.Add(-time.Minute)) // live
		// s3 has no transcript at all: dead
		if got, ok := FindBoardByCwd(cwd); !ok || got != want {
			t.Fatalf("got %q, %v; want %q, true", got, ok, want)
		}
	})

	t.Run("several live matches stay ambiguous", func(t *testing.T) {
		setup(t)
		writeTestBoard(t, "s1", cwd)
		writeTestBoard(t, "s2", cwd)
		touchTranscript(t, cwd, "s1", now.Add(-time.Minute))
		touchTranscript(t, cwd, "s2", now.Add(-time.Minute))
		if got, ok := FindBoardByCwd(cwd); ok {
			t.Fatalf("got %q, true; want ambiguous", got)
		}
	})

	t.Run("several matches none live stay ambiguous", func(t *testing.T) {
		setup(t)
		writeTestBoard(t, "s1", cwd)
		writeTestBoard(t, "s2", cwd)
		if got, ok := FindBoardByCwd(cwd); ok {
			t.Fatalf("got %q, true; want ambiguous", got)
		}
	})

	t.Run("no match", func(t *testing.T) {
		setup(t)
		writeTestBoard(t, "s1", "/proj/other")
		if got, ok := FindBoardByCwd(cwd); ok {
			t.Fatalf("got %q, true; want none", got)
		}
	})
}

// TestBoardToPickerRoundTrip drives picker -> board -> B (back to picker) ->
// pick another board, asserting the transitions land in the right mode and
// that per-board state (undo history, board tree, path) does not leak across.
func TestBoardToPickerRoundTrip(t *testing.T) {
	t.Setenv("SESSION_NOTES_DIR", t.TempDir())
	t.Setenv("SESSION_NOTES_PROJECTS_DIR", t.TempDir())
	p1 := writeTestBoard(t, "s1", "/proj/a")
	p2 := writeTestBoard(t, "s2", "/proj/b")

	m := newModel()
	if err := m.openBoard(p2); err != nil {
		t.Fatal(err)
	}
	// Plant undo history on this board so we can assert it doesn't leak.
	m.hist.snapshot("stale content from the first board")

	keyB := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("B")}
	m.handleBoardKey(keyB)
	if m.mode != modePicker {
		t.Fatalf("after B: mode = %v, want modePicker", m.mode)
	}
	if m.board != nil || m.path != "" || m.watch != nil {
		t.Fatalf("after B: board state not cleared (board=%v path=%q watch=%v)", m.board, m.path, m.watch)
	}
	if len(m.entries) != 2 {
		t.Fatalf("after B: %d picker entries, want 2", len(m.entries))
	}
	if m.entries[m.pickerCur].path != p2 {
		t.Fatalf("after B: picker cursor on %q, want the board we came from %q", m.entries[m.pickerCur].path, p2)
	}

	// Pick the other board.
	for i, e := range m.entries {
		if e.path == p1 {
			m.pickerCur = i
		}
	}
	m.handlePickerKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != modeBoard || m.path != p1 {
		t.Fatalf("after enter: mode=%v path=%q, want modeBoard %q", m.mode, m.path, p1)
	}
	// The first board's undo history must not restore into this board.
	m.undo()
	if m.status != "nothing to undo" {
		t.Fatalf("undo leaked across boards: status = %q", m.status)
	}

	// And back to the picker once more.
	m.handleBoardKey(keyB)
	if m.mode != modePicker {
		t.Fatalf("second B: mode = %v, want modePicker", m.mode)
	}
	if m.entries[m.pickerCur].path != p1 {
		t.Fatalf("second B: picker cursor on %q, want %q", m.entries[m.pickerCur].path, p1)
	}
}

func TestShortID(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"5dca9400-75ea-4bc4-bb1b-fb6fb4aaabde", "5dca9400"},
		{"nodashesbutlong", "nodashes"},
		{"short", "short"},
	} {
		if got := shortID(tc.in); got != tc.want {
			t.Errorf("shortID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
