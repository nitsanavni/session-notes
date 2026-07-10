package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/nitsanavni/session-notes/internal/board"
)

// runHistory implements `session-notes history [--board <p> | --session <id>]
// [-n N]`: print the board's journaled edits as compact line-diffs, oldest
// first — the catch-up command. An agent whose context was compacted (or a
// human returning to a session) reads the tail of this instead of diffing the
// whole board by memory. Read-only; exits 0 with no output when the journal
// is empty.
func runHistory(args []string) int {
	var boardPath, session string
	limit := 20
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			fmt.Println("usage: session-notes history [--board <path> | --session <id>] [-n N]")
			return 0
		case "--board":
			if i+1 >= len(args) {
				return editErr("--board needs a path")
			}
			i++
			boardPath = args[i]
		case "--session":
			if i+1 >= len(args) {
				return editErr("--session needs an id")
			}
			i++
			session = args[i]
		case "-n":
			if i+1 >= len(args) {
				return editErr("-n needs a count")
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 {
				return editErr("-n needs a positive count")
			}
			limit = n
		default:
			return editErr(fmt.Sprintf("history: unknown argument: %s", args[i]))
		}
	}
	path, err := editBoardPath(boardPath, session)
	if err != nil {
		return editErr(err.Error())
	}

	entries := board.History(path)
	if len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	for _, e := range entries {
		stamp := e.Time
		if t, terr := time.Parse(time.RFC3339, e.Time); terr == nil {
			stamp = t.Format("15:04")
		}
		fmt.Fprintf(os.Stdout, "%s %s\n", stamp, e.Author)
		added, removed := board.DiffLines(e.Before, e.After)
		for _, l := range removed {
			fmt.Fprintf(os.Stdout, "  - %s\n", l)
		}
		for _, l := range added {
			fmt.Fprintf(os.Stdout, "  + %s\n", l)
		}
	}
	return 0
}
