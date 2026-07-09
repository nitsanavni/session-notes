# session-notes

A shared scratchpad for a Claude Code session — one board per session, viewed and
edited by you in a tmux popup, and read/maintained by Claude during the session via
hooks. Threads of work, open questions, a plan, ideas, and a running log, kept in a
plain Markdown file that both sides can safely edit at the same time.

## What it looks like

```
┌─ session-notes ─ abc-123 ─ ~/code/foo ───────────────────────────────────┐
│ Plan                                                                     │
│   [ ] step one                                                          │
│                                                                          │
│ ▸ Threads                                                                │
│   [>] auth refactor — extracting middleware                             │
│   [x] fix flaky test                                                    │
│                                                                          │
│   Questions                                                              │
│   [ ] !! drop the legacy endpoint? @user                                │
│     - user: yes, let's drop it                                          │
│     - claude: done in abc123                                            │
│                                                                          │
│   Ideas                                                                  │
│   - cache invalidation could be event-driven                            │
│                                                                          │
│   Log                                                                    │
│   21:30 start: session started in /home/nitsan/code/foo                 │
│   21:42 claude: finished thread "fix flaky test"                        │
├───────────────────────────────────────────────────────────────────────┤
│ j/k move · tab section · a add · R reply · space status · ! urgent ·    │
│ d archive · D delete · enter expand · e edit · E $EDITOR · o open link ·│
│ y copy path · u undo · ctrl+r redo · L log · m map · r reload · ? · q    │
└───────────────────────────────────────────────────────────────────────┘
```

The `▸` marks the active section; the highlighted row is the cursor. Urgent
(`!!`) items are highlighted, done (`[x]`) items are dimmed.

