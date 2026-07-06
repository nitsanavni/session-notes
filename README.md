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
│ u undo · ctrl+r redo · L log · r reload · ? help · q quit                │
└───────────────────────────────────────────────────────────────────────┘
```

The `▸` marks the active section; the highlighted row is the cursor. Urgent
(`!!`) items are highlighted, done (`[x]`) items are dimmed.

## Install

```
./install.sh            # hooks scoped to the current project (./.claude/settings.json)
./install.sh --global   # hooks for all sessions (~/.claude/settings.json)
```

This will:

1. Build the binary with the Go toolchain at `~/.local/go/bin/go` and install it to
   `~/.local/bin/session-notes` (warns if `~/.local/bin` isn't on your `PATH`).
2. Create `~/.claude/boards/{panes,.state}` (board data is always kept centrally).
3. Merge the `SessionStart` / `SessionEnd` / `UserPromptSubmit` hooks into the chosen
   settings.json — project-level `./.claude/settings.json` by default, so only sessions
   in that repo get boards; `--global` for `~/.claude/settings.json`. The existing file
   is backed up first (`settings.json.bak.<timestamp>`). Safe to re-run — it won't
   duplicate entries.

The installer prints a tmux `bind-key` line for you to add yourself (it never edits
`~/.tmux.conf` automatically):

```tmux
bind-key g display-popup -E -w 80% -h 80% "session-notes --pane '#{pane_id}'"
```

Add that to `~/.tmux.conf`, then reload it:

```
tmux source-file ~/.tmux.conf
```

With that in place, `prefix + g` pops open the board for whichever Claude Code
session is running in the active pane.

## Board format

Each session gets a Markdown file at `~/.claude/boards/<session-id>.md`: YAML
frontmatter (`session`, `cwd`, `started`) followed by fixed `##` sections — Plan,
Threads, Questions, Ideas, Log.

```markdown
---
session: abc-123
cwd: /home/nitsan/code/foo
started: 2026-07-06T21:30:00+03:00
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

  In the TUI, `R` replies to the item under the cursor; deleting an item removes
  its whole reply thread with it.
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

An item's text can contain `[[name]]` to link to a side markdown file for
longer-form content that doesn't belong inline in the board — a design writeup,
a full error dump, Claude's detailed answer to a question. `[[name]]` renders
in a distinct color in the TUI (display only; the file text is untouched).

Linked files live at `~/.claude/boards/<session-id>.notes/name.md` (next to the
board itself, scoped to the session). In the TUI, `o` opens the item's first
link in `$EDITOR` (same suspend-and-resume as `E`), creating the notes
directory and the file — seeded with a `# name` heading — if it doesn't exist
yet. `o` is a no-op on items with no links.

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

## Keybindings (TUI)

| Key           | Action                                  |
|---------------|------------------------------------------|
| `j` / `k`     | move cursor down / up                    |
| `tab` / `shift-tab` | next / previous section             |
| `1` … `9`     | jump to the Nth section                   |
| `a`           | add item to current section              |
| `A`           | add sections (multi-select overlay)      |
| `R`           | reply to item (threaded sub-bullet)      |
| `space`       | cycle status `[ ] → [>] → [x]`           |
| `!`           | toggle urgent (`!!`)                     |
| `e`           | edit item inline (the bullet line only)  |
| `E`           | open board in `$EDITOR` (suspends TUI)   |
| `o`           | open item's first `[[link]]` in `$EDITOR` (suspends TUI) |
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
side ever reads a torn file.

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
reprioritize a thread) — not just at the next prompt boundary.

## Footprint — what lives outside this repo

Everything the tool creates or touches outside the repo, and why:

| Path | What | Why there |
|------|------|-----------|
| `~/.local/bin/session-notes` | the installed binary | on `PATH` for tmux popups and hooks from any directory |
| `~/.claude/boards/<id>.md` | one board per session | central so boards survive repo moves and the picker can list all sessions |
| `~/.claude/boards/<id>.notes/` | `[[linked]]` side notes | long-form content stays next to its board, not in your project |
| `~/.claude/boards/panes/*.json` | tmux pane → session mapping | written by `session-start`, read by `prefix+g`; must be findable from outside any repo |
| `~/.claude/boards/.state/` | per-session `last-seen` markers | lets `prompt-submit` tell Claude "the board changed" without touching the board |
| `./.claude/settings.json` (or `~/.claude/settings.json` with `--global`) | the three hooks | this is where Claude Code looks for hooks; project scope by default so only opted-in repos get boards |
| `settings.json.bak.<timestamp>` | installer backups | written next to the settings file before every merge |
| `~/.tmux.conf` | one `bind-key` line | added by you (the installer only prints it) |
| `~/.local/go/` | Go toolchain | build dependency only; not touched at runtime |

Nothing else: no daemons, no databases, no shell profile edits. Uninstall =
delete the binary, the hooks block in settings.json, the tmux bind line, and
(if you want the data gone) `~/.claude/boards/`.
