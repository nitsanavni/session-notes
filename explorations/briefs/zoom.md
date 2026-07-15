# Infinite Zoom as the Primary UX

## Pearls

**P1 — Zoom is a camera, not a filter.** Workflowy's zoom is a re-render. Kill that. Model the whole tree as one continuous surface with a camera. "Zooming into a node" is the camera translating and scaling toward that node's region; siblings slide off-canvas, ancestors compress into the breadcrumb *by visibly shrinking into it*. Nothing ever appears or disappears without a spatial explanation. This is what makes it feel like navigating a place instead of paging through views.

**P2 — Semantic zoom: distance = summarization level.** A node's render is a function of its apparent size. Far (<~40px): title chip. Mid: title + one-line gist + child count + activity dots. Close: full content, thread replies, attachments. Zooming is *the* progressive-disclosure mechanism — no triangles; collapse is just "far away." The far view of a subtree an agent worked in is literally the agent's summary of it.

**P3 — Agent activity as light.** An agent working 3 levels deep emits a glow that propagates up: the deep node pulses, ancestors show a soft ember on their edge. From the root you see where the tree is alive, like lit windows at night. Zooming toward the glow is how you check on the agent. Unread replies use the same channel, dimmer.

**P4 — Outline and mind-map are one layout with two parameter values.** Both are positions computed from the same tree: outline = x from depth, y from visit order; map = radial. A scalar `t ∈ [0,1]` interpolates every node's position, text alignment, connector curvature. Free, continuous, interruptible (t=0.5 is itself a usable "staircase" view).

**P5 — Breadcrumb as spatial memory / zoom history.** The breadcrumb is the compressed remains of what you zoomed past, and a scrubber: hover ghost-previews, click animates the camera back *through* intermediate levels (~80ms/level) rather than teleporting. `[`/`]` = camera history.

**Killed:** free-form infinite canvas (positions become user homework); 3D z-depth (gimmick); pinch as primary on desktop.

## Interaction sketches

**Zoom in (keyboard).** Cursor on node, `Ctrl-→`/`Enter`: (0ms) node highlights, siblings fade to 0.3 and slide apart; (~120ms) camera translates/scales, title glides up-left into breadcrumb slot shrinking, children scale up from inline positions to full rows, chips rewrap to full text with crossfade at midpoint; (250ms) settle, slight ease-out overshoot (1.01→1.0), new crumb slides in. 250–300ms, `cubic-bezier(0.2,0,0.2,1)`. Zoom out must be visually symmetric or spatial memory breaks.

**Zoom toward the glow.** Ember on a deep subtree; `g` + hotkey (embers labeled 1–9) or click: camera dives *through* intermediate levels (~70ms each), breadcrumb accreting, landing on the node where the agent is typing.

**Morph to map.** `m`: every node slides along a curved path from outline to radial position (300ms, staggered 8ms by depth — children arrive after parents, reads as blooming). Bullets become cards; indent guides bend into edges. Cursor persists; arrows work in both projections.

## Demo specs

**camera-zoom.html** — 4-level fake tree (~40 nodes). One world div, CSS transform camera. Click/Enter zoom-in choreography, Esc zoom-out, animated breadcrumb, semantic zoom faked with three canned render sizes crossfading at thresholds, 2 glowing embers with `g` dive-through. Success = "oh, it's a *place*."

**morph.html** — same tree ~25 nodes, two position functions, slider + `m` animating t, SVG connectors interpolating indent-guides→curves, depth-staggered, arrows work at t=0 and t=1. Success = interruptible, never soup mid-flight.

## Risks

- CSS-scaling text mid-animation blurs/reflows; likely FLIP transforms in flight, real reflow on settle — demos should validate this.
- Semantic-zoom summaries need a summarizer; manual trees show bare chips.
- Map mode >200 nodes needs culling/clustering.
- Split panes vs "one camera" unresolved.
- Motion fatigue: reduced-motion path + instant-repeat (held zoom-out ~80ms/level).