`m` toggles the mindmap view: the board as a center-outward map, sections as
the first ring. `hjkl` move focus tree-structurally — `j`/`k` walk siblings on
your side of the center (the node visually above/below), `h`/`l` cross the
parent/child axis toward and away from the center. Long node text is truncated
to 40 columns with a `…` (the full text shows in the footer); `w` expands the
focused node in place as a wrapped multi-line block. `enter` cycles the focused
node's fold — collapsed → default → replies-shown → collapsed: each press reveals
more until the whole subtree is visible, then one more collapses it to a single
`[+N]` (or `[N replies]`) summary of everything hidden. When a move surprises you (focus
didn't land where your fingers expected), press `!`: a prompt shows what just
happened ("you hit k · focus moved 'Log' → 'Working Agreements' — where did you
expect it?") and your note is appended to `<board>.feedback.jsonl` along with
the last 20 map actions, each with a replayable before-state. Browse the log
with `session-notes feedback <board.md>` (or `--json`); `--gen-test` prints
ready-to-paste Go test source that replays each surprising move and asserts
where it lands today, with a TODO for what you expected — flip one line to turn
it into a failing test.

## Install

```
./install.sh            # hooks scoped to the current project (./.claude/settings.json)
./install.sh --global   # hooks for all sessions (~/.claude/settings.json)
./install.sh --download # skip the build, install a prebuilt release binary
```

This will:

1. Build the binary with the Go toolchain (`~/.local/go/bin/go` or `go` on your
   `PATH`) and install it to `~/.local/bin/session-notes` (warns if `~/.local/bin`
   isn't on your `PATH`). No Go? It downloads a prebuilt binary from the
   [latest GitHub release](https://github.com/nitsanavni/session-notes/releases/latest)
   instead (linux/macOS, amd64/arm64).
2. Create `~/.claude/boards/{panes,.state}` (board data is always kept centrally).
3. Merge the `SessionStart` / `SessionEnd` / `UserPromptSubmit` hooks into the chosen
   settings.json — project-level `./.claude/settings.json` by default, so only sessions
   in that repo get boards; `--global` for `~/.claude/settings.json`. The existing file
   is backed up first (`settings.json.bak.<timestamp>`). Safe to re-run — it won't
   duplicate entries.

The installer prints a tmux `bind-key` line for you to add yourself (it never edits
`~/.tmux.conf` automatically):

```tmux
# popup (dismiss with q)
bind-key g display-popup -E -w 80% -h 80% "session-notes --pane '#{pane_id}'"
# persistent side pane: board on the right of the claude pane
bind-key G split-window -h -l 40% "session-notes --pane '#{pane_id}'"
# ...or below it
bind-key C-g split-window -v -l 30% "session-notes --pane '#{pane_id}'"
# dashboard: every live session for this project on one screen
bind-key D display-popup -E -w 90% -h 90% "session-notes --dash"
```

Add those to `~/.tmux.conf`, then reload it:

```
tmux source-file ~/.tmux.conf
```

`prefix + g` pops open the board for whichever Claude Code session is running in
the active pane. `prefix + G` (or `C-g`) opens the same board as a real split
pane instead — it live-reloads on Claude's edits, so you can keep it open beside
the session as a permanent dashboard; `q` closes it and returns the space.
`prefix + D` opens the multi-session **Dashboard** (see below): every live
session in the current project on one screen.

### Cutting a release (maintainers)

Push a `v*` tag and the `release.yml` workflow builds linux/darwin ×
amd64/arm64 tarballs and attaches them (plus sha256 checksums) to a GitHub
Release:

```
git tag v0.1.0 && git push origin v0.1.0
```

## Board format

Each session gets a Markdown file at `~/.claude/boards/<session-id>.md`: YAML
frontmatter (`session`, `cwd`, `started`, and an optional `title`) followed by
fixed `##` sections — Waiting on User, Plan, Threads, Questions, Ideas, Parked,
Working Agreements, Log (an Archive section is created on demand by the first
archive action, just above Log).

```markdown
---
session: abc-123
cwd: /home/nitsan/code/foo
started: 2026-07-06T21:30:00+03:00
title: auth refactor
---

## Plan
- [ ] step one

## Threads
- [>] auth refactor — extracting middleware
- [x] fix flaky test

## Questions
- [ ] !! drop the legacy endpoint? @user

## Ideas
- cache invalidation could be event-driven

## Log
- 21:30 start: session started in /home/nitsan/code/foo
- 21:42 claude: finished thread "fix flaky test"
```

Conventions (parsed leniently — lines that don't match are kept verbatim, never
destroyed):

- **Title** (`title:` in the frontmatter): an optional short human name for the
  session — "auth refactor", not a uuid. When set, the board header and the
  dashboard/picker show it in place of the session id (the header also tags on a
  shortened session id, first 8 chars, dimmed, so the id stays visible in both
  the list and map views). Claude is nudged (by the
  `session-start` blurb) to set one early. Any frontmatter keys the tool doesn't
  model are still preserved round-trip.
- **Status**: `[ ]` open, `[>]` in progress, `[x]` done, `[?]` blocked. Plain `- `
  items are fine in Ideas/Log, which don't track status.
- **Urgency**: a leading `!!` in the item text. Checking the box or removing `!!`
  acknowledges it.
- **Addressing**: `@claude` / `@user` anywhere in the text.
- **Replies (threads)**: an item indented two spaces under another item is a
  forum-style reply to it. Rather than editing an item's text to answer it, add a
  sub-bullet reply — usually `- author: text` — beneath it. Replies can nest
  arbitrarily deep (two spaces per level) and may themselves carry checkboxes:

  ```markdown
  ## Questions
  - [ ] drop the legacy endpoint? @user
    - user: yes, let's drop it
    - claude: done in abc123
      - user: thanks
  ```

  In the TUI, `R` replies **in thread**: on a reply it appends a sibling (the
  conversation stays flat); on a top-level item it starts the thread. `F` forks
  a nested sub-thread under the exact message at the cursor. Deleting an item
  removes its whole reply thread with it.
- **Continuation lines (multi-line bullets)**: a line indented deeper than a
  bullet but *not* starting with `- ` is continuation content of that bullet —
  extra prose, a code snippet, or an ASCII diagram. It stays attached to its
  bullet:

  ```markdown
  ## Threads
  - [>] draw the pipeline
        +---------+      +---------+
        |  input  | ---> | process |
        +---------+      +---------+
    the left box is the ingest stage
  ```

  In the TUI, a bullet and its continuation lines render together as one visual
  block and count as a **single cursor stop**. Continuation lines render verbatim
  (monospace, internal spacing preserved) — ASCII drawings are never re-wrapped or
  trimmed; lines wider than the viewport clip at the right edge rather than wrap.
  `d` deletes the bullet together with its continuation block and reply thread;
  `e` edits only the bullet line itself. Authoring multi-line content is done via
  `E` (`$EDITOR`); there is no multi-line inline editor. Long single lines
  (bullets, replies, log entries) soft-wrap to the viewport width, with wrapped
  rows hanging-indented under where the text starts.
- **Log**: append-only, one line per entry, `- HH:MM author: text`.
- **Archive**: a `## Archive` section holds items you've retired with `d` instead
  of deleting them. It's created on first use, placed just above `## Log` (or at
  the end if there's no Log). Archiving moves an item — with its whole reply
  thread and verbatim continuation lines — to the end of Archive, unchanged;
  archiving a whole section moves all its items there and removes the now-empty
  section. Nothing is destroyed, so a `d` is always recoverable by editing the
  file. In the TUI the Archive section is collapsed by default (it shows a
  `(N archived)` count); `enter`/`l` on its header toggles it open. Use `D` for a
  true delete that removes content from the file.

### Linked notes

An item's text can contain a `[[link]]` for longer-form content that doesn't
belong inline in the board — a design writeup, a full error dump, Claude's
detailed answer to a question. Links come in two forms and both render in a
distinct color in the TUI (display only; the file text is untouched):

- `[[name]]` (a plain name, no slash) is a **side note**, living at
  `~/.claude/boards/<session-id>.notes/name.md` (next to the board itself,
  scoped to the session). Opening a missing side note creates it — seeded with a
  `# name` heading.
- `[[path/with/slash.md]]` (contains a `/`, or starts with `~/` or `/`) is a
  **file path**, resolved relative to the session cwd (`~` expanded, absolute
  paths as-is). Use this to link real repo files like `[[docs/foo.md]]`. Opening
  a path that doesn't exist shows an error rather than creating a stub.

In the TUI, `o` opens the item's first link in `$EDITOR` (same
suspend-and-resume as `E`); with several links a chooser picks which one. `o` is
a no-op on items with no links.

Claude is expected to write its long-form answers into these files rather than
bloating the board itself — e.g. drop `[[legacy-endpoint-audit]]` into a
Questions item and put the full analysis in
`~/.claude/boards/<session-id>.notes/legacy-endpoint-audit.md`.

## Session resolution

Because you may have several Claude Code sessions running concurrently (different
tmux panes, different repos, or several panes in the same repo), `session-notes`
needs to figure out which board you mean:

1. `--board <path>` — explicit path, always wins.
2. `--pane <tmux-pane-id>` — looks up `~/.claude/boards/panes/<pane-id>.json`,
   which maps that pane to the board of the session currently running there. The
   tmux keybind passes `#{pane_id}` of the active pane, so each pane resolves to
   its own session independently.
   If a pane has no mapping (or its board file is gone), `session-notes` falls
   back to the newest board whose `cwd` matches the pane's current directory
   (asked from tmux) before showing the picker — so an unmapped pane opens the
   project's latest board instead of erroring out.
3. `$TMUX_PANE` — same pane-mapping lookup, using the environment variable
   instead of a flag (used when running `session-notes` with no `--pane`, inside
   tmux).
4. Fallback — if exactly one board's `cwd` matches the current directory, use it.
5. Otherwise — a picker listing all boards (session id, cwd, mtime, first
   in-progress thread), newest first.

If you run several sessions sequentially in the same pane, each new session's
`SessionStart` hook overwrites that pane's mapping, so the popup always shows the
latest session for that pane. Sessions started outside tmux still get a board;
they just aren't reachable via `--pane` (use the cwd fallback or the picker
instead).

## Dashboard

When you run **many** Claude Code sessions in parallel on one project — several
panes, background agents, a couple of experiments — picking one board from a list
is the wrong tool. The dashboard puts every live session on a single screen:

```
session-notes --dash        # this project (boards whose cwd == current dir)
session-notes -d            # same, short flag
session-notes --dash --all  # every project's sessions
```

Or bind it in tmux (`prefix + D`, see Install).

One card per board, sorted most-recently-active first:

```
session dashboard   this project · 3 live

> ● auth refactor        16s
      [>] extracting middleware
      !! drop the legacy endpoint? @user
      21:42 claude: finished thread "fix flaky test"

  ○ flaky-test hunt      7m
      [>] bisecting CI failures

  ✗ old spike            3h
      12:01 end: session ended
```

Each card shows a **liveness dot**, the session's **title** (or a short session
id — set `title:` in the frontmatter, see below), and a **relative activity
time**; beneath it, up to three in-progress `[>]` threads, any open `!!` urgent
items, and the last Log line.

- **Liveness** comes from the session's transcript. Every Claude Code session
  appends to `~/.claude/projects/<munged-cwd>/<session-id>.jsonl` as it works, so
  that file's mtime is a reliable "still alive" signal without any cooperation
  from the session itself. The dashboard classifies it: `●` **active** (touched
  in the last 2 minutes), `○` **idle** (last 2 hours), `✗` **gone** (older, or no
  transcript). The projects root is read-only to this tool; it is never written.
- **Live**: the dashboard rescans boards and liveness every 2 seconds, and also
  reacts to board-file changes via the filesystem watcher.
- **Keys**: `j`/`k` move between cards · `enter` opens that board in the normal
  board view (`q`/`esc` there returns to the dashboard, not quit) · `r` rescans ·
  `q`/`esc` quits.

## Keybindings (TUI)

| Key           | Action                                  |
|---------------|------------------------------------------|
| `j` / `k`     | move cursor down / up                    |
| `tab` / `shift-tab` | next / previous section             |
| `1` … `9`     | jump to the Nth section                   |
| `a`           | add item to current section              |
| `A`           | add sections (multi-select overlay)      |
| `R`           | reply in thread (flat sibling on a reply) |
| `F`           | fork a sub-thread under the cursor        |
| `space`       | cycle status `[ ] → [>] → [x]`           |
| `!`           | toggle urgent (`!!`)                     |
| `e`           | edit item inline (the bullet line only)  |
| `E`           | open board in `$EDITOR` (suspends TUI)   |
| `o`           | open item's first `[[link]]` in `$EDITOR` (suspends TUI) |
| `y`           | copy board file path to clipboard (shown in status) |
| `L`           | quick log entry                          |
| `d`           | archive item / section (into `## Archive`)|
| `D`           | hard-delete item / section from the file |
| `enter` / `l` | expand / collapse Archive (on its header)|
| `u`           | undo last change (up to 100)             |
| `ctrl+r`      | redo                                     |
| `r`           | reload from disk                         |
| `q` / `esc`   | quit                                     |
| `?`           | help                                     |

Section headers are cursor stops too: pressing `d` on a header archives the whole
section (its items move to `## Archive` and the empty section is removed); `D`
hard-deletes it. The `Archive` and `Log` sections themselves can't be archived.

The board file is watched for external changes and re-rendered live, so edits
Claude makes mid-session show up in the popup without you doing anything. All
writes (from the TUI or from Claude) are atomic (temp file + rename) so neither
side ever reads a torn file, and they are serialized by an advisory lock so
neither side clobbers the other (see How Claude participates). If Claude writes
while you're mid-edit in an inline input, the TUI rebases your edit onto Claude's
change on save instead of overwriting it — your text is never lost.

## How Claude participates

Claude doesn't run the TUI — it reads and edits the board file directly, driven by
three hooks that `install.sh` wires up in a Claude Code settings.json (project or
global, see Install):

- **`hook session-start`** — creates the board (if this session doesn't have one
  yet) and, inside tmux, records the pane-to-session mapping. It prints a short
  protocol blurb to stdout, which Claude Code feeds into the model's context: the
  board's path, and instructions to keep Plan/Threads up to date as it works,
  answer or raise items in Questions, append a Log line on milestones, and watch
  the board for live user edits with the `Monitor` tool.
- **`hook prompt-submit`** — runs before each prompt is processed. If the board
  has unacknowledged `!!` (urgent) items, it prints them so they're injected into
  Claude's context on the spot, rather than waiting for Claude to notice them on
  its own. It also prints a one-line notice if you've edited the board since
  Claude last looked (tracked via `~/.claude/boards/.state/<session-id>.last-seen`).
- **`hook session-end`** — appends an `end` line to the Log and cleans up the pane
  mapping if it still points at this session.

All three hooks are designed to be fast and never break your session: on any
internal error they exit `0` silently rather than failing the hook.

Beyond the hooks, Claude is expected to use the `Monitor` tool during the session
to react promptly when you edit the board live (e.g. you drop a `!!` question or
reprioritize a thread) — not just at the next prompt boundary. To avoid the watch
echoing Claude's own edits back at it, the recommended setup has Claude refresh
the watch's snapshot inside the same locked write it uses to edit the board, with
the watcher diffing under the same lock — then only your edits emit events.

**Write protocol (avoiding lost updates).** You and Claude edit the same file
concurrently, so every writer serializes on an advisory lock. Before any
read-modify-write of the board, take an exclusive `flock` on the sidecar lock
file `<board-path>.lock` (e.g. `~/.claude/boards/<id>.md.lock`) — *not* on the
board itself, since the board is replaced by an atomic rename each save and a lock
on its inode would not exclude a racing writer. Hold the lock across the whole
read → modify → temp-file → rename, then release it. The TUI's saves and the
hooks already do this via `board.WithLock`; any external writer (including
Claude's own file edits) must follow the same convention. Readers need no lock —
the atomic rename keeps reads torn-free. The TUI additionally rebases an in-flight
inline edit onto concurrent external writes rather than clobbering them.

## Footprint — what lives outside this repo

Everything the tool creates or touches outside the repo, and why:

| Path | What | Why there |
|------|------|-----------|
| `~/.local/bin/session-notes` | the installed binary | on `PATH` for tmux popups and hooks from any directory |
| `~/.claude/boards/<id>.md` | one board per session | central so boards survive repo moves and the picker can list all sessions |
| `~/.claude/boards/<id>.notes/` | `[[linked]]` side notes | long-form content stays next to its board, not in your project |
| `~/.claude/boards/panes/*.json` | tmux pane → session mapping | written by `session-start`, read by `prefix+g`; must be findable from outside any repo |
| `~/.claude/boards/.state/` | per-session `last-seen` markers | lets `prompt-submit` tell Claude "the board changed" without touching the board |
| `~/.claude/projects/<munged-cwd>/*.jsonl` | Claude Code session transcripts | **read-only** — the dashboard uses each transcript's mtime as its liveness signal; never written or modified |
| `$SESSION_NOTES_PROJECTS_DIR` | env override for the transcripts root above | defaults to `~/.claude/projects`; point it elsewhere for tests or non-standard installs |
| `$SESSION_NOTES_DIR` | env override for the boards directory | defaults to `~/.claude/boards` |
| `./.claude/settings.json` (or `~/.claude/settings.json` with `--global`) | the three hooks | this is where Claude Code looks for hooks; project scope by default so only opted-in repos get boards |
| `settings.json.bak.<timestamp>` | installer backups | written next to the settings file before every merge |
| `~/.tmux.conf` | one `bind-key` line | added by you (the installer only prints it) |
| `~/.local/go/` | Go toolchain | build dependency only; not touched at runtime |

Nothing else: no daemons, no databases, no shell profile edits. Uninstall =
delete the binary, the hooks block in settings.json, the tmux bind line, and
(if you want the data gone) `~/.claude/boards/`.
