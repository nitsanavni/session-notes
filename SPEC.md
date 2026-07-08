# session-notes — shared session board for Claude Code

A per-session scratchpad shared between the user and Claude: threads of work, open
questions, plan, ideas, log. User views/edits it in a tmux-popup TUI; Claude reads and
maintains it during the session via hooks + file watching.

## One binary: `session-notes`

- `session-notes` (no args) — open the TUI for the current session's board.
- `session-notes --pane <tmux-pane-id>` — resolve board via pane mapping (used by tmux keybind).
- `session-notes --board <path>` — open a specific board file.
- `session-notes hook session-start` — Claude Code SessionStart hook (JSON on stdin).
- `session-notes hook session-end` — SessionEnd hook.
- `session-notes hook prompt-submit` — UserPromptSubmit hook.

## Data layout

```
~/.claude/boards/
  <session-id>.md          # one board per Claude Code session
  panes/<pane-id>.json     # tmux pane -> session mapping, e.g. panes/%5.json
```

`panes/%5.json`: `{"session_id": "...", "cwd": "...", "board": "/abs/path.md", "started": "RFC3339"}`

## Session resolution (how the TUI finds the right board)

1. `--board` explicit path wins.
2. `--pane <id>`: read `panes/<id>.json`. The tmux keybind passes `#{pane_id}` of the
   active pane, so concurrent sessions in different panes resolve independently.
3. `$TMUX_PANE` env var, same lookup.
4. Fallback: if exactly one board's `cwd` matches the current cwd, use it.
5. Otherwise: picker listing all boards, newest mtime first.

Same pane, sequential sessions: SessionStart overwrites the mapping — latest wins (correct).
Non-tmux sessions still get a board; only the pane mapping is skipped.

## Board format

Markdown, loosely parsed. YAML frontmatter + fixed `##` sections. New boards
are seeded with, in order: Waiting on User, Plan, Threads, Questions, Ideas,
Parked, Working Agreements, Log (`board.TemplateSections`). Archive is not
seeded — the first archive action creates it just above Log. Abridged example:

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

Item conventions (parse leniently; unknown lines are kept verbatim):

- Status: `- [ ]` open · `- [>]` in progress · `- [x]` done · `- [?]` blocked. Plain `- ` items allowed (Ideas/Log).
- Urgency: leading `!!` in the text. Removing `!!` or checking the box acknowledges it.
- Addressing: `@claude` / `@user` anywhere in the text.
- Log is append-only, `- HH:MM author: text`.
- Round-trip rule: the TUI/hooks must never destroy content they don't understand.

## Concurrency (avoiding lost updates)

The board is edited concurrently by the TUI and by external writers (Claude, the
hooks). Two mechanisms keep a write from silently clobbering another:

- **Advisory locking (both sides).** Every writer takes an exclusive `flock` on a
  sidecar lock file `<board-path>.lock` — never on the board itself, because the
  board's inode is replaced by the atomic rename and a lock on it would not
  exclude a racing writer. The lock is held across the whole read → modify →
  rename. In this repo, `board.WithLock(path, fn)` is that primitive; `Save`,
  `SaveTo`, and `SaveRebasing` all acquire it. **Any external writer must follow
  the same convention:** `flock` `<board>.lock` around its own read-modify-rename.
  Readers need no lock — the atomic rename keeps reads torn-free.

- **Rebase-before-save (TUI).** The TUI tracks the exact bytes it last loaded or
  saved. Inside the lock, a save re-reads disk; if disk differs from that
  last-known state (someone wrote while the user was typing), the TUI reparses the
  *disk* version and re-applies its one pending mutation onto that fresh tree,
  then saves the merged result — so both edits survive. The mutation is located in
  the fresh tree by the target item's exact raw line (first match within the same
  section); if the target vanished from disk, the user's text is appended as a new
  item to the section rather than lost. Undo records the external state so a first
  undo reveals the external write and a second reaches the pre-mutation state.
  - **Which ops rebase:** the text-entry ops where losing work hurts — `a` add,
    `e` edit, `R` reply, `L` log. The one-keystroke, trivially-redoable ops —
    `space` status, `!` urgent, `d`/`D` archive/delete, `A`/custom add-section —
    fall back to last-writer-wins (still under the lock, so never a torn write).

## Hooks behavior

- **session-start**: read `{session_id, cwd, ...}` from stdin JSON. Create
  `~/.claude/boards/<session-id>.md` from template if missing (frontmatter + empty sections,
  log line). If `$TMUX_PANE` set, write `panes/$TMUX_PANE.json`. Print to stdout a short
  protocol blurb for Claude's context: board path + instructions (maintain Threads/Plan as
  you work, answer/raise Questions, append Log on milestones, watch for user edits with the
  Monitor tool, urgent items are injected automatically).
- **prompt-submit**: read stdin JSON (has `session_id`). If the board has unchecked `!!`
  items, print them to stdout (they enter Claude's context). Also print a one-line notice
  if board mtime is newer than a `.last-seen` state file (then touch the state file:
  `~/.claude/boards/.state/<session-id>.last-seen`).
- **session-end**: append `- HH:MM end: session ended` to Log; delete the pane mapping file
  if it points at this session.

Hooks must be fast (<100ms) and never fail the session: on any error, exit 0 silently.

## TUI (bubbletea + lipgloss + bubbles)

- Layout: board rendered as sections; cursor moves across items; active section highlighted.
- Keys: `j/k` move · `tab`/`shift-tab` next/prev section · `a` add item to current section ·
  `space` cycle status `[ ]→[>]→[x]` · `!` toggle urgent · `d` delete item ·
  `e` edit item inline (textinput) · `E` open board in `$EDITOR` (suspend TUI) ·
  `L` quick log entry · `r` reload · `q`/`esc` quit · `?` help.
- Live reload: watch the board file (fsnotify) and re-render on external change (Claude's
  edits appear live). Writes are atomic (temp file + rename) to avoid torn reads. While an
  inline input is open the reload is deferred (it must not touch the input buffer or the
  edit target); the change is merged at save time instead (see Concurrency).
- New items get author `user:` implicitly? No — keep text as typed; author tags optional.
- Style: dark-friendly, subtle; urgent items highlighted; done items dimmed.
- Picker mode (fallback/`-l`): list boards (session id, cwd, mtime, first in-progress thread).

## tmux integration

```tmux
bind-key g display-popup -E -w 80% -h 80% "session-notes --pane '#{pane_id}'"
```

## Install

`./install.sh`:
- build binary (`~/.local/go/bin/go build`), copy to `~/.local/bin/session-notes`
- merge hooks into `~/.claude/settings.json` (jq; back up first; idempotent)
- print the tmux bind-key line for the user's tmux.conf
