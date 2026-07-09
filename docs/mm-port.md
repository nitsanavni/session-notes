# Porting mm into session-notes — feature scoping

Goal: a native Go mindmap view/editor for boards inside the existing TUI —
full control over integration and board special cases, no bun/mm-checkout
dependency. High-level in/out list for discussion; nothing here is built yet.

## In — port from mm

- **Outline model over markdown bullets**: lossless parse/serialize of nested
  `- ` bullets with checkboxes. We already have board parsing; this adds the
  tree model the layout needs.
- **Center-outward layout**: title at center, first ring around it, subtrees
  fanning outward with box-drawing connectors. The signature feature.
- **One skin** (rounded) to start; mm's skin set (sharp/heavy/double/ascii) is
  a later nicety.
- **Navigation**: move focus between nodes (arrows/hjkl), expand/collapse a
  subtree.
- **Structural edits**: add sibling/child, delete subtree, move/reparent,
  inline text edit of the focused node.
- **Checkbox toggle** on the focused node.
- **Re-layout on change** — immediate mode only; mm's settle-timer and manual
  (`=`) layout modes are out for v1.

## Out — deliberately not ported

- **Bun/TS runtime and mm's CLI** (`tree`, `add`, `find`, `done`, `mv`,
  `--json` addressing) — Go equivalents can come later if agents need them;
  Claude edits the file directly anyway.
- **Scrollback / no-alt-screen rendering** — mm's "lives in your scrollback"
  design conflicts with our bubbletea alt-screen TUI. The map renders as a
  view inside the TUI (and thus in the tmux popup).
- **File watching** — the TUI already watches the board and rebases in-flight
  edits; the map view plugs into that, it doesn't bring its own watcher.
- **mm's checkbox semantics quirk** (toggle cycles `x` ↔ `space`, never back
  to no-box) — we do it right: sections and plain nodes stay box-less.

## Added — board-specific behavior mm doesn't have

- **Section skeleton as first ring, protected**: `##` sections are structural
  nodes — always present (even empty), fixed order, not checkbox-able, not
  deletable from the map. Solves the corruption class we hit with the
  bridge-script approach for free.
- **Full status marker set**: `[ ]` / `[>]` / `[x]` / `[?]` as first-class
  states with distinct glyphs, one key cycling them — not just GFM checkboxes.
- **`!!` urgent highlighting** on the map.
- **Reply-aware styling**: `user:` / `claude:` sub-bullets rendered dimmer /
  colored as conversation, maybe collapsed to a count until focused
  (threads stay readable when discussions get long).
- **Long-node handling**: truncate on the map, show the focused node's full
  text in a status/detail line (mm just grows very wide).
- **Log section excluded by default** (append-only noise; toggleable).
- **Locked saves + concurrent-edit rebase reused**: the map view saves through
  `board.WithLock` and the same 3-way rebase the outline view uses — Claude's
  concurrent edits merge instead of clobbering.
- **View toggle**: one key switches outline view ↔ map view on the same loaded
  board (same undo history), rather than a separate binary.
