package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/nitsanavni/session-notes/internal/board"
	"github.com/nitsanavni/session-notes/internal/cloud"
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
	var boardPath, session, snapshot, author, id, root string
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
		case "-h", "--help":
			fmt.Println("usage: session-notes edit <add|reply|fork|set|status|log|title|replace|undo|redo> [--board <path> | --session <id>] [--refresh-snapshot <path>] [--as <author>] [--id <id>] [--root <id>] args…")
			return 0
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
		case "--id":
			if v, ok := takeVal(); ok {
				id = v
			} else {
				return editErr("--id needs a node id")
			}
		case "--root":
			if v, ok := takeVal(); ok {
				root = v
			} else {
				return editErr("--root needs a node id")
			}
		default:
			pos = append(pos, a)
		}
	}

	if len(pos) == 0 {
		return editErr("usage: session-notes edit <add|reply|fork|set|status|log|title|replace|undo|redo> [flags] args…")
	}
	sub := pos[0]
	rest := pos[1:]

	var path string
	if cloud.IsRemote(boardPath) {
		// Remote board: --board is an http(s) URL. A #<node> fragment supplies a
		// default --root carve-out when --root/--id are not given, so
		// `edit reply --board https://host/b/x#<id> <text>` scopes to that node.
		ref, perr := cloud.ParseRef(boardPath)
		if perr != nil {
			return editErr(perr.Error())
		}
		path = boardPath
		if root == "" && ref.Node != "" {
			root = ref.Node
		}
		snapshot = "" // no self-edit snapshot for remote writes
	} else {
		var err error
		path, err = editBoardPath(boardPath, session)
		if err != nil {
			return editErr(err.Error())
		}
	}

	switch sub {
	case "add":
		// With --root the target defaults to the root node (section root → that
		// section; item root → a child of the item), so add takes just <text>.
		if root != "" {
			if len(rest) != 1 {
				return editErr("usage: session-notes edit add --root <id> <text>")
			}
			return editApplyOp(path, snapshot, root, board.Op{Name: "add", Text: rest[0]})
		}
		if len(rest) != 2 {
			return editErr("usage: session-notes edit add <section> <text>")
		}
		return editApplyOp(path, snapshot, root, board.Op{Name: "add", Section: rest[0], Text: rest[1]})
	case "reply":
		// In-thread semantics via the shared op layer: replying to a reply
		// continues the conversation flat; use fork to nest deliberately.
		// --id addresses the target node directly; otherwise a <query> substring.
		// With --root and no --id/<query>, the parent defaults to the root node.
		if id != "" {
			if len(rest) != 1 {
				return editErr("usage: session-notes edit reply --id <id> <text>")
			}
			return editApplyOp(path, snapshot, root, board.Op{Name: "reply", ID: id, Text: rest[0]})
		}
		if root != "" && len(rest) == 1 {
			return editApplyOp(path, snapshot, root, board.Op{Name: "reply", ID: root, Text: rest[0]})
		}
		if len(rest) != 2 {
			return editErr("usage: session-notes edit reply <query> <text>")
		}
		return editApplyOp(path, snapshot, root, board.Op{Name: "reply", Query: rest[0], Text: rest[1]})
	case "fork":
		if id != "" {
			if len(rest) != 1 {
				return editErr("usage: session-notes edit fork --id <id> <text>")
			}
			return editApplyOp(path, snapshot, root, board.Op{Name: "fork", ID: id, Text: rest[0]})
		}
		if root != "" && len(rest) == 1 {
			return editApplyOp(path, snapshot, root, board.Op{Name: "fork", ID: root, Text: rest[0]})
		}
		if len(rest) != 2 {
			return editErr("usage: session-notes edit fork <query> <text>")
		}
		return editApplyOp(path, snapshot, root, board.Op{Name: "fork", Query: rest[0], Text: rest[1]})
	case "status":
		if id != "" {
			if len(rest) != 1 {
				return editErr("usage: session-notes edit status --id <id> <open|wip|done|blocked|none>")
			}
			st, ok := parseState(rest[0])
			if !ok {
				return editErr(fmt.Sprintf("unknown status %q; use open|wip|done|blocked|none", rest[0]))
			}
			return editApplyOp(path, snapshot, root, board.Op{Name: "status", ID: id, Status: st})
		}
		if len(rest) != 2 {
			return editErr("usage: session-notes edit status <query> <open|wip|done|blocked|none>")
		}
		st, ok := parseState(rest[1])
		if !ok {
			return editErr(fmt.Sprintf("unknown status %q; use open|wip|done|blocked|none", rest[1]))
		}
		return editApplyOp(path, snapshot, root, board.Op{Name: "status", Query: rest[0], Status: st})
	case "set":
		// Replace an item's text, addressed by --id (the id-native "replace").
		if id == "" {
			return editErr("usage: session-notes edit set --id <id> <text>")
		}
		if len(rest) != 1 {
			return editErr("usage: session-notes edit set --id <id> <text>")
		}
		return editApplyOp(path, snapshot, root, board.Op{Name: "edit", ID: id, Text: rest[0]})
	case "log":
		if len(rest) != 1 {
			return editErr("usage: session-notes edit log <text>")
		}
		if author == "" {
			author = "claude"
		}
		return editApplyOp(path, snapshot, root, board.Op{Name: "log", Text: rest[0], Author: author})
	case "title":
		if len(rest) != 1 {
			return editErr("usage: session-notes edit title <text>")
		}
		return editApplyOp(path, snapshot, root, board.Op{Name: "title", Text: rest[0]})
	case "replace":
		if len(rest) != 2 {
			return editErr("usage: session-notes edit replace <old> <new>")
		}
		if cloud.IsRemote(path) {
			return remoteReplace(path, rest[0], rest[1])
		}
		err := board.EditUnderLockJournaled(path, snapshot, "claude", func(content string) (string, error) {
			deliverPending(snapshot, content)
			if !strings.Contains(content, rest[0]) {
				return "", fmt.Errorf("string not found: %q", rest[0])
			}
			return strings.Replace(content, rest[0], rest[1], 1), nil
		})
		if err != nil {
			return editErr(err.Error())
		}
		return 0
	case "undo", "redo":
		// Walk the board's shared undo journal — the same timeline the web
		// UI's u/ctrl+r use. Refuses (instead of clobbering) when the board
		// changed through a non-journaling writer since the journaled edit.
		if len(rest) != 0 {
			return editErr("usage: session-notes edit " + sub)
		}
		if cloud.IsRemote(path) {
			return editErr(sub + " is not supported for remote boards yet")
		}
		fn := board.Undo
		if sub == "redo" {
			fn = board.Redo
		}
		if err := fn(path); err != nil {
			return editErr(err.Error())
		}
		return 0
	default:
		return editErr(fmt.Sprintf("unknown edit subcommand %q", sub))
	}
}

