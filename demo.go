package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/nitsanavni/session-notes/internal/board"
	"github.com/nitsanavni/session-notes/internal/web"
)

// demoBoard is the seeded content for `session-notes demo`: a small board with a
// few nested subtrees so the zoom / carve-out primitive has something to show.
// IDs are stamped by EnsureIDs on save (like any programmatic write).
const demoBoard = `---
session: demo
title: session-notes demo
---

## Plan
- [x] scaffold the demo board
- [>] show off zoom + subtree carve-outs
- [ ] hand a subtree to a sub-agent

## Threads
- [>] auth refactor — extracting middleware
  - claude: pulled the token check into RequireAuth
  - [ ] wire the rate limiter next
- [>] search relevance
  - [ ] tune BM25 weights
  - [ ] add a synonym table
- [x] fix flaky test

## Questions
- [ ] !! drop the legacy /v1 endpoint? @user
  - user: what still calls it?
  - claude: only the mobile client < 3.0

## Ideas
- cache invalidation could be event-driven
- a plugin surface for custom sections
`

// demoInstance is a running demo: a served throwaway board plus the teardown
// that stops the server and removes its temp dir.
type demoInstance struct {
	boardPath string
	id        string // board id it is served under
	url       string // http://host:port/b/<id>
	rootID    string // a subtree node id to suggest (may be "")
	rootText  string
	stop      func()
}

// startDemo seeds a throwaway board in a temp dir and serves it on addr (use
// host:0 for a free port). It never touches ~/.claude/boards. The returned
// instance's stop() closes the server and removes the temp dir.
func startDemo(addr string) (*demoInstance, error) {
	dir, err := os.MkdirTemp("", "session-notes-demo-")
	if err != nil {
		return nil, err
	}
	boardPath := filepath.Join(dir, "demo.md")
	b := board.Parse(demoBoard)
	b.EnsureIDs() // stamp stable node ids so the carve-out suggestion is concrete
	board.SaveAuthor = "demo"
	if err := b.SaveTo(boardPath); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	rootID, rootText := demoSubtreeRoot(boardPath)

	srv := web.New()
	id, err := srv.Register(boardPath)
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	srv.SetHome(id)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	httpSrv := &http.Server{Handler: srv.Handler()}
	go func() {
		if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			fmt.Fprintln(os.Stderr, "session-notes:", err)
		}
	}()

	inst := &demoInstance{
		boardPath: boardPath,
		id:        id,
		url:       fmt.Sprintf("http://%s/b/%s", ln.Addr().String(), id),
		rootID:    rootID,
		rootText:  rootText,
		stop: func() {
			httpSrv.Close()
			os.RemoveAll(dir)
		},
	}
	return inst, nil
}

// runDemo implements `session-notes demo`: seed a throwaway board, serve it
// locally, and print a few things to try. Ctrl-C tears the whole thing down.
// File-mode only — no tokens, no ~/.claude/boards writes — so a stranger can see
// the product in one command.
func runDemo(args []string) int {
	addr := "127.0.0.1:0" // 0 = pick a free port
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			fmt.Println("usage: session-notes demo [--addr host:port]")
			fmt.Println("  Seed a throwaway board, serve it locally, print things to try. Ctrl-C tears down.")
			return 0
		case "--addr":
			if i+1 >= len(args) {
				return serveErr("--addr needs host:port")
			}
			i++
			addr = args[i]
		default:
			return serveErr("unknown argument: " + args[i])
		}
	}

	inst, err := startDemo(addr)
	if err != nil {
		return serveErr(err.Error())
	}

	fmt.Println("session-notes demo — a throwaway board, served locally.")
	fmt.Println()
	fmt.Println("  board file:", inst.boardPath)
	fmt.Println("  open:      ", inst.url)
	fmt.Println()
	fmt.Println("Things to try:")
	fmt.Println("  1. Open the URL. Press ? for the key map, m for the mindmap view.")
	fmt.Println("     f zooms into a subtree (outline + map), b/Escape steps back out.")
	fmt.Println("  2. Edit from a second terminal — the page updates live:")
	fmt.Printf("       session-notes edit add Threads \"a new thread\" --board %s\n", inst.boardPath)
	if inst.rootID != "" {
		fmt.Printf("  3. Hand one subtree to a sub-agent (carve-out). Edits confined to %q:\n", inst.rootText)
		fmt.Printf("       session-notes edit reply \"…\" \"child: done\" --board %s --root %s\n", inst.boardPath, inst.rootID)
		fmt.Printf("       session-notes watch --board %s --node %s --once\n", inst.boardPath, inst.rootID)
	}
	fmt.Printf("  4. Stream live edits as JSON:  session-notes watch --board %s --json\n", inst.boardPath)
	fmt.Println()
	fmt.Println("Ctrl-C to stop (the temp board is removed).")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	fmt.Println("\nsession-notes: shutting down demo…")
	inst.stop()
	return 0
}

// demoSubtreeRoot returns the id and text of the first item with children in the
// saved demo board — a good zoom/carve-out root to suggest. Empty if none.
func demoSubtreeRoot(path string) (id, text string) {
	b, err := board.Load(path)
	if err != nil {
		return "", ""
	}
	for _, s := range b.Sections {
		for _, it := range s.Items {
			if it != nil && it.ID != "" && len(it.Children) > 0 {
				return it.ID, it.Text
			}
		}
	}
	return "", ""
}
