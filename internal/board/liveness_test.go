package board

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMungeCwd(t *testing.T) {
	cases := []struct{ in, want string }{
		// Verified empirically against a real ~/.claude/projects install:
		{"/home/nitsan/code/session-notes", "-home-nitsan-code-session-notes"},
		// An existing hyphen is a non-alnum char too, so it maps to '-' (a no-op
		// visually). Confirmed by the real -home-nitsan-code-mm-tui directory.
		{"/home/nitsan/code/mm-tui", "-home-nitsan-code-mm-tui"},
		// Dots become '-'.
		{"/home/nitsan/code/foo.bar", "-home-nitsan-code-foo-bar"},
		// Underscores become '-'.
		{"/home/a_b/c", "-home-a-b-c"},
		// Every non-alnum maps 1:1 (no collapsing of runs); a leading slash
		// yields a leading dash, as seen in the real directory names.
		{"/a//b", "-a--b"},
		{"relative/path", "relative-path"},
		{"", ""},
	}
	for _, c := range cases {
		if got := mungeCwd(c.in); got != c.want {
			t.Errorf("mungeCwd(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTranscriptPath(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SESSION_NOTES_PROJECTS_DIR", root)

	got := TranscriptPath("/home/nitsan/code/session-notes", "abc-123")
	want := filepath.Join(root, "-home-nitsan-code-session-notes", "abc-123.jsonl")
	if got != want {
		t.Fatalf("TranscriptPath = %q, want %q", got, want)
	}
}

func TestLiveness(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SESSION_NOTES_PROJECTS_DIR", root)

	cwd := "/home/nitsan/code/session-notes"
	sess := "sess-1"

	// Absent transcript -> zero time.
	if got := Liveness(cwd, sess); !got.IsZero() {
		t.Fatalf("Liveness(absent) = %v, want zero", got)
	}

	// Create the transcript and set a known mtime.
	p := TranscriptPath(cwd, sess)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	want := time.Now().Add(-90 * time.Second).Truncate(time.Second)
	if err := os.Chtimes(p, want, want); err != nil {
		t.Fatal(err)
	}
	got := Liveness(cwd, sess)
	if !got.Truncate(time.Second).Equal(want) {
		t.Fatalf("Liveness = %v, want %v", got, want)
	}
}
