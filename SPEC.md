# session-notes — shared session board for Claude Code

A per-session scratchpad shared between the user and Claude: threads of work, open
questions, plan, ideas, log. User views/edits it in a tmux-popup TUI; Claude reads and
maintains it during the session via hooks + file watching.

## One binary: `session-notes`

- `session-notes` (no args) — open the TUI for the current session's board.
- `session-notes --pane <tmux-pane-id>` — resolve board via pane mapping (used by tmux keybind).
- `session-notes --board <path>` — open a specific board file.
- `session-notes pins [--due] --board <path> | --session <id>` — print the board's pinned-item block (same format `prompt-submit` injects). Plain: always print, silent when there are no pins. `--due`: print only when due on the re-injection cadence, updating the same `.pins` state the hook uses (so a monitor and the hook share one cadence). Always exits 0.
- `session-notes docs <topic>` — print an on-demand protocol/recipe topic (`protocol`, `monitor`, `conflicts`, `cli`, `blurb`); no/unknown topic lists them. `blurb` prints the session-start blurb itself (placeholder board path) — the single source shared with the `session-start` hook injection. Versioned with the binary; keeps the session-start blurb small.
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
   If the mapping is missing (or its board file is gone), fall back to the newest
   board whose frontmatter `cwd` matches the pane's current directory (queried
   from tmux via `pane_current_path`) before dropping to the picker — a pane
   without a mapping opens the project's latest board instead of erroring out.
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
- Pin: leading `!pin` in the text (mutually exclusive with `!!`, and NOT urgent). Pinned items are re-injected into Claude's context by `prompt-submit` on a cadence (see below), or on demand via `session-notes pins`. Rendered with a subtle, calmer indicator than `!!`.
- Addressing: `@claude` / `@user` anywhere in the text.
- Log is append-only, `- HH:MM author: text`.
- Links: `[[name]]` (no slash) is a side note at `<boards-dir>/<session-id>.notes/name.md` (opening a missing one creates it). `[[path/with/slash.md]]` (contains `/`, or starts with `~/` or `/`) is a file path relative to the session cwd (`~` expanded, absolute as-is); opening a missing path errors rather than creating a stub. `o` opens the current item's first link.
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
  the same convention:** `flock` `<board>.lock` around its own read-modify-rename
  — which is exactly what the `session-notes edit` CLI (below) does, so Claude
  writes through it instead of hand-rolling a lock. Readers need no lock — the
  atomic rename keeps reads torn-free.

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

## Claude write CLI (`session-notes edit`)

Claude's first-class, safe write path — the shipped replacement for the ad-hoc
`python3 fcntl.flock` scripts sessions used to reinvent. `session-notes edit
<sub> [flags] args…` performs one locked read → modify → atomic-rename per
invocation via `board.EditUnderLock` (which wraps `board.WithLock` + the shared
atomic-write step — the CLI does not hand-roll a second lock protocol).

- **Board selection:** `--board <path>` (explicit, the primary form for an agent)
  or `--session <id>` (→ `~/.claude/boards/<id>.md`). Exactly one; the file must
  exist.
- **Subcommands** (mirroring the replace / append-under semantics of a per-session
  `board_edit.py`):
  - `add <section> <text>` — append a top-level open item to an **existing**
    section; missing section is an error that lists the board's sections (no
    auto-create).
  - `reply <query> <text>` — append an indented reply under the first item (any
    section, any depth, document order) whose raw line or text contains `<query>`;
    no match is an error, multiple matches use the first and note the count on
    stderr.
  - `status <query> <state>` — set the matched item's checkbox;
    `open|wip|done|blocked|none` map to `[ ]|[>]|[x]|[?]|`plain.
  - `log <text>` — append `- HH:MM claude: <text>` to Log (`--as <author>`
    overrides `claude`).
  - `title <text>` — set the frontmatter `title` (`""` clears).
  - `replace <old> <new>` — exact first-occurrence string replace over the raw
    file bytes; the escape hatch (and the way to reconcile conflict markers).
    `<old>` absent is an error.
- **`--refresh-snapshot <path>`** — after the rename, still inside the lock, copy
  the new content over `<path>`. This is the monitor self-edit-suppression hook: a
  `Monitor` watch that snapshots/diffs under the same lock then emits only the
  user's edits, never Claude's.
