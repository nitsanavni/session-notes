# Vision: a tree you and agents inhabit together

Synthesis of five ideation tracks (zoom UX, agent presence, threading/context, DSL, architecture+aesthetic). 2026-07-15.

## The spine

One idea holds everything: **the outline is a single continuous place — notes, conversations, and agents all live at positions in it.** Not four features; one spine:

1. **Zoom is a camera, not a filter.** The whole tree is one surface; zooming in/out is camera motion. Nothing appears/disappears without a spatial explanation. Distance = summarization level (semantic zoom): far away a node is a title chip, close up it's full content + threads. Collapse doesn't exist; "collapsed" is just "far away."
2. **Context is the path.** Talking to an agent at a node gives it the ancestor spine + local subtree. What the agent knows is literally the visible tree around your reply — inspectable via a "context lens" strip. Quoting another node splices its path in (transclusion, not copy).
3. **The agent is a cursor, not a chat window.** An attached agent has a position, a hue, a "pen" (permission-scoped subtree). Its attention renders as heat that fades like footprints. Agent output streams in as "wet ink" — translucent proposals you accept (Tab) or reject; the agent never edits your text, only appends.
4. **Structure is the harness.** Almost no DSL: `@name:` spawns with path-as-context, `>>` steers an attached agent, `[ ]/[~]/[x]/[?]` is the lifecycle, `↳ name:` marks agent replies, `[[^id]]` transcludes results. Fan-out = N children; pipeline = a node referencing done nodes; judge = a skeptic child. With agents off, the same bytes read as ordinary nested notes.
5. **Threads are scaffolding; notes are the product.** Reply = child, fork = two children, quote = a pointer edge. Behaviors like *distill* (collapse a thread into durable bullets) are **not** core — they're an agent doing ordinary operations with the primitives.
6. **Architecture:** Go + SQLite. One `nodes` table, typed `edges` (child/reply/quote/transclude), append-only `events` table streamed over SSE as the sync/presence protocol. Agents attach via an MCP server the notes server exposes (`read_context`, `append`, `move_presence`); Claude Code hooks remain for ambient attachment. Last-write-wins + fractional ranks; no CRDTs in v0. Fresh module in this repo ("scratch with good neighbors").

## Primitives, not features (working agreement, 2026-07-15)

nitsan, reviewing the threads demo: "we need to create *primitives*, like threads, not encode higher level functions like 'distill'".

The core is exactly four primitives:

1. **Node** — one block of content.
2. **Typed edge** — child / reply / quote (+ rank).
3. **Position-as-context** — an actor at a node sees its path + local subtree, and can query/pin to see more (fog of war).
4. **Authorship** — every node knows who wrote it (user or which agent); agent-authored nodes arrive as proposals.

The litmus test for anything else: *could an agent build it using the primitives?* Distill = write a summary node + re-parent the thread. Fork = add a second child. Judge panel = spawn skeptic children. All derived; none belong in the core or the DSL. The product ships primitives plus great defaults built on them — the defaults are replaceable, the primitives are not.

## Aesthetic (all demos share this)

- Type: system-ui/Inter, 15px/1.6, content column max 44rem. Text is the interface; chrome on hover only.
- Ink: `#1a1a1a` on `#fbfbfa` (light), `#e8e8e6` on `#141413` (dark). Hairlines at 8% ink.
- One accent with meaning: agent presence = `#6c8eef`; blocked/urgent = `#d97757` sparingly. Nothing else gets color.
- Motion: 160–220ms ease-out, transform/opacity only, FLIP continuity for zoom, nothing bounces, nothing blocks input.
- Steal from: iA Writer (text-as-interface), Linear (motion discipline, keyboard-first), Muse (spatial calm).

## Full briefs

The five ideation briefs are preserved in `briefs/` — pearls, killed ideas, interaction sketches, risks.

## Demos (in `gallery/`)

Run `node gallery/server.mjs` → http://localhost:4700. Every demo has a comment rail (press `c`); comments persist to `gallery/comments/*.json` where Claude reads them.
