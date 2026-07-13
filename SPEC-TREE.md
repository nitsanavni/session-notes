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

## M5 (deployable + durable, implemented)

M5 makes the M3/M4 server operable in production: token lifecycle, layered
durability, deploy artifacts, and operational hardening. No protocol changes —
the wire contract and access model are unchanged.

### Token lifecycle

- `server token list` — audit table (name, subject, admin, created) from the
  local `--db`; never the secret (only the SHA-256 hash is stored).
- `server token revoke <name>` — deletes every token under a name; the next
  request bearing a revoked secret 401s (the `ValidToken` full-table scan no
  longer matches). Same local-`--db` model as `token create`.

### Durability (three independent layers)

- **Bytes / point-in-time** — Litestream (`deploy/litestream.yml`) streams the
  SQLite db to S3-compatible storage, env-driven credentials, ~1s lag.
- **Second provider** — `deploy/backup.sh`: nightly `sqlite3 VACUUM INTO`
  (consistent, no downtime) + retention ladder pushed via restic *or* rclone
  (`BACKUP_TOOL` selects; provider-agnostic env). Cron-ready.
- **Format** — `server export --dir <dir>` writes each board as a re-parseable
  `<id>.md`; `remote pull --all <url> <dir>` is the HTTP-side equivalent (admin
  token). Boards outlive the engine as plain markdown.

### Operational hardening (code)

- Graceful shutdown: `SIGTERM`/`SIGINT` stops accepting, drains in-flight
  requests (SSE streams end on request-context cancel), then closes the DB
  (10s budget) via `http.Server.Shutdown`.
- `GET /healthz` — unauthenticated liveness (200 `ok`; 503 if the DB ping
  fails), outside the auth wrap and the body guard.
- Access log: one stderr line per request (method, path, status, duration),
  via a status-recording, `Flusher`-preserving middleware.
- Body guard: POST/PUT over 1 MiB refused `413` before any handler (Content-
  Length check + `http.MaxBytesReader`).

### Deploy artifacts (`deploy/`)

Multi-stage static `Dockerfile` (`CGO_ENABLED=0`, distroless runtime) +
`docker-compose.yml` (server + litestream sidecar + Caddy TLS, named volumes);
`Caddyfile` (automatic HTTPS, SSE-friendly proxy); `session-notes.service`
(bare-systemd route, hardened, SIGTERM stop); `backup.sh`; and `README.md` — a
runbook: Hetzner from zero, both install routes, token bootstrap, litestream,
backup cron, restore drill (litestream restore + integrity/export verification),
and the upgrade procedure (idempotent `ADD COLUMN` migrations = forward-only).

### Exit criteria (met)

1. Token list/revoke round-trip in the store; a revoked token stops validating.
2. `server export` (and the render round-trip) produces boards that re-parse
   with their sections/items intact.
3. `/healthz` returns 200 with no auth on a secured server.
4. An oversized POST body is refused 413.
5. Graceful shutdown drains and closes the DB on SIGTERM.

## M6 (CLI ergonomics for humans + agents, implemented)

Three dogfood-born conveniences; no protocol/access-model change beyond one SSE
query param.

### Per-directory board link (`link`/`unlink`/`link --show`)

`session-notes link <ref>` pins a default board for the cwd (railway-style), so
`edit`/`watch` need no `--board`. `<ref>` is anything `--board` accepts: a local
path (stored absolute) or `http(s)://host/b/<board>[#<node>]`. Storage is a
map of absolute-dir → ref in `links.json` (config dir, 0600, beside
`tokens.json`); lookups walk up parent dirs, git-style. `unlink` drops the cwd's
entry; `link --show` prints the effective ref and the directory it came from.

**Resolution priority** (edit + watch): explicit `--board`/`--session` > the
directory's link > error. An explicit flag always wins. A `#<node>` in a *local*
linked ref becomes the default `--root` (edit) / `--node` (watch); a remote ref
keeps its fragment for `ParseRef`, same as `--board`. The TUI's session/pane
resolution is unchanged (link scopes to the `--board`-taking commands).

### `watch --json`

Each reported change is one JSON line — `{"op":"add"|"remove","line","id",
"author","board","node"}` — for agents that parse instead of scraping `+/-`
lines. `line` is the item's source with its `^id` anchor stripped, `id` that
anchor, `board` the ref, `node` the watch root. Works for the local file watch
and the remote SSE watch; implies `--no-notes` (no JSON shape for note events).
`author` is filled only when known (empty for the file watcher and SSE). The
human `+/-` diff stays the default.

