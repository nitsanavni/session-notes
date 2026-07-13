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

## M3 (cloud-mode server, implemented)

A `session-notes server` process lets any Claude session anywhere attach to a
board (or subtree) with the companion CLI — login, watch, edit — using the same
`Tree` semantics as a local file. The `board.Tree` interface is the seam: a
`RemoteTree` (internal/cloud) implements Get/Apply/Watch over HTTP+SSE, so
`edit`/`watch` treat an `http(s)://…/b/<board>#<id>` ref exactly like a path.

### Storage

One markdown blob per board in SQLite (`modernc.org/sqlite`, pure Go, no cgo),
WAL mode. Boards are small, so this reuses Parse/Render/Apply/EnsureIDs
wholesale. Node-granular storage is deferred — the Tree interface allows
swapping later. Schema: `boards(id, content, version, created, updated)`,
`tokens(token_hash, name, created)`, and an append-only `ops(board_id, version,
author, before, after, ts)` journal (feeds `/history`). Writes to one board are
serialized by an in-process per-board mutex, mirroring the file backend's flock.

### Subcommands

- `session-notes server [--addr host:port] [--db path] [--insecure]` — serves
  the API + browser UI. Refuses to start without a token unless `--insecure`.
- `session-notes server token create --name <n>` — mints a bearer token,
  prints it once, stores only its SHA-256 hash.
- `session-notes login <server-url> [--token t]` — stores a bearer token keyed
  by host in `$CONFIG/session-notes/tokens.json` (0600); remote edit/watch read
  it automatically.
- `session-notes remote new <url> <name>` / `remote push <local> <url> [name]`
  / `remote pull <url>/b/<board> <local>` — create/import/export boards.

### Protocol (HTTP JSON + SSE)

All routes require `Authorization: Bearer <token>` (or `?token=` / the cookie a
tokened visit sets, for the browser) unless the server is `--insecure`.

| Method & path | Purpose |
| --- | --- |
| `GET /b/{id}` | browser board UI (same `board.html` as file mode) |
| `GET /api/boards` | board list |
| `POST /api/boards` | create board `{id,name,content?}` → 201 |
| `GET /api/board/{id}` | board JSON (adds a `version` field; `ETag`) |
| `GET /api/board/{id}/raw` | `{content, version}` markdown blob |
| `PUT /api/board/{id}/raw` | replace whole blob `{content,author}` (push) |
| `POST /api/board/{id}/edit` | apply one op (see below) |
| `GET /api/board/{id}/events[?node=<id>]` | SSE change stream |
| `GET /api/board/{id}/history` | journaled edits as line-diffs |
| `GET /api/keymap` | keybinding help JSON |

**Edit body** is a superset of the file-mode web `editReq`: `op`, `id`, `root`,
`section`, `raw`, `query`, `text`, `status`, `author`, `urgent`/`pinned`, and an
optional `base` version. It maps to a `board.Op` (including ID and Root
addressing) and is applied through `board.Apply` + `EnsureIDs`. `Op.Root`
travels in the POST, so the subtree carve-out is enforced **server-side**: an op
addressing a node outside its root is refused (400, `ErrOpInvalid`).

**Versioning / conflict.** Every write bumps the board's integer version. A POST
may carry `base`: if `base >= 0` and it no longer matches the stored version,
the server returns **409** with the current version and does not apply — the
client refetches and reapplies (ops are surgical, so retry is cheap). Omitting
`base` (the RemoteTree default) relies on the server's per-board serialization
alone. `ErrOpNotFound` also maps to 409.

**SSE scoping.** `events?node=<id>` fires only when the subtree rooted at that
node actually changed between the client's last-seen and current content (diffed
via `board.SubtreeSource`), so sibling-subtree edits stay invisible — the remote
equivalent of `watch --node`.

### Exit criteria (met)

1. SQLite store round-trips content, bumps versions, 409s on stale base, refuses
   out-of-scope rooted ops.
2. `RemoteTree` drives edit/watch/carve-out end-to-end against an httptest
   server; token auth gates all routes (401 without, 200 with); login config
   round-trips at 0600.
3. Integration: real port, two clients editing sibling subtrees concurrently
   lose no writes; a `--node`-scoped remote watch fires only for its subtree.
4. `--board http(s)://…` routes `edit` and `watch` to the remote backend; a
   `#<node>` fragment supplies a default `--root` / watch scope.