- **Pane-resolution fallback**: when a pane has no mapping (the "no board
  mapped to pane" you just hit), fall back to the newest board for the
  project cwd / the picker, instead of exiting.

## Decisions (from the board)

1. **Editing from day one** — no read-only interim.
2. **Bridge removed** — `scripts/boardmm` deleted; this port replaces it.
3. **Keys**: outline-view bindings win where the two overlap (a add, e edit,
   space status-cycle, d/D archive/delete, u/ctrl+r undo/redo, o open link);
   mm's keys fill the gaps the outline view doesn't have (hjkl spatial nav
   across the map, enter collapse/expand subtree).

## Plan

- Phase 1: `internal/mindmap` — tree model + center-outward layout +
  renderer, golden-tested against mm's own `tree` output.
- Phase 2: interactive map view in the TUI behind a view toggle (nav,
  structural edits, marker cycle).
- Phase 3: board specials — protected sections, reply styling, !! highlight,
  locked saves + concurrent-edit rebase wiring.

## Status

- Phase 1 landed (f81de40): internal/mindmap — model, layout, renderer;
  byte-identical to mm on 7 golden fixtures.
- Phase 2 landed (338c523): `m` toggles the interactive map view in the TUI;
  spatial nav, folding, edits through the shared locked-save/rebase path.
- Phase 3 landed: reply-aware styling (dim `user:`/`claude:` sub-bullets) with
  automatic collapse into a `[+N]` suffix in the default view; the `Log`
  section excluded from the map by default with a
  "Log hidden · M" footer hint (`M` toggles it on); and the pane-resolution
  fallback — a pane with no mapping resolves to the newest board matching the
  pane's current directory (via tmux `pane_current_path`) before the picker,
  instead of exiting with "no board mapped to pane".
- Surprise recorder ported (mm's `!` feedback feature): the map keeps a ring
  buffer of the last 20 actions, each with a replayable before-state (board
  markdown + focus key + folds/replies/Log visibility); `!` prompts "you hit k ·
  focus moved 'X' → 'Y' — where did you expect it?" and appends the note + trail
  to `<board>.feedback.jsonl`. Divergences from mm, both simplifications: the
  sidecar-spill threshold is 8 KiB (mm: 64 KiB) since board maps are smaller
  than mm maps, and spilled records are plain JSON (no gzip/base64 — greppable,
  trivial reader), same as mm. `session-notes feedback <board.md>` lists records
  (`--json` raw); `--gen-test` prints paste-ready Go tests (package tui,
  building on the `newMapModel`/`keyPress` test helpers) that replay the move
  and assert today's landing, mm's flip-one-line-to-fail design.
- Navigation reworked to mm's tree-structural model (was a geometric
  nearest-node scorer). Root cause of the reported "k on a section does nothing
  / lands somewhere odd" surprise: the scorer required candidates to sit
  strictly on one side of the focus *center* and then weighted cross-axis
  distance 3×, so vertically-stacked first-ring sections (same column, differing
  widths → differing centers) were rejected or mis-ranked, letting up/down jump
  across the map or stall. Now `j`/`k` walk the focused node's siblings **on its
  side of the center** in document order (which is exactly the visual top-to-
  bottom stack, so up/down reach the adjacent node and never dive into a
  subtree); on the center they step to the nearest first-ring node in that
  direction. `h`/`l` move the parent/child axis (mm's `moveLateral`): toward the
  center is the parent, away enters the branch at its first child — so left/right
  cross between sides via the center. Deterministic; table-tested against real
  layouts in `mapnav_test.go` (including the recorded `k`-on-`Log` case). mm's
  per-side "remember where I descended from" memory (`lastVisited`) is not
  ported — away-from-center always enters at the first child.
- Long-node handling implemented (mm has none — it just grows wide). Node text
  is truncated to 40 display columns (unicode-width aware, in `width.go`) with a
  trailing `…` at layout time; the focused node's full text stays in the detail
  footer. `w` toggles the focused node expanded in place — wrapped into a
  multi-line block at 40 cols. `mindmap.Placement` gained a `Height` and per-row
  `Lines`; layout row-packing sums node heights and connectors attach at a
  node's vertical center row. Truncation/expansion is a `mindmap.Options` opt-in
  (`LayoutTreeOpts`) that the TUI turns on and the golden tests leave off, so the
  byte-identical-to-mm fixtures are untouched; the Go-only truncation and
  multi-line-layout cases have their own tests (`layout_opts_test.go`).
- Fold model unified (was two overlapping toggles). The original design had
  `enter` do one of two unrelated things depending on the node: on a node with
  reply children it toggled ONLY reply visibility (`mapRepliesShown`), so its
  non-reply children and any already-expanded nested reply subtrees stayed on
  screen; the ordinary whole-subtree fold (`mapFolded`) was unreachable there.
  Users reported "collapsing a thing with replies doesn't fully collapse it" —
  two half-collapses, no full one. Replaced both boolean maps with a single
  per-node `mapFold map[string]foldState` (`foldDefault`/`foldCollapsed`/
  `foldRepliesExpanded`, absent == default) and one predictable cycle:
  **collapsed → default → replies-shown → collapsed**. Each `enter` reveals more
  until the whole subtree is visible, then one more press collapses it fully to a
  single suffix — `[+N]` over every hidden descendant (replies included), or
  always `[+N]`. States that render
  identically for a given node are skipped, so a no-reply node is a two-state
  default↔collapsed toggle and a replies-only node is summary↔expanded. Collapse
  is done by simply not emitting hidden children (never `mindmap.Folded`), so the
  suffix counts are computed in the TUI and are always right; nested descendant
  fold state is keyed by stable node key and survives an ancestor's round trip.
  Recorder change (`feedback.go`): the captured before-state now carries a single
  `fold` object (`{key: state}`) instead of the old `folded`/`repliesShown`
  string arrays. Old JSONL records still replay — `restoreMapModel` and the
  `--gen-test` emitter read the legacy fields, mapping `folded`→collapsed and
  `repliesShown`→replies-expanded — so no data migration is needed.
