package main

import (
	"fmt"
	"os"
	"time"

	"github.com/nitsanavni/session-notes/internal/board"
	"github.com/nitsanavni/session-notes/internal/hooks"
)

// runPins implements `session-notes pins`: surface a board's pinned items outside
// the prompt-submit path (the user works board-first, so most Claude turns come
// from a file-watcher monitor rather than a prompt). Two modes:
//
//	pins --board <p> | --session <id>          always print the pinned block (exit
//	                                           0; silent when there are no pins)
//	pins --due --board <p> | --session <id>    print ONLY when due per the same
//	                                           changed-or-stale rule the hook uses,
//	                                           updating the shared cadence state
//
// Both exit 0 regardless; --due is silent when not due. --due shares
// hooks.PinsDue and the same .pins state file so a hook firing and a `pins --due`
// call observe one cadence (neither double-injects the other's window).
func runPins(args []string) int {
	var boardPath, session string
	due := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			fmt.Println("usage: session-notes pins [--due] --board <path> | --session <id>")
			return 0
		case "--board":
			if i+1 < len(args) {
				i++
				boardPath = args[i]
			}
		case "--session":
			if i+1 < len(args) {
				i++
				session = args[i]
			}
		case "--due":
			due = true
		default:
			fmt.Fprintf(os.Stderr, "pins: unknown argument: %s\n", args[i])
			fmt.Fprintln(os.Stderr, "usage: session-notes pins [--due] --board <path> | --session <id>")
			return 2
		}
	}

	path, err := editBoardPath(boardPath, session)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pins:", err)
		return 2
	}
	b, err := board.Load(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pins:", err)
		return 1
	}

	block, texts := hooks.PinsBlock(b)
	if len(texts) == 0 {
		return 0 // no pins: print nothing, exit 0.
	}

	if due {
		// Cadence state is keyed by session id (the hook's key). Prefer an
		// explicit --session, else the board's frontmatter session.
		sid := session
		if sid == "" {
			sid = b.Frontmatter.Session
		}
		if sid == "" {
			fmt.Fprintln(os.Stderr, "pins: --due needs a session id (pass --session or a board with a session: frontmatter key)")
			return 2
		}
		if !hooks.PinsDue(hooks.PinsStatePath(sid), texts, hooks.PinCadence(b), time.Now()) {
			return 0 // not due: silent.
		}
	}

	fmt.Print(block)
	return 0
}
