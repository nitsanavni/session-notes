# Agents as First-Class Citizens in the Outline

## Pearls

**P1 — The agent is a cursor, not a chat window.** An attached agent has a *position* in the tree, like a collaborator's caret in Google Docs. Identity = a small colored glyph in the bullet gutter; its context *is* its position (subtree rooted where attached). Dissolves the chat-panel/document split — you never leave the outline, you write near the agent.

**P2 — Attention trail with decay.** Don't show logs; show *heat*. Nodes the agent recently read glow faintly, fading over ~10s; the node it's writing pulses. Footprints in snow, not a diary. Zero text, fully peripheral.

**P3 — Reply-to *is* addressing; position *is* context.** Replying under any node inside an agent's subtree routes to that agent, with the ancestor path as context. @-mention only for cross-subtree summons. Context assembly is mechanical and legible: ancestors (why), replied-to node (what), siblings (neighbors) — the user can *see* the context because it's the visible tree.

**P4 — Agents write in proposal ink.** Output streams live as "wet ink" — translucent, distinct left border. Tab accepts (ink dries), Shift-Tab rejects (fades out); accept-all at subtree root. Nothing an agent writes is canon until touched, yet you watch it think.

**P5 — Leash = permission gradient.** Spawn node carries a leash: read subtree / write subtree / read tree / act (tools). Visualized as a subtle tint behind the subtree (its "pen"). Dragging the glyph re-scopes; attach/detach/handoff is drag-and-drop of a presence glyph.

**Killed:** agent-as-chat-sidebar; free-text DSL as *primary* spawn path (keystroke is faster; DSL is the power layer); activity-log timeline (the tree is the log).

## Interaction sketches

**Spawn ceremony** (<2s, zero dialogs): cursor on node → `ctrl-a` → bullet blooms into a soft ring; inline ghost row `⟡ new agent — leash: [write subtree ▾] — prompt: _` → type one line, Enter → ring becomes a colored glyph (name + hue: *⟡ moss*), subtree gets faint moss tint. Pulse = booting; steady = attached; ripple = thinking. Wet-ink bullets stream in.

**Reply in context:** user adds a child bullet under an agent-written row inside moss's pen → it renders a tiny `→ ⟡` affordance (who will read it, ancestors ride along) → heat moves there → wet-ink reply grows under it.

**Detach / handoff:** drag glyph — pen tint follows like a lens. Drop on sibling subtree = re-attach (context swap, confirmation flash). Drop on edge dock strip = detach (parks dimmed, resumable). Drop onto another agent's glyph = handoff (writes a summary bullet, parks).

**Glanceability:** steady glyph+tint = idle; heat trail = reading; pulse = writing; gutter ripple = long tool call. Nothing animates when nothing happens.

## Demo specs

**presence.html** — hardcoded outline (~15 nodes). Fake agent attaches mid-tree on load; scripted loop: heat sweeps 3–4 descendants (10s decay) → wet-ink bullets stream char-by-char → idle. Hover glyph shows pen tint. Buttons: "spawn second agent" (different hue/subtree, concurrent — the money shot is two heat trails at once) and "replay". ~200 lines, CSS transitions only.

**spawn.html** — same outline. `ctrl-a` runs the spawn ceremony (canned prompt). Typing a child bullet in the pen shows `→ ⟡` routing and triggers a canned wet-ink reply with Tab-to-accept (ink dries: opacity+border animate). Glyph draggable to one other node and to a dock strip. Sells P3+P4+P5 in 60 seconds.

## Risks

- Heat vs honesty: real agents read context in bursts; the trail is partly theater. Decide truthful telemetry vs legible fiction.
- Wet ink at volume: 40 streamed bullets make per-bullet accept unbearable → subtree-level accept, auto-dry for low-stakes leashes.
- Protocol: wants MCP verbs `read_subtree / propose_bullets / move_attention / request_rescope` + hook injecting replies-in-pen as prompts. Turn-based sessions need a wake mechanism (file watch / SSE); the board protocol is prior art.
- Overlapping pens need a stacking rule — nearest-pen-wins default.
- Hues stop scaling past ~5 agents; optimize for 1–3.
