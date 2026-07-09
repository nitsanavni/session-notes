package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/nitsanavni/session-notes/internal/hooks"
)

// docTopics maps a `session-notes docs <topic>` topic to its recipe text. The
// content is the on-demand companion to the (now-shrunk) session-start blurb:
// the blurb keeps the essential habits; these topics carry the full recipes.
// They are versioned with the binary, so they never drift from actual behavior.
var docTopics = map[string]string{
	"protocol": `Board protocol — conventions

The board is a markdown file grouped into "## " sections. A fresh board seeds:
Waiting on User, Plan, Threads, Questions, Ideas, Parked, Working Agreements, Log
(Archive is created on demand). Sections can be renamed; nothing hard-codes their
meaning except a few conventions below.

Statuses (item checkboxes):
  [ ] open    [>] in progress    [x] done    [?] blocked    (plain "- " = no checkbox)

Sections you maintain:
- Keep Threads and Plan current as you work.
- Answer questions addressed "@claude" in Questions; raise your own as
  "- [ ] question @user".
- Waiting on User is the human's attention queue: move anything blocked on the
  user there with a one-line why.
- Append milestones to Log as "- HH:MM claude: text" (append-only).

Replies / threading (forum-style):
- To answer or react to a specific item, append an INDENTED sub-bullet reply
  under it ("  - claude: text") instead of rewriting the item's text inline.
  Replies nest 2 spaces per level.
- Discussions run FLAT: continue a back-and-forth as sibling bullets at the same
  level; nest deeper only to fork a sub-topic off one specific message.

Markers (in the item text, after any status marker):
- "!!" — urgent. Injected into Claude's context once, on the next user prompt.
- "!pin" — pinned. Re-injected on a cadence (whenever the pinned set changes, or
  >35 min since the last injection). Use it for working agreements / standing
  instructions that should survive long sessions and compaction.

[[links]] come in two forms, both opened with "o" in the TUI:
- [[name]] (no slash) is a SIDE NOTE at <boards-dir>/<session-id>.notes/name.md.
  Write longer content (designs, scoping, research) there; opening a missing one
  creates it.
- [[path/with/slash.md]] (has a slash, or starts with ~/ or /) is a FILE PATH,
  resolved relative to the session cwd (~ expanded, absolute as-is). Use it to
  link real repo files; opening a missing path shows an error, no stub.

Preserve any content you don't understand; edit surgically.`,

	"monitor": `Monitor — react to the user's live board edits

The user edits the board in a TUI while you work. Watch the file with the Monitor
tool so you react at edit time (a dropped "!!" question, a reprioritized thread),
not just at the next prompt boundary. Have the watch emit the diff itself so the
event carries the change and you needn't re-read the file:

  cp BOARD snap
  while true; do
    sleep 1
    cmp -s BOARD snap && continue
    diff -U0 snap BOARD | grep -E '^[+-]' | grep -vE '^(\+\+\+|---) '
    cp BOARD snap
  done

Self-edit suppression: to keep the watch from firing on YOUR OWN edits, pass
"--refresh-snapshot snap" to every "session-notes edit" call. It copies the new
content over the snapshot INSIDE THE SAME LOCK, right after the atomic replace, so
a watcher that also takes the lock around its compare-and-snapshot cycle only ever
emits the user's edits, never yours.

  session-notes edit reply "frobnicator" "claude: rewired it" \
    --board BOARD --refresh-snapshot snap`,

	"conflicts": `Conflicts — reconciling merge markers under the lock

If the board shows <<<<<<< / ======= / >>>>>>> conflict markers, a merge needs
reconciling. Rules:
- Integrate BOTH sides. NEVER delete the user's text.
- Remove the markers.
- Write the result with "session-notes edit replace <old> <new>" (one or several)
  so the reconciliation happens under the same lock every other writer uses — do
  not hand-edit the file.

"edit replace" does an exact first-occurrence string replace over the raw file; it
errors if <old> is not found. Quote multi-word arguments. Reconcile in small,
precise replacements (e.g. one per conflict hunk) rather than one giant swap, so a
mismatch tells you exactly which hunk drifted.`,

	"cli": `Claude write CLI — edit reference

WRITE THE BOARD VIA THE CLI, never by editing the file yourself. Each subcommand
does the whole locked read → modify → atomic-replace (the same advisory lock the
TUI uses), so your write never clobbers the user's concurrent edit and you never
reinvent an flock script.

Target the board with --board <path> (primary for an agent) or --session <id>
(resolved to <boards-dir>/<id>.md). Every write is SILENT on success and exits
non-zero with a one-line reason on failure.

  edit add <section> <text>     append a top-level item to an existing section
                                (errors, listing sections, if it is missing)
  edit reply <query> <text>     append an indented reply under the first item whose
                                raw line or text contains <query>
  edit status <query> <state>   set an item's checkbox:
                                open|wip|done|blocked|none → [ ]|[>]|[x]|[?]|plain
  edit log <text>               append "- HH:MM claude: <text>" to Log
                                (--as <author> overrides the author)
  edit title <text>             set the frontmatter title ("" clears it)
  edit replace <old> <new>      exact first-occurrence string replace over the raw
                                file (escape hatch; errors if <old> not found)

Flags (any subcommand):
  --board <path> | --session <id>   the target board (exactly one)
  --refresh-snapshot <path>         copy the new content over <path> inside the
                                    same lock (monitor self-edit suppression)

Why the lock: the user edits the file concurrently, so writes serialize on an
exclusive flock of the sidecar "<board>.lock" (not the board — it is replaced by
atomic rename each save). Do NOT hand-roll this; use the CLI.`,
}

// runDocs implements `session-notes docs [topic]`: print a topic's recipe, or
// list the available topics when the topic is missing or unknown.
func runDocs(args []string) int {
	if len(args) >= 1 {
		// blurb is dynamic: it is the session-start injection text with the board
		// path substituted. docs has no session context, so use a placeholder path
		// to keep the injected text and the on-demand doc a single source.
		if args[0] == "blurb" {
			fmt.Print(hooks.Blurb("<board-path>"))
			return 0
		}
		if body, ok := docTopics[args[0]]; ok {
			fmt.Println(body)
			return 0
		}
		fmt.Fprintf(os.Stderr, "unknown docs topic: %s\n\n", args[0])
	}
	fmt.Fprintln(os.Stderr, "usage: session-notes docs <topic>")
	fmt.Fprintln(os.Stderr, "topics: "+strings.Join(docTopicNames(), " "))
	// Listing is a valid, useful response; exit 0 when no topic was requested so
	// scripts can call `docs` to discover topics without tripping on a nonzero.
	if len(args) == 0 {
		return 0
	}
	return 2
}

// docTopicNames returns the topic names in a stable, meaningful order.
func docTopicNames() []string {
	order := []string{"protocol", "monitor", "conflicts", "cli", "blurb"}
	// Guard against a topic being added to the map but not the order list.
	seen := map[string]bool{}
	for _, t := range order {
		seen[t] = true
	}
	var extra []string
	for t := range docTopics {
		if !seen[t] {
			extra = append(extra, t)
		}
	}
	sort.Strings(extra)
	return append(order, extra...)
}
