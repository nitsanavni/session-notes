// session-notes — shared per-session board for Claude Code.
// See SPEC.md for the full design.
package main

import (
	"fmt"
	"os"

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
		os.Exit(runUpgrade())
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
  session-notes hook session-start    Claude Code SessionStart hook (JSON on stdin)
  session-notes hook session-end      SessionEnd hook
  session-notes hook prompt-submit    UserPromptSubmit hook
`)
}

func exitIf(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "session-notes:", err)
		os.Exit(1)
	}
}
