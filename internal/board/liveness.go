package board

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ProjectsDir returns the root under which Claude Code stores per-session
// transcripts: ~/.claude/projects. Overridable via SESSION_NOTES_PROJECTS_DIR
// so tests (and unusual setups) can point liveness elsewhere.
func ProjectsDir() string {
	if d := os.Getenv("SESSION_NOTES_PROJECTS_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".claude", "projects")
}

// mungeCwd converts a session cwd into the directory name Claude Code uses under
// its projects root. Verified empirically against a real ~/.claude/projects and
// against Claude Code 2.1.201's own sanitizer: every character that is not
// [a-zA-Z0-9] is replaced with '-'. Examples from a live install:
//
//	/home/nitsan/code/session-notes -> -home-nitsan-code-session-notes
//	/home/nitsan/code/mm-tui        -> -home-nitsan-code-mm-tui   (existing '-' kept)
//	/home/nitsan/code/foo.bar       -> -home-nitsan-code-foo-bar  ('.' -> '-')
//
// Note: Claude Code additionally truncates sanitized names longer than 200
// characters and appends a hash. That only affects pathologically deep cwds and
// is not reproduced here (the hash is an internal detail); real project paths
// are far shorter.
func mungeCwd(cwd string) string {
	var b strings.Builder
	b.Grow(len(cwd))
	for _, r := range cwd {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

// TranscriptPath returns the path to a Claude Code session transcript:
// <projects>/<munged-cwd>/<session-id>.jsonl.
func TranscriptPath(cwd, sessionID string) string {
	return filepath.Join(ProjectsDir(), mungeCwd(cwd), sessionID+".jsonl")
}

// Liveness returns the modification time of a session's transcript, which
// advances every time the session appends activity. It returns the zero time if
// the transcript does not exist (or cannot be stat'd) — i.e. no known activity.
// Callers classify the age (active/idle/gone); this only reports the raw mtime.
func Liveness(cwd, sessionID string) time.Time {
	fi, err := os.Stat(TranscriptPath(cwd, sessionID))
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}
