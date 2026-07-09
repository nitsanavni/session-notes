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
  `board.WithLock` and the same 3-way rebase the list view uses — Claude's
  concurrent edits merge instead of clobbering.
- **View toggle**: one key switches list view ↔ map view on the same loaded
  board (same undo history), rather than a separate binary.
- **Pane-resolution fallback**: when a pane has no mapping (the "no board
  mapped to pane" you just hit), fall back to the newest board for the
  project cwd / the picker, instead of exiting.

## Open questions (also on the board)

1. Map view read-only first (fast to land, edits stay in list view), or
   editing from day one?
2. Keep `scripts/boardmm` as a bridge until the port lands, or delete now?
3. mm's interaction model (which keys, edit mode behavior) — mirror mm
   exactly for muscle memory, or adopt the list view's keys for consistency?
