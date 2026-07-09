// Package hooks implements the Claude Code hook subcommands.
//
// Contract: hooks must be fast and must never fail the session. Every entry
// point swallows errors and returns nil (the caller exits 0). Output on stdout
// is injected into Claude's context, so only intentional text is printed.
package hooks

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nitsanavni/session-notes/internal/board"
)

// DefaultPinCadence is the maximum age of a pinned-item injection before the
// prompt-submit hook re-injects the pins even when their content is unchanged.
// A board can override it with a "pin-cadence:" frontmatter key.
const DefaultPinCadence = 15 * time.Minute

// PinCadence returns the effective re-injection cadence for a board: its
// "pin-cadence:" frontmatter value parsed as a Go duration, or DefaultPinCadence
// when the key is absent, unparseable, or non-positive. Parsing is lenient so a
// typo on one board never crashes the hook or starves its pins.
func PinCadence(b *board.Board) time.Duration {
	if b == nil {
		return DefaultPinCadence
	}
	if d, err := time.ParseDuration(strings.TrimSpace(b.Frontmatter.PinCadence)); err == nil && d > 0 {
		return d
	}
	return DefaultPinCadence
}

// PinsBlock returns the pinned-items block the prompt-submit hook and the `pins`
// command print (header + one bullet per pinned item), along with the flat list
// of pinned display texts used for the cadence hash. Both are empty when the
// board has no live pinned items.
func PinsBlock(b *board.Board) (string, []string) {
	pinned := b.PinnedItems()
	if len(pinned) == 0 {
		return "", nil
	}
	texts := make([]string, len(pinned))
	var sb strings.Builder
	sb.WriteString("Pinned board items (reinjected on cadence):\n")
	for i, it := range pinned {
		texts[i] = it.DisplayText()
		fmt.Fprintf(&sb, "- %s\n", texts[i])
	}
	return sb.String(), texts
}

// PinsStatePath returns the pins cadence state file path for a session id. The
// prompt-submit hook and `pins --due` share it so they observe one cadence.
func PinsStatePath(sessionID string) string { return pinsPath(sessionID) }

// PinsDue is the exported cadence gate shared by the hook and `pins --due`: it
// reports whether texts are due for re-injection under cadence, updating
// statePath when they are. See pinsDue.
func PinsDue(statePath string, texts []string, cadence time.Duration, now time.Time) bool {
	return pinsDue(statePath, texts, cadence, now)
}

// wipStaleAfter is how long a "[>]" item must have been continuously in-progress
// before the coherence digest flags it as untouched.
const wipStaleAfter = 2 * time.Hour

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
	board.SaveAuthor = "hook" // attribute journal entries to the hook, not the user
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

	fmt.Fprint(stdout, Blurb(path))

	if prev := previousBoards(in.SessionID, in.Cwd, 3); len(prev) > 0 {
		fmt.Fprintf(stdout, "\nBoards from previous sessions in this project (read them for history/context if useful):\n")
		for _, p := range prev {
			fmt.Fprintf(stdout, "- %s\n", p)
		}
	}
}

// Blurb returns the protocol blurb SessionStart prints, with boardPath
// substituted in. It is also exposed as `session-notes docs blurb` (with a
// placeholder path), so the injected text and the on-demand doc are one source.
func Blurb(boardPath string) string {
	return fmt.Sprintf(`Session board: %s

This session has a shared markdown board you and the user both maintain:
- Set a short "title:" in the frontmatter early (e.g. "auth refactor") so pickers show a name, not a uuid.
- Keep Threads and Plan current as you work (statuses: [ ] open, [>] wip, [x] done, [?] blocked); append milestones to Log (append-only).
- Reply forum-style: append an indented "  - claude: text" sub-bullet under an item rather than rewriting it. Discussions run FLAT (sibling bullets); nest deeper only to fork a sub-topic.
- "!!" marks an item urgent (injected once, next prompt); "!pin" keeps an item re-injected on a cadence (good for working agreements). Both go in the item text.
- WRITE THE BOARD VIA THE CLI, never by editing the file: "session-notes edit <add|reply|status|log|title|replace> --board %s [--refresh-snapshot <snap>] args…" does the whole locked read → modify → atomic-replace (the same lock the TUI uses), so your write never clobbers the user's concurrent edit.
- The user edits live in a TUI; watch the board with the Monitor tool and pass --refresh-snapshot to every edit so the watch ignores your own writes.
- Preserve content you don't understand; edit surgically.
- Run "session-notes docs <topic>" (protocol|monitor|conflicts|cli|blurb) for the full recipes (reply/threading, [[links]], watcher setup, conflict reconciliation, edit reference).
`, boardPath, boardPath)
}

// previousBoards returns up to max paths of other sessions' boards whose
// frontmatter cwd matches this session's cwd, newest first.
func previousBoards(sessionID, cwd string, max int) []string {
	if cwd == "" {
		return nil
	}
	entries, err := os.ReadDir(board.BoardsDir())
	if err != nil {
		return nil
	}
	type cand struct {
		path  string
		mtime time.Time
	}
	var cands []cand
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || len(name) < 4 || name[len(name)-3:] != ".md" || name == sessionID+".md" {
			continue
		}
		path := board.BoardsDir() + string(os.PathSeparator) + name
		b, err := board.Load(path)
		if err != nil || b.Frontmatter.Cwd != cwd {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		cands = append(cands, cand{path, fi.ModTime()})
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].mtime.After(cands[j].mtime) })
	out := make([]string, 0, max)
	for i := 0; i < len(cands) && i < max; i++ {
		out = append(out, cands[i].path)
	}
	return out
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

	// Pinned items re-inject on a cadence: whenever their content changed since
	// the last injection, or more than pinCadence has passed.
	now := time.Now()
	if block, texts := PinsBlock(b); len(texts) > 0 {
		if pinsDue(pinsPath(in.SessionID), texts, PinCadence(b), now) {
			fmt.Fprint(stdout, block)
		}
	}

	// Coherence digest: one cheap, deterministic health line.
	if line := coherenceDigest(b, wipPath(in.SessionID), now); line != "" {
		fmt.Fprintln(stdout, line)
	}

	statePath := lastSeenPath(in.SessionID)
	if sfi, err := os.Stat(statePath); err != nil || fi.ModTime().After(sfi.ModTime()) {
		if err == nil { // state exists and board is newer => it changed
			fmt.Fprintf(stdout, "Note: the session board (%s) changed since you last saw it; re-read it.\n", path)
		}
	}
	touch(statePath)
}

