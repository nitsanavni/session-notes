package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nitsanavni/session-notes/internal/board"
)

// runWatch implements `session-notes watch --board <path> [--snapshot <path>]
// [--once] [--interval <dur>] [--no-notes]`: the first-class replacement for the
// hand-rolled `cp BOARD snap; while true; diff …` monitor loop. It blocks,
// polling the board file (and its sibling `.notes/` dir) on an interval; on each
// external change it prints the changed lines as a unified-style item diff to
// stdout and keeps watching. `--once` exits 0 after the first change — the mode
// an agent's background/Monitor task wants (react, then re-arm).
//
// Self-edit suppression reuses the exact same snapshot the edit CLI already
// refreshes: each poll compares the board to the snapshot UNDER THE BOARD LOCK,
// so an `edit --refresh-snapshot <snap>` write (which updates the snapshot inside
// that same lock) is never seen as an external change. Pass the SAME path to
// both `watch --snapshot` and `edit --refresh-snapshot`.
func runWatch(args []string) int {
	var boardPath, session, snapshot, node string
	once := false
	notes := true
	interval := 2 * time.Second
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
			watchUsage(os.Stdout)
			return 0
		case "--board":
			if v, ok := takeVal(); ok {
				boardPath = v
			} else {
				return watchErr("--board needs a path")
			}
		case "--session":
			if v, ok := takeVal(); ok {
				session = v
			} else {
				return watchErr("--session needs an id")
			}
		case "--snapshot":
			if v, ok := takeVal(); ok {
				snapshot = v
			} else {
				return watchErr("--snapshot needs a path")
			}
		case "--interval":
			v, ok := takeVal()
			if !ok {
				return watchErr("--interval needs a duration (e.g. 2s)")
			}
			d, err := time.ParseDuration(v)
			if err != nil || d <= 0 {
				return watchErr("--interval needs a positive duration (e.g. 2s)")
			}
			interval = d
		case "--node":
			if v, ok := takeVal(); ok {
				node = v
			} else {
				return watchErr("--node needs a node id")
			}
		case "--once":
			once = true
		case "--notes":
			notes = true
		case "--no-notes":
			notes = false
		default:
			return watchErr(fmt.Sprintf("unknown argument: %s", a))
		}
	}

	// --node accepts a bare id or the canonical <board>#<id> ref; the fragment
	// after '#' is the node id, and a board part there overrides --board.
	if i := strings.Index(node, "#"); i >= 0 {
		if b := node[:i]; b != "" && boardPath == "" {
			boardPath = b
		}
		node = node[i+1:]
	}

	path, err := editBoardPath(boardPath, session)
	if err != nil {
		return watchErr(err.Error())
	}
	if snapshot == "" {
		snapshot = defaultSnapshotPath(path)
	}

	w := &watcher{board: path, snapshot: snapshot, node: node}
	if notes {
		w.notesDir = notesDirFor(path)
		w.notesState = snapshot + ".notes.json"
	}

	// Initialise the snapshot(s) only when absent, so a change that arrived
	// between two `--once` invocations (while the agent was busy) is still
	// reported on the next arming rather than silently swallowed by a fresh cp.
	if err := w.init(); err != nil {
		return watchErr(err.Error())
	}

	for {
		out, changed, gone, err := w.poll()
		if err != nil {
			return watchErr(err.Error())
		}
		if changed {
			fmt.Print(out)
			if once {
				if gone {
					return 1
				}
				return 0
			}
		}
		time.Sleep(interval)
	}
}

// watcher holds the polling state for one board (and optionally its notes dir).
type watcher struct {
	board      string
	snapshot   string
	node       string // "" watches the whole board; else only the subtree rooted here
	notesDir   string // "" disables notes watching
	notesState string // JSON sidecar of note path -> content hash
}

// init seeds any missing snapshot from the current state so the first poll does
// not report the whole board (or every note) as a change. Existing snapshots are
// left untouched — a pending, unreported change survives across invocations.
func (w *watcher) init() error {
	if _, err := os.Stat(w.snapshot); err != nil {
		cur, err := os.ReadFile(w.board)
		if err != nil {
			return err
		}
		if err := os.WriteFile(w.snapshot, cur, 0o644); err != nil {
			return err
		}
	}
	if w.notesDir != "" {
		if _, err := os.Stat(w.notesState); err != nil {
			cur, err := hashNotesDir(w.notesDir)
			if err != nil {
				return err
			}
			if err := writeNotesState(w.notesState, cur); err != nil {
				return err
			}
		}
	}
	return nil
}

