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
- Keep "Threads" and "Plan" up to date as you work (statuses: [ ] open, [>] in progress, [x] done, [?] blocked).
- Answer questions addressed @claude in "Questions"; raise your own with "- [ ] question @user".
- To answer or react to a specific item, append an indented sub-bullet reply under it, forum-style ("  - claude: text"), instead of rewriting the item's text inline. Replies nest 2 spaces per level; you can reply to a reply.
- Append milestones to "Log" as "- HH:MM claude: text" (append-only).
- The user edits the board live in a TUI; consider watching the file with the Monitor tool. Have the watch emit the diff itself so events carry the change and you don't need to re-read the file, e.g.:
    cp board snap; while true; do sleep 1; cmp -s board snap && continue; diff -U0 snap board | grep -E '^[+-]' | grep -vE '^(\+\+\+|---) '; cp board snap; done
- Items marked "!!" are urgent and will be injected into your context automatically on the next user prompt.
- Preserve any content you don't understand; edit surgically.
`, path)
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
