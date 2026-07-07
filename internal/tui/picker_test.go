package tui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

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