## M4 (multi-user attach as the permission model, implemented)

M4 turns the M2/M3 subtree carve-out into the product's real access-control
model: tokens carry identity, grants bind a token to a board (or a node within
it) with a permission level, and a subtree-scoped token literally cannot see or
touch anything outside its granted node — enforced server-side at the same op
choke point the local carve-out uses.

### Identity + grants

- **Tokens** gain `subject` and `admin` columns. Every authenticated request
  resolves to an `Identity{Subject, Admin}` (stashed in the request context by
  the auth middleware). The journalled `author` defaults to the token subject
  when the client sends none. `server token create --name <n>` mints an
  **all-boards admin bootstrap token** (the operator's own key) — the sane
  default so a fresh server is usable; scoped, non-admin tokens are minted only
  through grants. Existing (pre-M4) dbs are migrated with `ALTER TABLE ADD
  COLUMN` (idempotent).
- **`grants`** table: `(subject, board_id, root_node_id, perms, created)`, one
  row per `(subject, board)` (re-granting upserts). `root_node_id` empty = whole
  board. `perms` ∈ `read|write|admin` (ranked; a check requires "at least").

### Enforcement (single `authorize(board, need)` per handler)

- **No grant → 404** on that board (existence is not leaked; admin identities
  get an implicit whole-board admin grant on every board).
- **read** → `GET` board/raw/events/page allowed; `POST edit` refused **403**.
- **write + root_node_id** → the carve-out is **mandatory**: the server forces
  every op's `Op.Root` to the granted node and refuses a client-supplied root
  that isn't the grant root (**403**); ID/query/section addressing outside the
  subtree is then refused by the existing op-layer scope check (**400**). SSE is
  forced to `?node=<grant root>`; `GET /api/board` returns only the granted
  subtree via `Board.Scoped` (reuses the same Section/Item pointers, so a scoped
  agent's board JSON has no siblings) plus a `scope` field the browser shows as
  a "scoped view" badge. Whole-blob `PUT …/raw` and `history` are refused for a
  subtree-scoped token (they'd bypass or leak the carve-out).
- **admin** → manage boards + grants; `POST /api/boards` and all grant/token
  endpoints require admin.

### Endpoint additions

| Method & path | Purpose (admin-only) |
| --- | --- |
| `POST /api/tokens` | mint a token `{name,subject?,admin?}` → `{token,subject}` |
| `GET /api/board/{id}/grants` | list a board's grants |
| `POST /api/board/{id}/grants` | grant `{perm,root,subject\|tokenName\|newToken}`; `newToken` mints a token + grant in one step → `{subject,perm,root,token?}` |
| `DELETE /api/board/{id}/grants` | revoke `{subject\|tokenName}` |

### CLI

- `remote grant <url>/b/<board>[#<node>] --new-token <n> --perm read|write` —
  mint a scoped token + grant and print an **attach line** (login + edit,
  scoped to the subtree) to paste into a sub-agent: the headline carve-out
  handoff. `--token-name <n>` grants an existing token's subject instead.
- `remote grants <url>/b/<board>` lists; `remote revoke … --token-name/--subject`
  removes. All use the caller's stored admin token (`session-notes login`).

### Concurrency hardening

`RemoteTree.Apply` sends its last-seen version as an optimistic `base` and, on
409, refetches and retries the surgical op (bounded), then falls back to an
unconditional apply. Two writers racing on the **same** node get last-writer-
wins with **no dropped write and no 5xx** (the per-board write lock serializes
the final landing).

### Bootstrap flow

1. `session-notes server token create --name ops` → admin token (printed once).
2. `session-notes login https://host --token <ops-token>`.
3. `session-notes remote new https://host myboard` (admin).
4. `session-notes remote grant https://host/b/myboard#<node> --new-token scout
   --perm write` → paste the printed attach line to a sub-agent.

### Exit criteria (met)

1. Grants CRUD + token identity round-trip; perm matrix enforced
   (none/read/write-scoped/admin × GET/POST/SSE).
2. A subtree-scoped token's board view and SSE contain only the granted subtree;
   a write addressing outside the granted root is refused (403/400) end-to-end.
3. Two remote writers racing on the same node lose no writes and never 5xx.
4. CLI grant/list/revoke round-trip against an httptest server; a revoked token
   drops to 404.