// poll compares the board (and notes) to the snapshot under the board lock and,
// on change, returns the formatted diff and advances the snapshot. It is the
// testable core of the watch loop: no sleeping, no globals.
func (w *watcher) poll() (string, bool, bool, error) {
	var out strings.Builder
	changed := false
	gone := false
	err := board.WithLock(w.board, func() error {
		cur, err := os.ReadFile(w.board)
		if err != nil {
			return err
		}
		var snap []byte
		if b, err := os.ReadFile(w.snapshot); err == nil {
			snap = b
		}
		if w.node != "" {
			// Subtree watch: diff only the node's source lines, so edits
			// elsewhere on the board are invisible. A node that existed in the
			// snapshot but is absent now is reported as gone (and, in --once
			// mode, exits nonzero).
			before, hadBefore := board.SubtreeSource(string(snap), w.node)
			after, hasNow := board.SubtreeSource(string(cur), w.node)
			if d := diffItems(before, after); d != "" {
				changed = true
				out.WriteString(d)
			}
			if hadBefore && !hasNow {
				changed = true
				gone = true
				fmt.Fprintf(&out, "node gone: %s\n", w.node)
			}
			if changed {
				if err := os.WriteFile(w.snapshot, cur, 0o644); err != nil {
					return err
				}
			}
			return nil
		}
		if d := diffItems(string(snap), string(cur)); d != "" {
			changed = true
			out.WriteString(d)
			if err := os.WriteFile(w.snapshot, cur, 0o644); err != nil {
				return err
			}
		}

		if w.notesDir != "" {
			curNotes, err := hashNotesDir(w.notesDir)
			if err != nil {
				return err
			}
			prev, _ := readNotesState(w.notesState)
			if d := diffNotes(prev, curNotes); d != "" {
				changed = true
				out.WriteString(d)
				if err := writeNotesState(w.notesState, curNotes); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return out.String(), changed, gone, err
}

// diffItems renders the item-level change between two board contents as a
// unified-style diff: removed lines prefixed "-", added lines prefixed "+"
// (the same multiset shape board.DiffLines produces and history prints). Empty
// when nothing changed.
func diffItems(before, after string) string {
	added, removed := board.DiffLines(before, after)
	if len(added) == 0 && len(removed) == 0 {
		return ""
	}
	var b strings.Builder
	for _, l := range removed {
		fmt.Fprintf(&b, "-%s\n", l)
	}
	for _, l := range added {
		fmt.Fprintf(&b, "+%s\n", l)
	}
	return b.String()
}

// diffNotes reports added/changed/removed note files between two hash maps.
func diffNotes(before, after map[string]string) string {
	var lines []string
	for name, h := range after {
		if old, ok := before[name]; !ok {
			lines = append(lines, "note added: "+name)
		} else if old != h {
			lines = append(lines, "note changed: "+name)
		}
	}
	for name := range before {
		if _, ok := after[name]; !ok {
			lines = append(lines, "note removed: "+name)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n") + "\n"
}

// hashNotesDir returns a map of note file name -> content hash for every regular
// file directly under dir. A missing dir yields an empty map (no error), so a
// board that never grew a .notes/ sibling watches cleanly.
func hashNotesDir(dir string) (map[string]string, error) {
	m := map[string]string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return m, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		sum := sha256.Sum256(data)
		m[e.Name()] = hex.EncodeToString(sum[:])
	}
	return m, nil
}

func readNotesState(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]string{}, err
	}
	m := map[string]string{}
	if err := json.Unmarshal(data, &m); err != nil {
		return map[string]string{}, err
	}
	return m, nil
}

func writeNotesState(path string, m map[string]string) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// notesDirFor maps a board path "<dir>/<id>.md" to its sibling "<dir>/<id>.notes"
// (the same convention board.NotesDir uses for [[side notes]]).
func notesDirFor(boardPath string) string {
	return strings.TrimSuffix(boardPath, filepath.Ext(boardPath)) + ".notes"
}

// defaultSnapshotPath is the snapshot used when --snapshot is omitted. An agent
// that also writes via `edit --refresh-snapshot` should pass this same path to
// both (or set --snapshot on watch and --refresh-snapshot on edit to match).
// Prefer the default: the web UI reads this exact path to render per-item
// delivery receipts (board.WatchSnapshotPath is the shared convention).
func defaultSnapshotPath(boardPath string) string {
	return board.WatchSnapshotPath(boardPath)
}

func watchErr(msg string) int {
	fmt.Fprintln(os.Stderr, "watch:", msg)
	return 2
}

func watchUsage(w *os.File) {
	fmt.Fprint(w, `session-notes watch — block and emit the board's live edits as a diff

Usage:
  session-notes watch --board <path> [--node <id>] [--snapshot <path>]
                      [--once] [--interval <dur>] [--no-notes]

Blocks, polling the board file on --interval (default 2s). On each external
change it prints the changed items as a unified-style diff (removed "-",
added "+") to stdout and keeps watching. --once exits 0 after the first change
(the mode a background/Monitor task wants: react, then re-arm).

  --board <path> | --session <id>   the board to watch (exactly one)
  --node <id>                       watch only the subtree rooted at this node
                                    (accepts <board>#<id>); changes elsewhere are
                                    ignored, a deleted root reports "node gone"
                                    and exits nonzero in --once mode
  --snapshot <path>                 self-edit snapshot; pass the SAME path to
                                    'edit --refresh-snapshot' so your own writes
                                    stay invisible (default: <board>.watch)
  --once                            exit after the first change
  --interval <dur>                  poll interval, Go duration (default 2s)
  --no-notes                        do not also watch the board's .notes/ sibling
                                    (watched by default when present)
`)
}
