# Tree API spec (M0)

Workflowy-style direction for session-notes: every bullet is a node with a
stable ID; zoom (render/attach/edit a subtree as root) is the core addressing
primitive. This spec covers M1 (tree API over the file backend, pure refactor)
and the contract M2 (subtree attach/watch) builds on. Cloud backend later
implements the same interface.

## Node IDs

- Format in markdown: trailing anchor ` ^<id>` on the bullet line, e.g.
  `- [ ] ship the thing ^k3v9q2`. Obsidian block-ref style: human-tolerable,
  greppable, survives hand-editing.
- ID alphabet: 6 chars of base36 (lowercase a-z0-9), crypto/rand. Collision
  check within the board on assignment.
- Parsing: the anchor is parsed and stripped from `Item.Text` (like `!!` /
  `!pin`), stored as `Item.ID`, re-rendered on save. Anywhere else in the
  line, `^...` is plain text.
- Assignment is lazy and write-driven: parsing never mutates; any save through
  the op layer assigns IDs to **all** items that lack them (one-time churn per
  board is acceptable; watch/delivery diffs see it once). Hand-written bullets
  get IDs on the next programmatic save.
- IDs are immutable once assigned; a hand-edit that deletes an anchor simply
  gets a fresh ID (old ID is dead, no reuse).
- Sections keep title addressing (they are structural, not nodes), but each
  section heading also carries an ID anchor (`## Threads ^s1a2b3`) so a
  section can be a zoom root / attach target too.

## Node addressing

Canonical node ref: `<board>#<id>` (board path or board id as today, `#`
fragment is the node ID). `#` absent = whole board (root). This is the one
addressing scheme for CLI, web, TUI, watch, and future remote URLs — replacing
the current per-surface schemes (raw-line, `sN/i/j`, `it:<section>:<raw>`).

## The interface

In `internal/board` (file backend implements it now; server backend later):

```go
type Tree interface {
    // Read the subtree rooted at id (empty id = whole board), depth<0 = all.
    Get(id string, depth int) (*Node, error)
    // Apply an op addressed by node ID (extends board.Op with ID addressing).
    Apply(op Op) (OpResult, error)
    // Watch changes scoped to the subtree rooted at id.
    Watch(id string) (<-chan Event, func(), error)
}
```

- `Node` is the exported read model: `ID, Text, Status, Urgent, Pinned,
  Children`. `board.Item` stays the internal lossless representation.
- `Op` grows an `ID` addressing field; existing `Section+Raw` and `Query`
  addressing remain as fallbacks (back-compat for humans and old CLI usage)
  but resolve to an ID internally.
- `Event` carries the affected node ID(s) + op kind; the file backend may
  degrade to coarse "subtree changed" events derived from the whole-file diff,
  as long as events outside the watched subtree are filtered out.
- New ops needed for parity with the views: `move` (reparent/reorder) can wait
  until a caller needs it; do not block M1 on it.

## M1 exit criteria (pure refactor + IDs)

1. Anchors parse/render losslessly; boards without anchors behave exactly as
   today until first programmatic save.
2. `Tree` implemented by the file backend; CLI `edit`, web `/api/board` edit
   path, and TUI mutations all resolve through `Apply` with ID addressing
   available (`--id` flag on `session-notes edit`; `id` field in web editReq;
   web `itemJSON` includes `id`).
3. Behavior otherwise identical: gofmt/vet/`go test ./...` green, e2e green.
4. Undo journal, locking, `--refresh-snapshot`, delivery receipts unchanged in
   semantics (they remain whole-file under the hood in M1).

## M2 (contract only, implemented next)

- `session-notes watch --node <board>#<id>` and `edit ... --root <id>`:
  attach/watch/edit scoped to a subtree; edits addressed outside the root are
  refused. Zoom roots in TUI (`mapFocusRoot`) and web (`mapState.root`) become
  node IDs, persisted, shared across views, and linkable (`/b/{id}#<node>`).
- Carve-out: spawning a sub-agent with `--root <id>` is the permission
  boundary.