// deliverPending prints any external change the monitor has not yet delivered:
// the snapshot vs the pre-edit board, diffed INSIDE the write lock, before this
// edit applies. Without it, --refresh-snapshot would copy the post-edit board
// over the snapshot and silently mark a concurrent user edit as already seen —
// the one way the watch protocol could lose a change. The diff shape matches
// what `watch` emits, so the caller reads both the same way.
func deliverPending(snapshot, content string) {
	if snapshot == "" {
		return
	}
	snap, err := os.ReadFile(snapshot)
	if err != nil {
		return // no snapshot yet: no watcher armed, nothing owed
	}
	if d := diffItems(string(snap), content); d != "" {
		fmt.Print(d)
	}
}

// editApplyOp runs one op through the shared board.Apply dispatcher inside a
// journaled, locked write (author "claude"), printing any advisory note
// ("N items match; using the first") to stderr.
func editApplyOp(path, snapshot, root string, op board.Op) int {
	op.Root = root
	if cloud.IsRemote(path) {
		return remoteApplyOp(path, op)
	}
	err := board.EditUnderLockJournaled(path, snapshot, "claude", func(content string) (string, error) {
		deliverPending(snapshot, content)
		b := board.Parse(content)
		b.Path = path
		note, aerr := board.Apply(b, op)
		if note != "" {
			fmt.Fprintln(os.Stderr, "session-notes:", note)
		}
		if aerr != nil {
			return "", aerr
		}
		// Lazy, write-driven id assignment: any save through the op layer stamps
		// ids on every node still lacking one (one-time churn per board).
		b.EnsureIDs()
		return b.Render(), nil
	})
	if err != nil {
		return editErr(err.Error())
	}
	return 0
}

// remoteApplyOp routes one op to a remote server via the RemoteTree client,
// resolving the stored login token by host. Op.Root travels in the POST so the
// carve-out is enforced server-side.
func remoteApplyOp(rawURL string, op board.Op) int {
	ref, err := cloud.ParseRef(rawURL)
	if err != nil {
		return editErr(err.Error())
	}
	tree := cloud.NewRemoteTree(ref.Server, ref.Board, cloud.TokenFor(ref.Host))
	res, err := tree.Apply(op)
	if err != nil {
		return editErr(err.Error())
	}
	if res.Note != "" {
		fmt.Fprintln(os.Stderr, "session-notes:", res.Note)
	}
	return 0
}

// remoteReplace performs `edit replace` against a remote board: fetch the raw
// blob, substitute the first occurrence, and push it back.
func remoteReplace(rawURL, old, replacement string) int {
	ref, err := cloud.ParseRef(rawURL)
	if err != nil {
		return editErr(err.Error())
	}
	token := cloud.TokenFor(ref.Host)
	tree := cloud.NewRemoteTree(ref.Server, ref.Board, token)
	content, _, err := tree.Raw()
	if err != nil {
		return editErr(err.Error())
	}
	if !strings.Contains(content, old) {
		return editErr(fmt.Sprintf("string not found: %q", old))
	}
	updated := strings.Replace(content, old, replacement, 1)
	if _, err := cloud.PushContent(ref.Server, token, ref.Board, updated, "claude"); err != nil {
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

func editErr(msg string) int {
	fmt.Fprintln(os.Stderr, "session-notes:", msg)
	return 2
}
