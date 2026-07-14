# Changelog

All notable changes to session-notes are recorded here. Dates are ISO-8601.

## 2026-07-14

The tree/cloud release. session-notes grows from a per-session scratchpad into a
collaborative infinite-outline board: every bullet is an addressable node, any
subtree can be zoomed/watched/edited as its own root, and an optional cloud
server turns that subtree carve-out into a real, token-enforced permission.

**Compatibility.** No breaking changes for local file mode — existing boards
parse and behave exactly as before; node-id anchors are added lazily on the
first programmatic save. The one addition to adopt is optional: **cloud mode**
(`session-notes server`).

### Added

- **Node ids + `board#node` addressing** — every bullet gets a stable trailing
  `^id` anchor on its first CLI save (lazy, non-destructive); `<board>#<id>` is
  the single addressing scheme across CLI, web, TUI, `watch`, and remote URLs.
- **Subtree carve-outs** — `edit … --root <id>` confines every write to one
  node's subtree, and `watch --node <id>` (or `--node BOARD#<id>`) reports only
  changes inside it. Enforced at one op-resolution choke point, so it holds for
  every subcommand and client. See `docs subtree`.
- **Dual-view zoom** — a true re-root zoom (`f`) shared between the outline and
  the mindmap in both the TUI and the web UI, with a breadcrumb back out and a
  `#<node-id>` URL fragment that deep-links (and round-trips) a zoomed view.
- **Cloud mode (`session-notes server`)** — a SQLite-backed (WAL, pure-Go, no
  cgo) HTTP+SSE server that lets any Claude session anywhere attach to a board,
  or a single granted subtree, with the same watch/edit protocol as a local
  file. Token identity + a **grants ACL** (`(subject, board, node, perm)`,
  `read|write|admin`) make the subtree carve-out server-enforced: a scoped token
  cannot fetch, watch, or edit anything outside its granted node.
- **One-line agent handoff** — `remote grant …/b/board#<node> --new-token scout
  --perm write` mints a scoped token and prints a paste-ready attach line;
  `login` / `remote new|push|pull|grants|revoke` / `server token
  create|list|revoke` round out the CLI. Admins can also mint a scoped attach
  link from the web board UI (`s`).
- **Per-directory board link** — `session-notes link <ref>` pins a default board
  (local path or `http(s)://host/b/<board>[#<node>]`) for the cwd so `edit` /
  `watch` need no `--board`; walks up parent dirs, git-style. `unlink`,
  `link --show`.
- **`watch --json`** — one JSON line per change (`op`, `line`, `id`, `author`,
  `board`, `node`) for agents that parse instead of scraping `+/-` diffs; local
  file watch and remote SSE. **`watch --ignore-author <name>`** suppresses a
  remote watcher's own edits server-side.
- **`session-notes demo`** — one command: seed a throwaway board with a few
  subtrees, serve it locally on a free port, and print things to try. File-mode,
  no tokens, torn down on Ctrl-C.
- **`docs server` / `docs subtree`** topics, and a full production deploy runbook
  in [`deploy/README.md`](deploy/README.md) (Hetzner from zero, Docker Compose
  and bare systemd, Caddy TLS, Litestream + nightly VACUUM backups, restore
  drill, upgrades).

### Changed

- **Shared undo journal** across every writer (web UI, `session-notes edit`,
  TUI, hooks) — one timeline that survives server restarts; `u` in the browser
  can revert a CLI edit and vice-versa, with a refusal rather than clobbering an
  intervening unjournaled write.
- **Cloud durability + hardening** — the `ops` journal stores compact line-diffs
  (not full snapshots) with pruning and a one-time migration; `/history`
  paginates (`?limit`/`?before`) over a WAL read pool; grant/token bodies reject
  unknown fields; SSE events carry the change author; `/healthz`, a 1 MiB body
  guard, per-request access logging, and graceful SIGTERM shutdown.
- **Robustness** — hardened from an overnight soak (20 agents, 208k writes) that
  surfaced zero correctness bugs; the fixes above address journal growth,
  `/history` latency, and edit-tail stability under load.

### Fixed

- Web: external board changes are delivered to a between-poll watcher instead of
  being silently snapshotted as seen; automatic delivery receipts from the watch
  snapshot; text inputs commit via form submission for mobile keyboards.
