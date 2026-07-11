package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nitsanavni/session-notes/internal/board"
)

// runInit implements `session-notes init`: seed a fresh board file from the
// canonical template — the bootstrap for environments where no session-start
// hook runs (headless agents, CI, containers; see `docs headless`). Refuses to
// overwrite an existing board and prints the created path on success.
func runInit(args []string) int {
	usage := "usage: session-notes init --board <path> | --session <id> [--cwd <dir>] [--title <t>]"
	var path, session, cwd, title string
	for i := 0; i < len(args); i++ {
		need := func() (string, bool) {
			i++
			if i >= len(args) {
				return "", false
			}
			return args[i], true
		}
		var ok bool
		switch args[i] {
		case "-h", "--help":
			fmt.Println(usage)
			return 0
		case "--board":
			path, ok = need()
		case "--session":
			session, ok = need()
		case "--cwd":
			cwd, ok = need()
		case "--title":
			title, ok = need()
		default:
			return initErr("unknown flag: " + args[i] + "\n" + usage)
		}
		if !ok {
			return initErr(args[i-1] + " needs a value\n" + usage)
		}
	}
	if path == "" && session != "" {
		path = board.BoardPath(session)
	}
	if path == "" {
		return initErr("a target is required: pass --board <path> or --session <id>")
	}
	if session == "" {
		session = strings.TrimSuffix(filepath.Base(path), ".md")
	}
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if _, err := os.Stat(path); err == nil {
		return initErr("board already exists: " + path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return initErr(err.Error())
	}
	board.SaveAuthor = "init"
	b := board.Parse(board.Template(session, cwd, time.Now()))
	if title != "" {
		b.SetTitle(title)
	}
	if err := b.SaveTo(path); err != nil {
		return initErr(err.Error())
	}
	fmt.Println(path)
	return 0
}

func initErr(msg string) int {
	fmt.Fprintln(os.Stderr, "session-notes: "+msg)
	return 2
}
