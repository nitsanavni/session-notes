# Context API draft — fog of war

Sparked by nitsan's context-lens comment: "the agent can always query for more context (maybe we should design this API btw)".

## The model (nitsan's correction, 2026-07-15)

Fog of war is the **context model, not a visualization**: "the agent doesn't need to see infinitely into the sub tree — that's the fog of war, we don't need a fog to visualize it."

An actor at a node sees the ancestor path plus the subtree **only to a shallow depth** (default ~2); deeper levels appear as summaries/child-counts, not content. More context is always one call away. This depth limit is what keeps context cheap and *location meaningful* — if the agent saw everything, where you ask would stop mattering.

The lens UI can therefore be plain: a list of what's included, with `…` marking cut-off depth. No dimmed-map rendering needed.

## The verbs (v0)

- `read_context(node, depth=2)` — the default view: ancestor path (compressed) + subtree truncated at `depth`, with child counts / one-line gists where it was cut.
- `expand(node)` — lift the depth limit locally under a node already in view.
- `query(text) -> nodes` — search across the fog; matches light up, tagged "via query", and join the context. The user *sees* each query as a chip in the lens — agent curiosity is auditable.
- `pin(node)` — explicitly attach a distant node to the current context (the user-side gesture is `q` in the demo; agents can pin too). Stays lit independent of cursor, with a visible connector.

Later candidates (not v0): `drop(node)` (return a node to fog — context budgeting), `watch(node)` (subscribe; ties into the inbox/reply-routing model from the presence brief).

## Why this is good

- **Trust:** every context acquisition is a visible event (chip + light). No hidden retrieval.
- **Budget:** lit area ≈ token cost. A context budget becomes a literal, glanceable quantity of light.
- **Symmetry:** the same fog serves human progressive disclosure and agent progressive retrieval — one mechanism, one visualization, one mental model.

Maps onto the architecture brief's MCP surface: `read_context`/`query`/`pin` are the first three MCP tools the notes server exposes; each call also appends a `context.*` event to the event log, which is exactly what the lens panel renders.
