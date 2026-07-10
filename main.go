// session-notes — shared per-session board for Claude Code.
// See SPEC.md for the full design.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/nitsanavni/session-notes/internal/board"
	"github.com/nitsanavni/session-notes/internal/hooks"
	"github.com/nitsanavni/session-notes/internal/tui"
	"github.com/nitsanavni/session-notes/internal/update"
)

func main() {
	args := os.Args[1:]

	// Hook subcommands: must never exit non-zero or block the session. The
	// update check must never be reachable from here (see internal/update).
	if len(args) >= 1 && args[0] == "hook" {
		if len(args) >= 2 {
			runHook(args[1])
		}
		os.Exit(0)
	}

	// Upgrade subcommand: fetch and swap in the latest release.
	if len(args) >= 1 && args[0] == "upgrade" {
		if len(args) >= 2 && (args[1] == "-h" || args[1] == "--help") {
			fmt.Println("usage: session-notes upgrade")
			os.Exit(0)
		}
		os.Exit(runUpgrade())
	}

	// Feedback subcommand: list a board's map-nav surprise records
	// (<board>.feedback.jsonl, written by `!` in the map view).
	if len(args) >= 1 && args[0] == "feedback" {
		os.Exit(runFeedback(args[1:]))
	}

	// Edit subcommand: first-class locked write path (add/reply/status/log/
	// title/replace) for agents editing a board. See edit.go.
	if len(args) >= 1 && args[0] == "edit" {
		os.Exit(runEdit(args[1:]))
	}

	// History subcommand: print a board's journaled edits as compact diffs
	// (the catch-up command for agents and returning humans). See history.go.
	if len(args) >= 1 && args[0] == "history" {
		os.Exit(runHistory(args[1:]))
	}

	// Pins subcommand: surface a board's pinned items outside the prompt path
	// (monitors can call `pins --due` on the shared cadence). See pins.go.
	if len(args) >= 1 && args[0] == "pins" {
		os.Exit(runPins(args[1:]))
	}

	// Docs subcommand: print an on-demand protocol/recipe topic. See docs.go.
	if len(args) >= 1 && args[0] == "docs" {
		os.Exit(runDocs(args[1:]))
	}

	// Watch subcommand: block and emit the board's live edits as a diff, the
	// first-class replacement for a hand-rolled file-watcher loop. See watch.go.
	if len(args) >= 1 && args[0] == "watch" {
		os.Exit(runWatch(args[1:]))
	}

	// Serve subcommand: the web UI for non-tmux use. See serve.go.
	if len(args) >= 1 && args[0] == "serve" {
		os.Exit(runServe(args[1:]))
	}

	// The TUI surfaces the upgrade hint asynchronously off the UI thread; give
	// it the installed version to compare against.
	tui.Version = versionString()

	var boardPath, paneID string
	picker := false
	dash := false
	all := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--board":
			if i+1 < len(args) {
				i++
				boardPath = args[i]
			}
		case "--pane":
			if i+1 < len(args) {
				i++
				paneID = args[i]
			}
		case "-l", "--list":
			picker = true
		case "-d", "--dash":
			dash = true
		case "--all":
			all = true
		case "-h", "--help":
			usage(os.Stdout)
			return
		case "-v", "--version":
			fmt.Println("session-notes", versionString())
			if h := update.Hint(versionString()); h != "" {
				fmt.Fprintln(os.Stderr, h)
			}
			return
		default:
			fmt.Fprintf(os.Stderr, "unknown argument: %s\n\n", args[i])
			usage(os.Stderr)
			os.Exit(2)
		}
	}

	if dash {
		exitIf(tui.RunDash(all))
		return
	}
	if picker {
		exitIf(tui.RunPicker())
		return
	}

	path, ok := resolveBoard(boardPath, paneID)
	if !ok {
		exitIf(tui.RunPicker())
		return
	}
	exitIf(tui.Run(path))
}

// resolveBoard implements the spec's session-resolution order. Returns
// ok=false when the caller should fall back to the picker.
func resolveBoard(explicit, paneID string) (string, bool) {
	// 1. --board explicit path wins.
	if explicit != "" {
		return explicit, true
	}
	// 2. --pane mapping.  3. $TMUX_PANE mapping.
	if paneID == "" {
		paneID = os.Getenv("TMUX_PANE")
	}
	if paneID != "" {
		if m, err := board.ReadPaneMapping(paneID); err == nil && m.Board != "" {
			if _, err := os.Stat(m.Board); err == nil {
				return m.Board, true
			}
		}
		// Mapping missing or its board file is gone: fall back to the newest
		// board whose frontmatter cwd matches the pane's current directory (if
		// we can discover it via tmux), rather than exiting. If that finds
		// nothing we fall through to the picker below.
		if dir := paneCurrentPath(paneID); dir != "" {
			if path, ok := tui.NewestBoardByCwd(dir); ok {
				return path, true
			}
		}
	}
	// 4. Exactly one board matching the current cwd.
	// 5. Exactly one live session across all pane mappings.
	//
	// PAUSED: these two fuzzy, liveness-based steps can resolve to the wrong
	// session's board when a project has several boards (see the picker's
	// FindBoardByCwd). Until that's fixed, skip them and fall through to the
	// picker for manual selection. Set SESSION_NOTES_AUTORESOLVE=1 to restore
	// the automatic behavior.
	if os.Getenv("SESSION_NOTES_AUTORESOLVE") != "" {
		if cwd, err := os.Getwd(); err == nil {
			if path, ok := tui.FindBoardByCwd(cwd); ok {
				return path, true
			}
		}
		if path, ok := soleLiveBoard(); ok {
			return path, true
		}
	}
	// 6. Picker.
	return "", false
}

