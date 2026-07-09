package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/nitsanavni/session-notes/internal/board"
)

// runEdit implements `session-notes edit <sub> [flags] args…`: the first-class
// locked write path an agent uses in place of an ad-hoc flock script. Every
// subcommand does its read-modify-write inside board.EditUnderLock (exclusive
// flock + atomic rename), optionally refreshing a monitor snapshot inside the
// same critical section. Success is silent (unix-quiet); any failure prints a
// one-line error and returns non-zero.
func runEdit(args []string) int {
	// Pull recognized flags out of the argument list; the remainder are the
	// subcommand's positional arguments. Flags may appear anywhere.
	var boardPath, session, snapshot, author string
	var pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		takeVal := func() (string, bool) {
			if i+1 < len(args) {
				i++
				return args[i], true
			}
			return "", false
		}
		switch a {
		case "--board":
			if v, ok := takeVal(); ok {
				boardPath = v
			} else {
				return editErr("--board needs a path")
			}
		case "--session":
			if v, ok := takeVal(); ok {
				session = v
			} else {
				return editErr("--session needs an id")
			}
		case "--refresh-snapshot":
			if v, ok := takeVal(); ok {
				snapshot = v
			} else {
				return editErr("--refresh-snapshot needs a path")
			}
		case "--as":
			if v, ok := takeVal(); ok {
				author = v
			} else {
				return editErr("--as needs an author")
			}
		default:
			pos = append(pos, a)
		}
	}

	if len(pos) == 0 {
		return editErr("usage: session-notes edit <add|reply|status|log|title|replace> [flags] args…")
	}
	sub := pos[0]
	rest := pos[1:]

	path, err := editBoardPath(boardPath, session)
	if err != nil {
		return editErr(err.Error())
	}

	switch sub {
	case "add":
		if len(rest) != 2 {
			return editErr("usage: session-notes edit add <section> <text>")
		}
		return editStructural(path, snapshot, func(b *board.Board) error {
			if b.Section(rest[0]) == nil {
				return fmt.Errorf("no section %q; sections: %s", rest[0], strings.Join(sectionTitles(b), ", "))
			}
			// A leading status marker in <text> ("[>] foo") sets the item's
			// status instead of ending up as literal text after the default one.
			text := rest[1]
			it := b.AddItem(rest[0], text)
			if st, remainder, ok := leadingStatus(text); ok {
				it.Status = st
				it.Text = remainder
			}
			return nil
		})
	case "reply":
		if len(rest) != 2 {
			return editErr("usage: session-notes edit reply <query> <text>")
		}
		return editStructural(path, snapshot, func(b *board.Board) error {
			it, n := b.FindByQuery(rest[0])
			if n == 0 {
				return fmt.Errorf("no item matching %q", rest[0])
			}
			if n > 1 {
				fmt.Fprintf(os.Stderr, "session-notes: %d items match %q; replying under the first\n", n, rest[0])
			}
			b.AddReply(it, rest[1])
			return nil
		})
	case "status":
		if len(rest) != 2 {
			return editErr("usage: session-notes edit status <query> <open|wip|done|blocked|none>")
		}
		st, ok := parseState(rest[1])
		if !ok {
			return editErr(fmt.Sprintf("unknown status %q; use open|wip|done|blocked|none", rest[1]))
		}
		return editStructural(path, snapshot, func(b *board.Board) error {
			it, n := b.FindByQuery(rest[0])
			if n == 0 {
				return fmt.Errorf("no item matching %q", rest[0])
			}
			if n > 1 {
				fmt.Fprintf(os.Stderr, "session-notes: %d items match %q; updating the first\n", n, rest[0])
			}
			it.SetStatus(st)
			return nil
		})
	case "log":
		if len(rest) != 1 {
			return editErr("usage: session-notes edit log <text>")
		}
		if author == "" {
			author = "claude"
		}
		return editStructural(path, snapshot, func(b *board.Board) error {
			b.AppendLog(author, rest[0])
			return nil
		})
	case "title":
		if len(rest) != 1 {
			return editErr("usage: session-notes edit title <text>")
		}
		return editStructural(path, snapshot, func(b *board.Board) error {
			b.SetTitle(rest[0])
			return nil
		})
	case "replace":
		if len(rest) != 2 {
			return editErr("usage: session-notes edit replace <old> <new>")
		}
		err := board.EditUnderLock(path, snapshot, func(content string) (string, error) {
			if !strings.Contains(content, rest[0]) {
				return "", fmt.Errorf("string not found: %q", rest[0])
			}
			return strings.Replace(content, rest[0], rest[1], 1), nil
		})
		if err != nil {
			return editErr(err.Error())
		}
		return 0
	default:
		return editErr(fmt.Sprintf("unknown edit subcommand %q", sub))
	}
}

// editStructural runs a parse → mutate → render write of the board at path under
// the lock, refreshing snapshot when set. mutate operates on the freshly-parsed
// board; returning an error aborts the write.
func editStructural(path, snapshot string, mutate func(*board.Board) error) int {
	err := board.EditUnderLock(path, snapshot, func(content string) (string, error) {
		b := board.Parse(content)
		b.Path = path
		if err := mutate(b); err != nil {
			return "", err
		}
		return b.Render(), nil
	})
	if err != nil {
		return editErr(err.Error())
	}
	return 0
}

// editBoardPath resolves the target board from --board (explicit path, primary
// for agents) or --session (session id → boards-dir path). Exactly one must be
// given.
func editBoardPath(boardPath, session string) (string, error) {
	switch {
	case boardPath != "" && session != "":
		return "", fmt.Errorf("give --board or --session, not both")
	case boardPath != "":
		if _, err := os.Stat(boardPath); err != nil {
			return "", fmt.Errorf("board not found: %s", boardPath)
		}
		return boardPath, nil
	case session != "":
		p := board.BoardPath(session)
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("no board for session %s (%s)", session, p)
		}
		return p, nil
	default:
		return "", fmt.Errorf("a board is required: pass --board <path> or --session <id>")
	}
}

func parseState(s string) (board.Status, bool) {
	switch s {
	case "open":
		return board.StatusOpen, true
	case "wip":
		return board.StatusInProgress, true
	case "done":
		return board.StatusDone, true
	case "blocked":
		return board.StatusBlocked, true
	case "none":
		return board.StatusNone, true
	}
	return board.StatusNone, false
}

func sectionTitles(b *board.Board) []string {
	out := make([]string, 0, len(b.Sections))
	for _, s := range b.Sections {
		out = append(out, s.Title)
	}
	return out
}

func editErr(msg string) int {
	fmt.Fprintln(os.Stderr, "session-notes:", msg)
	return 2
}

// leadingStatus parses an explicit "[ ]/[>]/[x]/[?]" marker at the start of an
// add's text, returning the status and the text without it.
func leadingStatus(text string) (board.Status, string, bool) {
	if len(text) < 4 || text[0] != '[' || text[2] != ']' || text[3] != ' ' {
		return 0, "", false
	}
	var st board.Status
	switch text[1] {
	case ' ':
		st = board.StatusOpen
	case '>':
		st = board.StatusInProgress
	case 'x', 'X':
		st = board.StatusDone
	case '?':
		st = board.StatusBlocked
	default:
		return 0, "", false
	}
	return st, text[4:], true
}
