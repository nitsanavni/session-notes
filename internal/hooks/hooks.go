// Package hooks implements the Claude Code hook subcommands.
//
// Contract: hooks must be fast and must never fail the session. Every entry
// point swallows errors and returns nil (the caller exits 0). Output on stdout
// is injected into Claude's context, so only intentional text is printed.
package hooks

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/nitsanavni/session-notes/internal/board"
)

// hookInput is the subset of the Claude Code hook JSON we care about.
type hookInput struct {
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
}

func readInput(r io.Reader) (*hookInput, bool) {
	data, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil {
		return nil, false
	}
	var in hookInput
	if err := json.Unmarshal(data, &in); err != nil || in.SessionID == "" {
		return nil, false
	}
	return &in, true
}

// SessionStart handles the SessionStart hook: create the board if missing,
// record the tmux pane mapping, and print the protocol blurb for Claude.
func SessionStart(stdin io.Reader, stdout io.Writer) {
	in, ok := readInput(stdin)
	if !ok {
		return
	}
	now := time.Now()
	path := board.BoardPath(in.SessionID)

	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.MkdirAll(board.BoardsDir(), 0o755); err != nil {
			return
		}
		b := board.Parse(board.Template(in.SessionID, in.Cwd, now))
		if err := b.SaveTo(path); err != nil {
			return
		}
	}

	if pane := os.Getenv("TMUX_PANE"); pane != "" {
		_ = board.WritePaneMapping(pane, &board.PaneMapping{
			SessionID: in.SessionID,
			Cwd:       in.Cwd,
			Board:     path,
			Started:   now.Format(time.RFC3339),
		})
	}

	fmt.Fprintf(stdout, `Session board: %s

This session has a shared board (markdown) that you and the user both maintain:
- Early on, set a short "title:" in the board frontmatter describing the task (e.g. "auth refactor") so dashboards/pickers show a name instead of a uuid.
- Keep "Threads" and "Plan" up to date as you work (statuses: [ ] open, [>] in progress, [x] done, [?] blocked).
- Answer questions addressed @claude in "Questions"; raise your own with "- [ ] question @user".
- To answer or react to a specific item, append an indented sub-bullet reply under it, forum-style ("  - claude: text"), instead of rewriting the item's text inline. Replies nest 2 spaces per level.
- Discussions run FLAT: continue a back-and-forth as sibling bullets at the same level; nest deeper only to fork a sub-topic off one specific message.
- Append milestones to "Log" as "- HH:MM claude: text" (append-only).
- WRITE THE BOARD WITH THE CLI, not by editing the file yourself. "session-notes edit <sub> --board %s [--refresh-snapshot <snap>] args…" does the whole locked read → modify → atomic-replace for you (same lock the TUI uses), so your write never clobbers the user's concurrent edit. Subcommands:
    session-notes edit add <section> <text>       append a top-level item to a section
    session-notes edit reply <query> <text>       append an indented reply under the first item matching <query>
    session-notes edit status <query> <state>     set an item's checkbox (open|wip|done|blocked|none)
    session-notes edit log <text>                 append "- HH:MM claude: <text>" to Log
    session-notes edit title <text>               set the frontmatter title
    session-notes edit replace <old> <new>        exact first-occurrence string replace (raw escape hatch)
  Each write is silent on success and exits non-zero with a one-line reason on failure (missing section lists the sections; an unmatched query/old string says so).
- The user edits the board live in a TUI; consider watching the file with the Monitor tool. Have the watch emit the diff itself so events carry the change and you don't need to re-read the file, e.g.:
    cp %s snap; while true; do sleep 1; cmp -s %s snap && continue; diff -U0 snap %s | grep -E '^[+-]' | grep -vE '^(\+\+\+|---) '; cp %s snap; done
  To keep the watch from firing on YOUR OWN board edits, pass "--refresh-snapshot snap" to every "session-notes edit" call: it copies the new content over the snapshot inside the same lock, right after the atomic replace, so a watcher that also takes the lock around its compare-and-snapshot cycle only ever emits the user's edits.
- [[link]] wiki-links on the board come in two forms, both opened with the o key in the TUI:
  - [[name]] (a plain name, no slash) is a SIDE NOTE, resolved to "<boards-dir>/<session-id>.notes/name.md". For longer content (designs, scoping docs, research) write the file there and link it as [[name]]; opening a missing side note creates it.
  - [[path/with/slash.md]] (contains a slash, or starts with ~/ or /) is a FILE PATH, resolved relative to the session cwd (~ expanded, absolute paths as-is). Use this to link real repo files like [[docs/foo.md]]; opening a path that doesn't exist shows an error rather than creating a stub.
- Items marked "!!" are urgent and will be injected into your context automatically on the next user prompt.
- Preserve any content you don't understand; edit surgically.
- If you see <<<<<<< / ======= / >>>>>>> conflict markers on the board, a merge needs reconciling: integrate BOTH sides, NEVER delete the user's text, remove the markers, and write the result with "session-notes edit replace <old> <new>" (or several) so it happens under the lock.
- Why the CLI: the user edits this file concurrently, so writes must be serialized. "session-notes edit" takes an exclusive flock on the sidecar lock file "%s.lock" (NOT the board — the board is replaced by atomic rename each save), reads, modifies, writes a temp file, renames it over the board, and releases — the same lock the TUI uses, so your edits and the user's never clobber each other. Do NOT hand-roll this with your own flock script; use the CLI.
`, path, path, path, path, path, path, path)

	if prev := previousBoards(in.SessionID, in.Cwd, 3); len(prev) > 0 {
		fmt.Fprintf(stdout, "\nBoards from previous sessions in this project (read them for history/context if useful):\n")
		for _, p := range prev {
			fmt.Fprintf(stdout, "- %s\n", p)
		}
	}
}

