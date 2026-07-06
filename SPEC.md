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

Markdown, loosely parsed. YAML frontmatter + fixed `##` sections:

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
  edits appear live). Writes are atomic (temp file + rename) to avoid torn reads.
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