// paneCurrentPath asks tmux for a pane's current working directory, used to
// resolve a board by cwd when the pane has no mapping. Returns "" if tmux is
// unavailable or the pane is unknown (the caller then falls back to the picker).
var paneCurrentPath = func(paneID string) string {
	out, err := exec.Command("tmux", "display-message", "-p", "-t", paneID, "#{pane_current_path}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// soleLiveBoard returns the board path if every pane mapping (with a board
// file that still exists) points at the same single board.
func soleLiveBoard() (string, bool) {
	entries, err := os.ReadDir(board.PanesDir())
	if err != nil {
		return "", false
	}
	sole := ""
	for _, e := range entries {
		name := e.Name()
		if len(name) < 6 || name[len(name)-5:] != ".json" {
			continue
		}
		m, err := board.ReadPaneMapping(name[:len(name)-5])
		if err != nil || m.Board == "" {
			continue
		}
		if _, err := os.Stat(m.Board); err != nil {
			continue
		}
		if sole != "" && sole != m.Board {
			return "", false
		}
		sole = m.Board
	}
	return sole, sole != ""
}

// runFeedback parses `session-notes feedback <board.md> [--json | --gen-test]`
// and prints the board's surprise records.
func runFeedback(args []string) int {
	var boardPath string
	jsonOut := false
	genTest := false
	for _, a := range args {
		switch a {
		case "-h", "--help":
			fmt.Println("usage: session-notes feedback <board.md> [--json | --gen-test]")
			return 0
		case "--json":
			jsonOut = true
		case "--gen-test":
			genTest = true
		default:
			if strings.HasPrefix(a, "-") || boardPath != "" {
				fmt.Fprintf(os.Stderr, "feedback: unknown argument: %s\n", a)
				return 2
			}
			boardPath = a
		}
	}
	if boardPath == "" {
		fmt.Fprintln(os.Stderr, "usage: session-notes feedback <board.md> [--json | --gen-test]")
		return 2
	}
	if err := tui.RunFeedback(boardPath, jsonOut, genTest); err != nil {
		fmt.Fprintln(os.Stderr, "session-notes:", err)
		return 1
	}
	return 0
}

func runHook(name string) {
	// Belt and braces: a hook must exit 0 even if something panics.
	defer func() { _ = recover() }()
	switch name {
	case "session-start":
		hooks.SessionStart(os.Stdin, os.Stdout)
	case "session-end":
		hooks.SessionEnd(os.Stdin, os.Stdout)
	case "prompt-submit":
		hooks.PromptSubmit(os.Stdin, os.Stdout)
	case "stop":
		hooks.Stop(os.Stdin, os.Stdout)
	case "statusline":
		hooks.StatusLine(os.Stdin, os.Stdout)
	}
}

func usage(w *os.File) {
	fmt.Fprint(w, `session-notes — shared session board for Claude Code

Usage:
  session-notes                       open the TUI for the current session's board
  session-notes --pane <tmux-pane>    resolve board via pane mapping
  session-notes --board <path>        open a specific board file
  session-notes -d, --dash            live dashboard of this project's sessions
  session-notes --dash --all          dashboard across every project
  session-notes -l                    board picker (all boards, newest first)
  session-notes -v, --version         print version and exit
  session-notes upgrade               update to the latest release
  session-notes feedback <board.md>   list map-nav surprise records (--json raw,
                                      --gen-test print replayable Go test source)
  session-notes edit <sub> …          locked board write (--board <path> or
                                      --session <id>); subs: add reply fork
                                      status log title replace undo redo.
                                      --refresh-snapshot <p> refreshes a
                                      monitor snapshot in-lock
  session-notes history …             print a board's journaled edits as compact
                                      line-diffs, oldest first (--board <path> or
                                      --session <id>; -n <N> limits, default 20)
  session-notes pins [--due] …        print a board's pinned-item block (--board
                                      <path> or --session <id>); --due prints only
                                      when due on the re-injection cadence and
                                      updates the shared state (for monitors)
  session-notes serve                 web UI (dashboard + boards) on
                                      127.0.0.1:7080; --addr <host:port> to
                                      change, --board <path> to serve one board.
                                      Non-loopback binds need --token <t> (or
                                      $SESSION_NOTES_TOKEN; Bearer/?token=/
                                      cookie) or an explicit --insecure
  session-notes watch …               block and print the board's live edits as
                                      a unified item diff (--board <path> or
                                      --session <id>; --once exits after the
                                      first change; --snapshot <p> shares the
                                      edit --refresh-snapshot file; --interval
                                      <dur> default 2s; watches .notes/ too)
  session-notes docs <topic>          print a protocol/recipe topic
                                      (protocol|monitor|conflicts|cli|blurb); no
                                      topic lists them
  session-notes hook session-start    Claude Code SessionStart hook (JSON on stdin)
  session-notes hook session-end      SessionEnd hook
  session-notes hook prompt-submit    UserPromptSubmit hook
  session-notes hook stop             Stop hook (marks the session idle)
  session-notes hook statusline       statusLine command: records model /
                                      context% / cost into the board status
                                      sidecar and prints a compact status line
`)
}

func exitIf(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "session-notes:", err)
		os.Exit(1)
	}
}
