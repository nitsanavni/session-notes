# Architecture & Blank-Slate Aesthetic

## Pearls

**P1 — The node is the unit of everything.** One table of nodes; "outline", "thread", "agent conversation" are edge types over the same nodes. Zooming into a node = entering a thread = giving an agent context. No modes.

**P2 — Context is a path, not a prompt.** Addressing an agent at N yields the spine: root→N ancestors (compressed), N's subtree (full), quoted nodes (transcluded). Cheap with a closure table; the agent gets exactly what a zoomed-in human sees.

**P3 — Presence as cursor, not avatar.** One glowing gutter dot at the node the agent is reading/writing, trail fading ~3s. You can *see* an agent walk the outline. This single visualization sells the product.

**P4 — Agent output is a proposal layer.** Agent nodes arrive "pending" (translucent) until touched, which solidifies them. The agent never clutters; it offers.

**P5 — The event log IS the sync protocol.** Append-only `events` table; SSE streams it; web UI, MCP server, future TUI are all event-log consumers. Presence, edits, comments — one stream.

## v0 data model + attach protocol

```sql
CREATE TABLE nodes (
  id TEXT PRIMARY KEY,          -- ulid
  content TEXT NOT NULL,
  author TEXT NOT NULL,         -- 'user' | agent id
  created_at INTEGER, updated_at INTEGER,
  state TEXT DEFAULT 'solid'    -- 'pending' | 'solid' | 'collapsed-done'
);
CREATE TABLE edges (
  parent TEXT, child TEXT,
  kind TEXT NOT NULL,           -- 'child' | 'reply' | 'quote' | 'transclude'
  rank TEXT,                    -- fractional index
  PRIMARY KEY (parent, child, kind)
);
CREATE TABLE events (
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  ts INTEGER, actor TEXT,
  kind TEXT,                    -- node.add|node.edit|edge.add|presence.move|agent.spawn|...
  payload TEXT
);
CREATE TABLE agents (
  id TEXT PRIMARY KEY, name TEXT,
  session_id TEXT, status TEXT, -- thinking|writing|idle|detached
  at_node TEXT
);
```

Concurrency: LWW per node + fractional ranks. No CRDTs in v0 (single server, one user + N agents); revisit only for multi-device offline.

**Attach: MCP first, hooks second.** Notes server exposes MCP (streamable HTTP): `read_context(node_id)`, `append(node_id, kind, content)`, `move_presence(node_id)`, `watch(node_id)`. UI spawn = server runs `claude --mcp` pointed at itself, seeded with "you live at node X; your inbox is replies under X". Hooks stay for ambient attachment of dev sessions (today's boards). MCP gives agents verbs; hooks only gave them a file.

## Mini style-guide (shared by all demos)

- Type: system-ui/Inter; 15px/1.6; agent text same face at weight 450 + soft ink (voice, not chrome). Node text is the UI.
- Ink: light `#1a1a1a`/`#fbfbfa`; dark `#e8e8e6`/`#141413`. Soft ink ±35% toward bg. Hairlines 8% ink.
- One accent, meaning only: agent presence `#6c8eef`; pending 65% opacity; blocked `#d97757` sparingly. Links = ink + underline.
- Motion: 160ms ease-out standard; zoom 220ms FLIP shared-element; presence dot 300ms ease-in-out; transform/opacity only; nothing bounces or blocks.
- Space: 28px indent/depth, 6px sibling gap, 44rem column; chrome on hover/focus only.
- Steal: iA Writer (text-as-interface, focus=zoom), Linear (motion discipline, keyboard-first), Muse (spatial memory, calm). Tana's node-typing power, hidden.

## Scratch vs evolve

Fresh module in this repo: `internal/graph` + new app command, new SQLite store, new web dir. Keep: Go server skeleton, SSE plumbing experience, hooks knowledge, e2e harness pattern. Drop: markdown-file-as-store, lock/snapshot protocol, TUI-first, keymap machinery. "Starting from scratch with good neighbors."

## Risks

- Zoom smoothness is make-or-break; prototype with virtualization in mind (1k+ nodes).
- Agent spawning = process management: orphans, auth, cost. v0: cap concurrency, kill switch on every presence dot.
- LWW can eat user text → agents may not edit user-authored nodes, only append/propose.
- Scope gravity: this is four products unless held to one spine — *a tree you and agents inhabit together*.