// pinsDue reports whether the pinned items (identified by texts) are due for
// re-injection, updating the pins state file when they are. The state file holds
// one line "<unix-ts>\t<hash>": injection is due when the file is missing, the
// content hash changed, or now is more than pinCadence past the stored ts. On a
// due result the file is rewritten with now + the current hash; otherwise it is
// left untouched so staleness keeps accruing from the last real injection.
func pinsDue(statePath string, texts []string, cadence time.Duration, now time.Time) bool {
	hash := hashTexts(texts)
	due := true
	if data, err := os.ReadFile(statePath); err == nil {
		if ts, h, ok := strings.Cut(strings.TrimSpace(string(data)), "\t"); ok {
			if h == hash {
				if sec, perr := strconv.ParseInt(ts, 10, 64); perr == nil {
					due = now.Sub(time.Unix(sec, 0)) > cadence
				}
			}
		}
	}
	if due {
		_ = os.MkdirAll(board.StateDir(), 0o755)
		_ = os.WriteFile(statePath, []byte(fmt.Sprintf("%d\t%s\n", now.Unix(), hash)), 0o644)
	}
	return due
}

func hashTexts(texts []string) string {
	h := sha256.Sum256([]byte(strings.Join(texts, "\n")))
	return hex.EncodeToString(h[:])
}

// coherenceDigest returns a one-line board-health summary, or "" when every
// count is zero. It also ages "[>]" items via the wip state file at wipPath.
// Clauses (only nonzero ones appear):
//   - N threads [>] untouched >2h (age tracked in the wip state file)
//   - M questions @claude unanswered ([ ] items mentioning @claude, no claude: reply)
//   - K items in Waiting on User
func coherenceDigest(b *board.Board, wipPath string, now time.Time) string {
	stale := trackWip(wipPath, b.InProgressRawLines(), now)
	unanswered := len(b.UnansweredClaudeQuestions())
	waiting := b.WaitingOnUserCount()

	var clauses []string
	if stale > 0 {
		clauses = append(clauses, fmt.Sprintf("%d threads [>] untouched >2h", stale))
	}
	if unanswered > 0 {
		clauses = append(clauses, fmt.Sprintf("%d questions @claude unanswered", unanswered))
	}
	if waiting > 0 {
		clauses = append(clauses, fmt.Sprintf("%d items in Waiting on User", waiting))
	}
	if len(clauses) == 0 {
		return ""
	}
	return "Board health: " + strings.Join(clauses, " · ")
}

// trackWip records the first-seen time of each currently-in-progress raw line in
// the wip state file, drops lines that are no longer "[>]", and returns how many
// surviving entries were first seen more than wipStaleAfter ago. The file holds
// one "<unix-ts>\t<raw-line>" per line.
func trackWip(wipPath string, current []string, now time.Time) int {
	seen := map[string]int64{}
	if f, err := os.Open(wipPath); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			ts, raw, ok := strings.Cut(sc.Text(), "\t")
			if !ok {
				continue
			}
			if sec, perr := strconv.ParseInt(ts, 10, 64); perr == nil {
				seen[raw] = sec
			}
		}
		f.Close()
	}

	stale := 0
	var sb strings.Builder
	// Iterate current (document order) so the rewritten file stays ordered and
	// only carries lines that are still in progress.
	for _, raw := range current {
		first, ok := seen[raw]
		if !ok {
			first = now.Unix()
		}
		fmt.Fprintf(&sb, "%d\t%s\n", first, raw)
		if now.Sub(time.Unix(first, 0)) > wipStaleAfter {
			stale++
		}
	}
	if len(current) == 0 {
		_ = os.Remove(wipPath)
	} else {
		_ = os.MkdirAll(board.StateDir(), 0o755)
		_ = os.WriteFile(wipPath, []byte(sb.String()), 0o644)
	}
	return stale
}

// SessionEnd handles the SessionEnd hook: log the end and remove the pane
// mapping if it points at this session.
func SessionEnd(stdin io.Reader, stdout io.Writer) {
	board.SaveAuthor = "hook" // attribute journal entries to the hook, not the user
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

// pinsPath is the pinned-item cadence state file (timestamp + content hash).
func pinsPath(sessionID string) string {
	return board.StateDir() + string(os.PathSeparator) + sessionID + ".pins"
}

// wipPath is the "[>]" age-tracking state file for the coherence digest.
func wipPath(sessionID string) string {
	return board.StateDir() + string(os.PathSeparator) + sessionID + ".wip"
}

func touch(path string) {
	_ = os.MkdirAll(board.StateDir(), 0o755)
	now := time.Now()
	if err := os.Chtimes(path, now, now); err != nil {
		_ = os.WriteFile(path, nil, 0o644)
	}
}