### Self-edit suppression for remote watch (`watch --ignore-author`)

Local watch already suppresses self-edits via the shared `--snapshot`; the
remote SSE path had nothing. `watch --ignore-author <name>` passes
`?ignoreAuthor=<name>` to the events stream; the server drops any change whose
journalled ops (the `ops` table already records author per version) are **all**
authored by that name — filtering lives server-side at the SSE handler via
`Store.AuthorsSince`. A remote `edit` already sends a stable author: the client
sends no author, and the server defaults it to the token subject. So an agent
loop is: edit as subject S, `watch --ignore-author S` — no own-echo wakeups.

### Exit criteria (met)

1. link/unlink round-trip incl. parent-dir walk; explicit `--board` beats a link.
2. `watch --json` emits the documented line shape (local file + remote httptest).
3. ignore-author end-to-end: the watcher's own edit is silent, another author's
   edit fires.

## M7 (human affordances for the subtree/cloud product, in progress)

M7 puts human hands on the M3–M6 subtree/cloud machinery through the web UI. No
protocol/access-model change: it reuses the M4 grant endpoint and the shared
zoom addressing. Two of the three planned features shipped; the third (TUI
attaches to remote boards) is deferred — see "Cut" below.

### Share a node from the web UI (cloud, admin only)

From the board UI on a cloud server, an admin selects (or zooms to) a node and
mints a scoped attach link — the browser path to the M4 carve-out handoff that
was previously CLI-only (`remote grant --new-token`).

- **Feature detection.** A new `GET /api/me` on the cloud server returns
  `{subject, admin, cloud:true}`. The browser fetches it on load; the file-mode
  `serve` UI has **no** such route (404), so `canShare` stays false and the
  action is inert there. Only `cloud && admin` reveals it.
- **Trigger.** Keyboard `s`, a header ⤴ button, and the map touch toolbar. The
  target is the outline cursor's item/section or the map's focused node (by its
  durable id).
- **Endpoint reuse.** The action POSTs the existing `…/grants` (the M4
  `newToken` flow) with `{newToken, root:<node id>, perm}` — no new grant
  endpoint was needed. The server mints a non-admin token scoped to that node
  and returns it.
- **Output.** A copyable block: the attach command (`session-notes login <server>
  --token <t> && session-notes link <server>/b/<board>#<node>`, the M6 link
  flow) and a browser `?token=` URL. Revoke via `remote revoke --token-name`.
- Keymap gains a web-only `s` (outline + map).

### Outline re-root zoom (deferred from M4), shared with the map

The outline gains a **true re-root zoom** (`f`): the chosen node renders as root
with a breadcrumb back, everything else hidden — distinct from the retained
fold-based `z` (which collapses, not re-roots).

- **Shared zoom root.** The zoom root id is shared across views
  (`mapState.rootId`) and mirrored to the `#<node-id>` URL fragment in **both
  directions** (load seed + `hashchange`). A deep-link re-roots whichever view is
  active; switching views preserves the zoom (exit map → zoomed outline; `m`
  from a zoomed outline → zoomed map). The one-time fragment seed runs before the
  first render so the outline's hash-sync can't wipe it.
- **Breadcrumb / step-out.** Board › section › ancestors › root; each crumb
  re-roots to it, the board crumb clears the zoom. `Escape` steps out one level
  (a top-level item → its section → whole board).
- **Edit integrity.** Stops keep each item's real owning section, so
  status/urgent/edit/reply ops still address correctly inside a re-rooted item
  view. The M4 scoped-token subtree view composes: the server already returns
  only the subtree, and zoom then re-roots within it.

### Exit criteria

1. `/api/me` reports admin vs scoped vs 401; the share-grant path mints a
   node-scoped token for an admin and 403s a non-admin (Go tests).
2. Outline `f` re-roots with a breadcrumb and `#id`; `Escape`/crumb step out;
   the `#id` fragment round-trips (load + hashchange); a view switch preserves
   the zoom (e2e).
3. gofmt/vet/`go test ./...` green; the web e2e suite green.

### Cut: TUI attaches to remote boards