// previousBoards returns up to max paths of other sessions' boards whose
// frontmatter cwd matches this session's cwd, newest first.
func previousBoards(sessionID, cwd string, max int) []string {
	if cwd == "" {
		return nil
	}
	entries, err := os.ReadDir(board.BoardsDir())
	if err != nil {
		return nil
	}
	type cand struct {
		path  string
		mtime time.Time
	}
	var cands []cand
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || len(name) < 4 || name[len(name)-3:] != ".md" || name == sessionID+".md" {
			continue
		}
		path := board.BoardsDir() + string(os.PathSeparator) + name
		b, err := board.Load(path)
		if err != nil || b.Frontmatter.Cwd != cwd {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		cands = append(cands, cand{path, fi.ModTime()})
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].mtime.After(cands[j].mtime) })
	out := make([]string, 0, max)
	for i := 0; i < len(cands) && i < max; i++ {
		out = append(out, cands[i].path)
	}
	return out
}

// PromptSubmit handles the UserPromptSubmit hook: surface unchecked urgent
// items and a notice when the board changed since Claude last saw it.
func PromptSubmit(stdin io.Reader, stdout io.Writer) {
	in, ok := readInput(stdin)
	if !ok {
		return
	}
	path := board.BoardPath(in.SessionID)
	fi, err := os.Stat(path)
	if err != nil {
		return
	}

	b, err := board.Load(path)
	if err != nil {
		return
	}
	if urgent := b.UrgentOpenItems(); len(urgent) > 0 {
		fmt.Fprintf(stdout, "Urgent items on the session board (%s):\n", path)
		for _, it := range urgent {
			fmt.Fprintf(stdout, "- !! %s\n", it.DisplayText())
		}
	}

	statePath := lastSeenPath(in.SessionID)
	if sfi, err := os.Stat(statePath); err != nil || fi.ModTime().After(sfi.ModTime()) {
		if err == nil { // state exists and board is newer => it changed
			fmt.Fprintf(stdout, "Note: the session board (%s) changed since you last saw it; re-read it.\n", path)
		}
	}
	touch(statePath)
}

// SessionEnd handles the SessionEnd hook: log the end and remove the pane
// mapping if it points at this session.
func SessionEnd(stdin io.Reader, stdout io.Writer) {
	in, ok := readInput(stdin)
	if !ok {
		return
	}
	path := board.BoardPath(in.SessionID)
	if b, err := board.Load(path); err == nil {
		b.AppendLog("end", "session ended")
		_ = b.Save()
	}
	// Remove any pane mapping pointing at this session (usually just the one
	// for $TMUX_PANE, but a scan is cheap and works without the env var).
	entries, err := os.ReadDir(board.PanesDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if len(name) < 6 || name[len(name)-5:] != ".json" {
			continue
		}
		paneID := name[:len(name)-5]
		if m, err := board.ReadPaneMapping(paneID); err == nil && m.SessionID == in.SessionID {
			_ = os.Remove(board.PaneMappingPath(paneID))
		}
	}
}

func lastSeenPath(sessionID string) string {
	return board.StateDir() + string(os.PathSeparator) + sessionID + ".last-seen"
}

func touch(path string) {
	_ = os.MkdirAll(board.StateDir(), 0o755)
	now := time.Now()
	if err := os.Chtimes(path, now, now); err != nil {
		_ = os.WriteFile(path, nil, 0o644)
	}
}
