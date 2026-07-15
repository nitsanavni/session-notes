# Multi-threading & Context-as-Location

## Pearls

**P1 — Context is the path, thread is the subtree.** A conversation isn't a separate object; it's a node. Replying creates a child. Agent context = ancestor path + local subtree. Collapses channel/thread/message into one construct: position. You talk *at a place*, and the place is the topic. The single load-bearing idea.

**P2 — Typed edges, not typed nodes.** Reply, fork, quote are edge types, all rendered as children with distinct visuals: *reply* (continue, flat), *fork* (branch — inherits context to the fork point, then diverges), *quote* (transcluded live reference). Two forks under one node = two parallel conversations sharing a prefix — LLM conversation-branching, but spatial and visible.

**P3 — Quote = context splice.** A quote is a live pointer. Quoting node X into thread Y makes the agent's context Y's path **plus** X's path — a deliberate splice, cheap to resolve (two root-paths, deduped).

**P4 — Threads are scaffolding; distillation is a first-class gesture.** Any thread can be *distilled* — agent summarizes the exchange into 1–3 durable bullets that replace it; the raw transcript collapses behind the distilled note (zoomable, never lost). One keystroke, slightly ceremonial (satisfying collapse animation) so users actually do it.

**P5 — Explicit context lens.** A thin "what I can see" strip before the agent answers: breadcrumb path + quoted splices + recent siblings, each removable/pinnable. Inferred by default, always inspectable. Kills the #1 trust problem without a settings page.

**Killed:** replies-as-siblings (destroys thread locality); separate threads panel (recreates the split); automatic whole-document context (makes location meaningless).

## Interaction sketches

**Reply-in-place:** cursor on bullet → `r` → input slides in as indented child, user-color left rail → type, Enter → context-lens chip flashes (`Product › Pricing › …`) → agent reply streams as sibling message-child, thread runs flat → `d` (distill) → thread scrunches into one bullet with ⌄ affordance marking the buried transcript.

**Fork:** on an agent message, `f` → a second child rail sprouts, offset a few px, different tint — two parallel threads under one message like a subway map splitting; each fork shows a faint "inherits ↑ to here" marker.

**Quote-splice:** `>` opens fuzzy picker over the outline → chosen node appears embedded, recessed, with its own mini-breadcrumb → context lens shows two breadcrumbs joined by `+` → hovering the quote highlights the node at its home location.

## Demo specs

**threads.html** (~200 lines) — hardcoded 12-node outline. `j/k` move, `r` reply (canned agent responses stream word-by-word after 600ms), `f` fork (offset second rail, spring), `d` distill (scale+fade collapse ~300ms, ⌄ remnant). Context-lens breadcrumb chip animates above the input on every reply. Goal: conversation lives inside the outline and evaporates into notes.

**context-lens.html** (~150 lines) — split view: left outline with two distant subtrees; right "what the agent sees" panel. Cursor moves animate path chips flying in/out. `q` quotes the other subtree in — chips slide in joined by `+`, curved SVG connector draws between the two outline locations. Purely about making context visible and spatial.

## Risks

- Fork sprawl ≥3 deep unreadable → non-active forks auto-collapse to labeled stubs.
- Distill trust: buried transcript must be obviously reversible.
- Sibling leakage: default probably path + current thread only; siblings opt-in per-reply (lens makes either survivable).
- Quote staleness: answers pin a snapshot hash; lens marks stale quotes.
- Thread identity: agent memory needs stable node IDs, not paths.