The third planned feature — opening the bubbletea TUI on a `RemoteTree`-backed
board (`--board https://host/b/<board>`) — was **cut** as disproportionately
risky for this milestone. The TUI's mutation/reload path is file-path-centric at
many seams (`board.SaveRebasing(m.path, m.lastDisk, …)`, `board.Undo(m.path)`,
`os.ReadFile(m.path)`, an fsnotify directory watcher), and the rebase machinery
is fundamentally file/flock-based, whereas a remote board conflict-resolves via
`RemoteTree.Apply`'s version `base`. Bridging cleanly needs a `BoardStore`
interface over load/save/watch threaded through the model plus SSE-driven
refresh, remote-undo greying, and `$EDITOR`-merge disabling — a large change best
done as its own milestone rather than forced in alongside the two web features.
The remote read/edit/watch path itself already exists (`internal/cloud`,
exercised by `edit`/`watch`); only the TUI front-end binding is deferred.

## M9 (soak-driven robustness fixes, implemented)

The M8 overnight soak (45m, 20 agents, 208k writes) found **zero correctness
bugs** but three performance/robustness issues plus two API footguns. M9 fixes
exactly those — no protocol or access-model change beyond the SSE payload and
`/history` query params.

### Journal format: stored line-diffs, not snapshots (+ pruning)

The `ops` journal stored the full `before` AND `after` board text per op —
O(ops × board_size) disk (943 MB journal for 154 KB of live board). `journalTx`
now stores only the **line-diff** (`added`/`removed`, newline-joined) computed by
`board.DiffLines` — exactly what `/history` renders, so no user-visible fidelity
is lost. `before`/`after` columns are retained (default `''`) but written empty.
Two extra bounds keep the table small: each board's journal is **pruned to the
newest `journalKeep=500` rows** (amortized every `pruneEvery=64` versions), and
diffs are tiny to begin with.

**Migration.** A one-time, idempotent conversion guarded by `PRAGMA
user_version` (`journalSchemaVersion=1`): on `Open`, any legacy row still
carrying a full snapshot is converted to a diff, its snapshot cleared, every
board pruned to `journalKeep`, and the db `VACUUM`ed to reclaim space. A fresh or
already-migrated db skips the work.

**Undo/journal consumers.** The cloud journal is read only by `/history` (via the
diff) and `AuthorsSince` (author only); remote undo was refused in M3, and
nothing reads `before`/`after` for state reconstruction — so dropping the
snapshots breaks no consumer.

### `/history` pagination + WAL read pool

`GET …/history` now takes `?limit` (default 100, capped at `journalKeep`) and
`?before=<op id>` cursor; the response carries a `before` cursor for the next
(older) page when the page is full. `Store.History(id, limit, before)` applies a
bounded `LIMIT` and `id < before` filter server-side (newest-first), so a GET no
longer loads and diffs the entire journal. Separately, `SetMaxOpenConns(1)` is
lifted to **8 for file dbs** (kept at 1 for `:memory:`, whose per-connection
databases can't be pooled): WAL supports one writer + many concurrent readers, so
`/history`/`/healthz`/edits no longer queue head-of-line behind one connection.
Writes stay single-writer via the per-board lock plus `_txlock=immediate` (write
lock taken up front, contention waits out `busy_timeout` instead of erroring);
per-connection pragmas ride in the DSN so every pooled connection applies them.

### Unknown-field policy on POST bodies

The **grant, revoke-grant, and create-token** handlers now set
`DisallowUnknownFields` — a typo'd field (e.g. `"node"` instead of `"root"`)
400s instead of being silently ignored and minting a broader grant than intended
(over-granting is security-adjacent). The **edit** and **raw-push** bodies stay
deliberately lenient: `editReq` is a large optional-field superset shared with
the file-mode web client and meant to be forward-compatible, and an unknown edit
field cannot escalate access (the op is still scope-checked). This split is
intentional: strict where a typo widens access, forgiving where it cannot.

### SSE event carries the change author

The events stream payload changed from a bare `data: changed` to
`data: {"authors":[…]}` (distinct authors of the ops since the client's last-seen
version, via `AuthorsSince`). `board.Event` gains an `Author` field
(comma-joined); the `watch --json` client fills the previously-empty `author` on
each emitted change. The browser board UI polls (`setInterval`), never consumed
SSE, so the payload change affects only the CLI watcher; an older server's bare
`changed` parses to an empty author (back-compatible).

### Exit criteria (met)

1. Journal round-trips diffs and `/history` renders the same added/removed lines
   the old full-snapshot path did; the fat-journal migration converts + prunes an
   existing db (Go tests).
2. `/history` honors `?limit`/`?before` and returns a paging cursor; the read
   pool + immediate txlock hold single-writer semantics under load.
3. Grant/token bodies 400 on unknown fields; no stray grant is minted.
4. A remote `watch --json` event carries the editing author.
5. A short validation soak shows sub-linear journal growth, flat-ish `/history`
   latency, stable edit p99, and zero criticals.