- **Output contract:** silent on success (unix-quiet); any failure prints a single
  `session-notes: <reason>` line to stderr and exits non-zero.

## Hooks behavior

- **session-start**: read `{session_id, cwd, ...}` from stdin JSON. Create
  `~/.claude/boards/<session-id>.md` from template if missing (frontmatter + empty sections,
  log line). If `$TMUX_PANE` set, write `panes/$TMUX_PANE.json`. Print to stdout a short
  protocol blurb for Claude's context: board path + the essential habits only (title early,
  maintain Threads/Plan, reply etiquette, `!!`/`!pin`, write via `session-notes edit`, watch
  with the Monitor tool), ending with a pointer to `session-notes docs <topic>` for the full
  recipes. Kept under ~12 lines so it doesn't bloat every session's context.
- **prompt-submit**: read stdin JSON (has `session_id`). If the board has unchecked `!!`
  items, print them to stdout (they enter Claude's context). Then re-inject pinned (`!pin`)
  items when due — the pinned set changed since the last injection, or the cadence elapsed
  (default 15 min, overridable per board with a `pin-cadence:` frontmatter key in Go duration
  syntax, e.g. `10m`/`90s`/`1h`) — tracked via `~/.claude/boards/.state/<session-id>.pins`
  (timestamp + content hash). Because most turns come from a file-watch monitor rather than a
  user prompt, a monitor can call `session-notes pins --due --board <path>` each cycle to
  resurface pins on the same cadence and shared state. Then
  print a one-line coherence digest ("Board health: N threads `[>]` untouched >2h · M
  questions @claude unanswered · K items in Waiting on User"), only the nonzero clauses, and
  only when at least one is nonzero — `[>]` age is tracked in
  `~/.claude/boards/.state/<session-id>.wip` (first-seen time per raw `[>]` line; an item
  leaves the file when it is no longer `[>]`). Also print a one-line notice if board mtime is
  newer than a `.last-seen` state file (then touch the state file:
  `~/.claude/boards/.state/<session-id>.last-seen`).
- **session-end**: append `- HH:MM end: session ended` to Log; delete the pane mapping file
  if it points at this session.

Hooks must be fast (<100ms) and never fail the session: on any error, exit 0 silently.

## TUI (bubbletea + lipgloss + bubbles)

- Layout: board rendered as sections; cursor moves across items; active section highlighted. The sticky header shows the title (frontmatter `title`, else session id, else path); when a `title` is set, a shortened session id (first 8 chars) is shown dimmed next to it (outline header and map header) so the id stays visible.
- Keys: `j/k` move · `tab`/`shift-tab` next/prev section · `a` add item to current section ·
  `space` cycle status `[ ]→[>]→[x]` · `!` toggle urgent · `d` delete item ·
  `e` edit item inline (textinput) · `T` edit the frontmatter title (empty clears it) ·
  `E` open board in `$EDITOR` (suspend TUI) ·
  `o` open item's first `[[link]]` · `y` copy board file path to clipboard ·
  `w` wrap the item at the cursor (single truncated line ↔ full multi-line block, session-only) ·
  `L` quick log entry · `m` toggle map view · `r` reload · `q`/`esc` quit · `?` help.
- Long items render as a single line truncated with `…`; `w` toggles the item at the cursor
  to a wrapped multi-line block (continuation rows hang-indented to the text column, reply
  indent preserved) and back. The toggle is per item and lasts the session only, like the map's `w`.
- Map view (`m`): the board as a center-outward mindmap (ported from mm — see
  docs/mm-port.md). Title at center, all `##` sections as the protected first ring,
  items as subtrees. `m` toggles between the outline and the map and the selection
  carries over both ways: entering the map focuses the node for the outline
  cursor's item (revealing it if a collapsed ancestor or un-expanded reply thread
  hid it), and leaving the map moves the outline cursor onto the map's focused
  item (or its section). Map fold, expand, and focus-root state persist across
  toggles; a persisted focus root is dropped when it no longer contains the
  carried-over item so the selection stays visible. Navigation is tree-structural
  (mm-style, not pixel-scored):
  `j`/`k` (down/up) walk the focused node's siblings on its side of the center,
  top-to-bottom, so up/down always reach the node visually above/below and never
  dive into a subtree or jump across the map; `h`/`l` (left/right) move along the
  parent/child axis — toward the center is the parent, away enters the branch at
  its first child, so left/right cross between the two sides via the center.
  `f` focuses into the selected subtree (mm's focus feature): the map re-roots on
  that node — it becomes the center, its children fan out — with a breadcrumb
  trail (e.g. `board › Threads › port mm…`) rendered under the header naming the
  path back. Focus lands on the new center's first child; a collapsed focus root
  is un-collapsed (it would otherwise hide the very children being revealed).
  `b` (and `esc`, when no input is open) steps focus out one level, resting the
  cursor on the node stepped out of, until repeated presses reach the whole
  board; folds, edits and navigation all work as normal within the focused
  subtree, and fold/expand state persists across re-rooting since it is keyed by
  stable node key. `o` opens a `[[wiki-link]]` on the focused item's text,
  reusing the outline view's link machinery (a chooser overlay when the item has
  several links, then back to the map); sections and the center have no links.
  Node text is truncated to 40 display columns (unicode-width aware) with a `…`
  so maps don't sprawl (the fold summary suffix — `[+N]` — is
  truncation-exempt: the base text is clipped first, then the suffix appended, so
  a collapsed node always shows its hidden-child count regardless of side or
  text length); the focused node's full text still shows in the detail
  footer, and `w` toggles the focused node expanded in place — rendered as a
  wrapped multi-line block instead of one truncated line (the suffix rides on the
  last wrapped row). `enter` runs one
  unified fold cycle on the focused node: **collapsed → default → replies-shown →
  collapsed**. Each press reveals more (the default view, then the full reply
  thread) until everything is shown, then one more press collapses the whole
  subtree to a single summary suffix — `[+N]` counting every hidden descendant,
  always `[+N]`. States that would render
  identically are skipped, so a node with no replies just toggles
  default↔collapsed and a replies-only node toggles summary↔expanded. Nested fold
  state survives a parent's collapse/expand round trip. `a`/`e`/`space`/`d`/`D`
  edit. Items: `e` edits, `space` cycles status, `a`/`A` add child/sibling, `d`
  archives, `D` deletes. Sections: `e` renames the heading (contents intact; a
  rename that would collide with an existing section is refused), `d` archives
  the whole section into `## Archive` (with the same Archive/Log guards as the
  outline view); sections still can't be marked (`space`) or hard-deleted (`D`) from
  the map. On the center node `e` edits the board title (same as `T` in the list
  view) and `a` opens the add-sections overlay; the center is never markable,
  archivable, or deletable. A section rename replays by matching the OLD title in
  the fresh disk tree (no-op if it vanished); the map's fold/expand state is keyed
  by section index so it follows a rename automatically, while the outline view's
  per-title collapse override is migrated to the new title (so state never leaks
  onto a later section that reuses the old name). Every mutation saves through the
  same locked write + rebase path as the outline view and shares its undo history.
  The title edit is a single-field, last-writer-wins op under the lock (no
  per-item merge — the whole value is set absolutely on rebase).
  Conversational reply children (`user:`/`claude:` sub-bullets) render dim and, in
  the default view, collapse into a `[+N]` suffix on their parent. The
  append-only `Log` section is excluded from the map by default (a "Log hidden ·
  M" footer hint shows when it is); `M` toggles it back on. When a move surprises
  you (focus didn't land where your fingers expected), `!` opens a prompt showing
  what just happened ("you hit k · focus moved 'Log' → 'Working Agreements' —
  where did you expect it?") and appends the note plus the last 20 map actions —
  each with a fully replayable before-state (board markdown, focus key, folds,
  Log visibility) — to `<board>.feedback.jsonl`. Records over 8 KiB are spilled
  as plain JSON to a `<board>.feedback/` sidecar dir and referenced by a stub.
  Browse with `session-notes feedback <board.md>` (`--json` raw); `--gen-test`
  prints ready-to-paste Go test source that replays each surprising move and
  asserts where it lands today, with a TODO noting the expectation — flip one
  line to make it fail. Nav fixes are driven by these recorded surprises.
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
