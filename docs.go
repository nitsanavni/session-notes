package main

//go:generate go run ./internal/keymap/cmd/genkeys

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/nitsanavni/session-notes/internal/hooks"
	"github.com/nitsanavni/session-notes/internal/keymap"
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
  >15 min since the last injection; override per board with a "pin-cadence:"
  frontmatter key in Go duration syntax, e.g. "pin-cadence: 10m" / "90s" / "1h").
  Use it for working agreements / standing instructions that should survive long
  sessions and compaction. Prompt-submit rarely fires when Claude's turns come
  from a file-watcher monitor, so a monitor can run "session-notes pins --due
  --board <path>" on each cycle to resurface pins on the same cadence.

[[links]] come in two forms, both opened with "o" in the TUI:
- [[name]] (no slash) is a SIDE NOTE at <boards-dir>/<session-id>.notes/name.md.
  Write longer content (designs, scoping, research) there; opening a missing one
  creates it.
- [[path/with/slash.md]] (has a slash, or starts with ~/ or /) is a FILE PATH,
  resolved relative to the session cwd (~ expanded, absolute as-is). Use it to
  link real repo files; opening a missing path shows an error, no stub.

Preserve any content you don't understand; edit surgically.`,

	"monitor": `Monitor — react to the user's live board edits

The user edits the board in a TUI while you work. Watch the file so you react at
edit time (a dropped "!!" question, a reprioritized thread), not just at the next
prompt boundary. Use the first-class watch command instead of a hand-rolled loop
— it takes the board lock around its compare-and-snapshot cycle, emits the change
as a unified item diff, and also watches the board's .notes/ sibling:

  session-notes watch --board BOARD --snapshot snap --once

Run that in a background task / Monitor tool. It blocks until the user changes the
board, prints the changed items (removed "-", added "+") to stdout, then exits 0.
React to the diff, then run it again to re-arm. It polls (default every 2s;
--interval to change); pass --no-notes to skip the .notes/ dir. Omit --once to
stream every change from one long-running process instead.

Self-edit suppression: to keep the watch from firing on YOUR OWN edits, pass the
SAME snapshot path to every "session-notes edit" call as "--refresh-snapshot snap".
It copies the new content over the snapshot INSIDE THE SAME LOCK, right after the
atomic replace, so the watcher (which reads board+snapshot under that same lock)
only ever emits the user's edits, never yours.

In-band delivery: before applying, an edit with --refresh-snapshot diffs the
snapshot against the pre-edit board (still inside the lock) and prints any
not-yet-delivered external change to stdout in the same "-"/"+" diff shape the
watch emits. So a user edit that lands while your watch is between --once
re-armings is handed to you by your own write instead of being silently
snapshotted as seen — READ THE STDOUT OF YOUR EDIT CALLS; non-empty output is
a delivery, react to it like a watch diff.

Delivery receipts: the web UI reads the DEFAULT snapshot path ("<board>.watch")
and marks items whose change has not reached the snapshot with a pending "◌",
flipping to a brief "✓" when a delivery advances it — the user literally sees
whether you have been handed their edit. Use the default snapshot path (omit
--snapshot, pass "<board>.watch" to --refresh-snapshot) so receipts light up.

  session-notes edit reply "frobnicator" "claude: rewired it" \
    --board BOARD --refresh-snapshot snap

Appendix — the old manual loop (equivalent, if you can't use the binary):

  cp BOARD snap
  while true; do
    sleep 1
    cmp -s BOARD snap && continue
    diff -U0 snap BOARD | grep -E '^[+-]' | grep -vE '^(\+\+\+|---) '
    cp BOARD snap
  done`,

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
(resolved to <boards-dir>/<id>.md). --board also accepts a remote board URL
(http(s)://host/b/<board>[#<node>]) — edit/watch treat it like a local file.

Board resolution priority (edit + watch), highest first:
  1. explicit --board <ref> (or --session <id>)
  2. the directory's linked board: "session-notes link <ref>" pins a default
     board for the cwd (walks up parent dirs, git-style). A #<node> in a local
     linked ref becomes the default --root (edit) / --node (watch).
  3. otherwise the command errors "a board is required".
An explicit flag ALWAYS wins over a link. Link once, then run bare commands:
  session-notes link ./board.md          # or a remote URL, or …/b/board#<node>
  session-notes edit add Threads "hi"    # no --board needed
  session-notes watch --once             # ditto
  session-notes link --show              # print the effective ref + its source
  session-notes unlink                   # drop the cwd's link

Every write is SILENT on success — unless
--refresh-snapshot finds external changes the monitor has not delivered yet
(see below) — and exits non-zero with a one-line reason on failure.

  edit add <section> <text>     append a top-level item to an existing section
                                (errors, listing sections, if it is missing)
  edit reply <query> <text>     reply in-thread under the first item whose raw
                                line or text contains <query>. Replying to a
                                reply continues the conversation FLAT (appended
                                to the thread's parent) — same semantics as the
                                TUI's R and the web UI
  edit fork <query> <text>      nest a sub-thread under the exact matched item
                                (the TUI's F) — use when forking a side topic
  edit status <query> <state>   set an item's checkbox:
                                open|wip|done|blocked|none → [ ]|[>]|[x]|[?]|plain
  edit log <text>               append "- HH:MM claude: <text>" to Log
                                (--as <author> overrides the author)
  edit title <text>             set the frontmatter title ("" clears it)
  edit replace <old> <new>      exact first-occurrence string replace over the raw
                                file (escape hatch; errors if <old> not found)
  edit undo | edit redo         walk the board's shared undo journal (last 100
                                journaled edits — yours and the web UI's, one
                                timeline). Refuses if the board changed through
                                a non-journaling writer (TUI, $EDITOR) since.

Addressing by node id (stable across edits): every bullet carries a trailing
" ^<id>" anchor once it has been saved through the CLI. Prefer it over <query>
when you have it — it survives text edits and reparenting:
  edit set --id <id> <text>     replace an item's text (id-native "replace")
  edit reply|fork|status --id <id> …   address the exact node by id

Flags (any subcommand):
  --board <path> | --session <id>   the target board (exactly one)
  --id <id>                         address the target node by its ^<id> anchor
  --root <id>                       CARVE-OUT: confine all addressing to the
                                    subtree rooted at that node; edits targeting
                                    anything outside it are refused. add/reply/
                                    fork default their target/parent to the root.
                                    See "docs subtree".
  --refresh-snapshot <path>         copy the new content over <path> inside the
                                    same lock (monitor self-edit suppression).
                                    First prints any external change the watch
                                    has not delivered yet (snapshot vs pre-edit
                                    board, same "-"/"+" shape) so a concurrent
                                    user edit is never silently marked seen —
                                    treat non-empty stdout as a watch delivery

Why the lock: the user edits the file concurrently, so writes serialize on an
exclusive flock of the sidecar "<board>.lock" (not the board — it is replaced by
atomic rename each save). Do NOT hand-roll this; use the CLI.

Bootstrap:
  init --board <path>           seed a fresh board from the canonical template
                                (--session <id>, --cwd, --title) — for
                                environments where no session-start hook runs;
                                refuses to overwrite (see docs headless)

Read-side helpers (not writes):
  history --board <p> [-n N]    print the board's journaled edits as compact
                                line-diffs, oldest first, stamped time+author.
                                THE CATCH-UP COMMAND: after context compaction,
                                read this instead of re-deriving what changed.
  pins [--due] --board <p>      print the board's pinned-item block. Plain: always
                                print (silent when no pins). --due: print only when
                                due on the re-injection cadence (default 15m, or the
                                board's "pin-cadence:" frontmatter) and update the
                                same state the prompt-submit hook uses. A file-watch
                                monitor can call "pins --due" each cycle to resurface
                                pins even when no user prompt fires.

Session status sidecar (not a write you make):
  hook statusline               wired as Claude Code's statusLine command. Reads the
                                statusLine JSON on stdin and records the live model,
                                context-window %, and cost into
                                <boards-dir>/.state/<session>.status, printing a
                                compact "<model> | ctx NN%" line. The TUI footer and
                                web header show model + a context bar + activity from
                                that sidecar while it's fresh (hidden after 5m stale).`,

	"headless": `Headless / remote environments — no tmux, no hooks, no inbound net

Sandboxed agent environments (Claude Code on the web, CI, containers) usually
have no tmux, no installed hooks, and no way for a human to reach a port the
sandbox binds. The whole protocol still works — it is a file plus this CLI.

Bootstrap (no hook ran, so no board exists yet):

  session-notes init --board <path> [--title <t>]

seeds the canonical sections and prints the path (refuses to overwrite an
existing board; --session <id> / --cwd override the frontmatter defaults).
Every command accepts --board <path>, so the file can live anywhere; set
$SESSION_NOTES_DIR to give --session <id>, the picker, the dashboard, and
serve a boards directory to resolve against.

The loop, spelled out:
- WRITE through "session-notes edit …" (docs cli) — never edit the file
  directly; the CLI takes the same lock every other writer uses.
- WATCH with "session-notes watch --board B --snapshot S" (docs monitor) in a
  background task, and pass --refresh-snapshot S on your own edits so the
  watch fires only for other writers.
- CATCH UP after context compaction with "session-notes history --board B".

Web UI in a sandbox:
- "session-notes serve" binds 127.0.0.1:7080; non-loopback binds require
  --token <t> (or $SESSION_NOTES_TOKEN). If the environment accepts no
  inbound connections, a human cannot browse to the sandbox's server — drive
  it yourself with a headless browser when you need the web surface, and
  point the human at running "serve" on their own machine instead (the UI is
  touch-friendly, so a phone on their network works).`,

	"subtree": `Subtree attach — hand a sub-agent one node as its whole world

Zoom is a first-class primitive: any node id (` + "`^<id>`" + ` anchor) is an
addressable, watchable, editable root. Handing a sub-agent a single node as its
carve-out gives it exactly that node's subtree to read, react to, and edit —
and nothing else. This is the permission boundary: the parent keeps the rest of
the board; the child cannot even see, let alone touch, a sibling subtree.

Pick the root: any bullet's trailing " ^<id>" anchor (ids are stamped on the
first CLI save; run any edit once if the board has none yet). A "## Heading
^<id>" section anchor works as a root too.

Watch just the subtree — only changes inside it are reported; a deleted root
prints "node gone" and exits nonzero under --once so the child knows to stop:

  session-notes watch --board BOARD --node <id> --once
  session-notes watch --node BOARD#<id> --once     # canonical <board>#<id> ref

Edit within the subtree — every addressing path (id, <query>, section+raw)
resolves inside the root only; a query cannot match a sibling, and an --id
outside the root is refused. add/reply/fork default their target to the root,
so the child rarely needs to name it:

  session-notes edit add   --board BOARD --root <id> "a child of the root"
  session-notes edit reply --board BOARD --root <id> "reply under the root"
  session-notes edit set   --board BOARD --root <id> --id <child-id> "new text"
  # editing a node outside <id> exits non-zero: "target is outside the subtree"

Whole-board ops (title, log, section add/delete) are refused under --root.

Spawn pattern: give the sub-agent BOARD + the node id and these two commands.
It watches --node to react to changes in its slice and edits --root to write
back, exactly as the top-level agent uses the whole board — same protocol, one
node deep. Views follow: the TUI map zoom (f/b) and the web #<node-id> URL
fragment both re-root on the same node id, so a zoomed view is shareable.

Authenticated carve-out (cloud) — real auth, one node as the world

On a ` + "`session-notes server`" + ` the carve-out becomes an enforced permission,
not a convention. A grant binds a token to (board, node, read|write): a
subtree-scoped token literally cannot fetch, watch, or edit a sibling — the
server forces every op's root to the granted node, scopes the SSE stream, and
serves a board view containing only that subtree.

Mint a scoped token and print an attach line in one step (admin token required,
stored by ` + "`session-notes login`" + `):

  session-notes remote grant https://host/b/BOARD#<id> --new-token scout --perm write
  # prints:
  #   granted subtree #<id> (write) to "scout"
  #   attach: session-notes login https://host --token <T> && \
  #           session-notes edit --board 'https://host/b/BOARD#<id>'
  #   token: <T>

Paste the attach line into the sub-agent. It now watches and edits that node as
its whole world with real auth behind it — hand an agent one node, nothing else
reachable. Manage grants with ` + "`remote grants <url>/b/BOARD`" + ` (list) and
` + "`remote revoke <url>/b/BOARD --token-name scout`" + `. Grant an existing token's
identity instead of minting with ` + "`--token-name`" + `.

Simpler sub-agent loop — link once, then bare commands. After login, pin the
scoped ref for the working dir so every command drops --board:

  session-notes login https://host --token <T>
  session-notes link 'https://host/b/BOARD#<id>'   # #<id> becomes the default node
  session-notes watch --once --ignore-author scout  # react to OTHERS' edits only
  session-notes edit reply "…" "scout: done"        # writes as subject "scout"

The edit's author defaults to the token's subject server-side (here "scout"), so
` + "`watch --ignore-author scout`" + ` suppresses the agent's own echo: the server drops
any change whose journalled ops are all authored by that name. Edit as subject
S, watch --ignore-author S — no more own-echo wakeups. (Local file watch uses
--snapshot self-edit suppression instead; --ignore-author is the remote path.)`,

	"server": `Cloud server — run session-notes for agents anywhere

` + "`session-notes server`" + ` is the SQLite-backed cloud backend: any Claude session
anywhere attaches to a board (or a granted subtree) with the same watch/edit
protocol as a local file. Tokens carry identity; grants are the access model
(see ` + "`docs subtree`" + ` for the carve-out handoff).

Run it (refuses to start with no tokens unless --insecure; --insecure disables
auth and is refused on any non-loopback --addr — loopback dev only):

  session-notes server --addr 127.0.0.1:7099 --db ./server.db
  session-notes server token create --name ops        # admin bootstrap token, printed once
  session-notes server token list                     # audit: name/subject/admin/created
  session-notes server token revoke <name>            # immediate; next request 401s

Durability layers (details + a full VPS runbook in deploy/README.md):

  - Litestream streams the SQLite db to S3 (deploy/litestream.yml).
  - Nightly sqlite3 VACUUM INTO + retention ladder to a 2nd provider
    (deploy/backup.sh, restic or rclone).
  - Format durability — export every board as re-parseable <id>.md:
      session-notes server export --dir ./boards          # against the db
      session-notes remote pull --all https://host ./boards   # over HTTP (admin)

Operational surface: GET /healthz (no auth, 200 ok) for probes; one stderr log
line per request (method path status duration); POST/PUT bodies over 1 MiB are
refused 413; SIGTERM/SIGINT shut down gracefully (drain SSE, close the db).

Deploy artifacts live in deploy/: Dockerfile + docker-compose.yml (server +
litestream + Caddy TLS), a session-notes.service systemd unit, a Caddyfile, and
the runbook (Hetzner from zero, restore drill, upgrades).`,
}

// runDocs implements `session-notes docs [topic]`: print a topic's recipe, or
// list the available topics when the topic is missing or unknown.
func runDocs(args []string) int {
	if len(args) >= 1 {
		if args[0] == "-h" || args[0] == "--help" {
			fmt.Println("usage: session-notes docs <topic>")
			fmt.Println("topics: " + strings.Join(docTopicNames(), " "))
			return 0
		}
		// blurb is dynamic: it is the session-start injection text with the board
		// path substituted. docs has no session context, so use a placeholder path
		// to keep the injected text and the on-demand doc a single source.
		if args[0] == "blurb" {
			fmt.Print(hooks.Blurb("<board-path>"))
			return 0
		}
		// keys: the keybindings help, rendered from the single source of truth
		// (internal/keymap). `--markdown` emits the README table body used by the
		// go:generate step; the drift test asserts README matches this output.
		if args[0] == "keys" {
			fmt.Print(keymap.Markdown())
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
	order := []string{"protocol", "monitor", "conflicts", "cli", "subtree", "server", "headless", "blurb"}
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
