# Context API draft — fog of war

Sparked by nitsan's context-lens comment: "the agent can always query for more context (maybe we should design this API btw)".

## The metaphor

The tree is fogged. Every actor — human or agent — carries a light:

- **Human:** zooming lifts fog one level at a time (camera-zoom demo).
- **Agent:** starts with a small lit circle and lifts fog by asking (context-lens demo).

Context is therefore not a static prompt but a *lit region* — visible to the user at all times in the lens panel, and cheap to reason about ("what does it know?" = "what is lit?").

## The verbs (v0)

- `read_context(node)` — the free light: ancestor path (compressed) + local subtree (full). What you get just by being somewhere.
- `query(text) -> nodes` — search across the fog; matches light up, tagged "via query", and join the context. The user *sees* each query as a chip in the lens — agent curiosity is auditable.
- `pin(node)` — explicitly attach a distant node to the current context (the user-side gesture is `q` in the demo; agents can pin too). Stays lit independent of cursor, with a visible connector.

Later candidates (not v0): `expand(node)` (lift fog one level under a lit node), `drop(node)` (return a node to fog — context budgeting), `watch(node)` (subscribe; ties into the inbox/reply-routing model from the presence brief).

## Why this is good

- **Trust:** every context acquisition is a visible event (chip + light). No hidden retrieval.
- **Budget:** lit area ≈ token cost. A context budget becomes a literal, glanceable quantity of light.
- **Symmetry:** the same fog serves human progressive disclosure and agent progressive retrieval — one mechanism, one visualization, one mental model.

Maps onto the architecture brief's MCP surface: `read_context`/`query`/`pin` are the first three MCP tools the notes server exposes; each call also appends a `context.*` event to the event log, which is exactly what the lens panel renders.
