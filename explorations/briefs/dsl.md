# The Outline IS the Harness (DSL)

## Pearls

**P1 — Structure is semantics; no new block syntax.** The tree already encodes what a harness needs: siblings = parallel, children = sub-tasks/context, indentation = scope. The DSL adds only *addressing* (who acts) and *status* (what state). A fan-out isn't a construct you write; it's what happens when you address an agent at a node with N children. **The harness patterns fall out of the tree for free.**

**P2 — Context is the path from root.** `@claude: …` at a node gives the agent the ancestor chain (optionally siblings). No hidden system prompts — the note is the prompt; you can see what the agent knows by looking up the tree.

**P3 — Agents leave replies as children, never edits.** Output is appended as attributed child bullets (`↳ claude:`). Human text is never rewritten. Threading, auditability, and safe degradation — a transcript that reads as nested notes.

**P4 — Status glyph doubles as the spawn verb.** `[ ]` todo → flip to `[>]` and the node *becomes* an agent task. Running `[~]` (animated in UI, plain char on disk), done `[x]` with results as children. One character is the whole lifecycle API — unifies GTD checkboxes with agent dispatch.

**P5 — Node references make results composable.** `[[slug]]` transcludes another node's subtree (its result). References are human-friendly slugs derived from node text (`[[playful]]`, `[[intro/technical]]` to disambiguate), auto-completed by a picker when you type `[[`; stable node ids exist only behind the scenes (nitsan: "not using IDs but slugs maybe, can do it w ids behind the scenes"). A pipeline is node B referencing node A. Wiki-links are the wires.

**Killed:** fenced ```agent blocks (Jupyter's mistake in an outliner); `?` question-prefix (addressing *is* asking); required YAML attributes; explicit `parallel{}`/`loop{}` keywords.

## The six constructs

| # | Syntax | Meaning |
|---|--------|---------|
| 1 | `@name: text` | Spawn agent here; context = ancestor path |
| 2 | `>> text` | Address the agent already attached at/above |
| 3 | `[ ] [~] [x] [?]` | pending / running / done / blocked |
| 4 | `↳ name: text` | Agent-authored reply |
| 5 | `[[^id]]` | Transclude another node's subtree |
| 6 | `{k=v}` trailing | Optional: `{model=opus}` `{loop=until-dry}` `{votes=3}` |

Patterns as shape: **fan-out** = `@claude:` node with N `[ ]` children (one agent per child, parallel). **Verify** = child `@skeptic: refute this {votes=3}`. **Pipeline** = `@claude: synthesize [[^a1]] [[^b2]]` — runs when referenced nodes are `[x]`. **Loop** = `{loop=until-dry}`.

Worked scenario:

```markdown
- Launch blog post ^p1
  - draft angle: "outline as harness" ^p2
  - [~] @claude: write three intro paragraphs, different tones {votes=judge}
    - [x] playful ^i1
      - ↳ claude: What if your to-do list could do itself? …
    - [~] technical ^i2
    - [ ] narrative ^i3
  - [ ] @claude: pick the best of [[^i1]] [[^i2]] [[^i3]], explain why
  - >> keep it under 800 words total
```

Agents off: a nested to-do list. Agents on: a judge-panel fan-out feeding synthesis, with mid-flight steering. **Same bytes.**

Prior art: org-babel (results are opaque blocks, not tree children), Jupyter (linear, no ancestry-context), Observable (`[[^id]]` is its cheap cousin), session-notes board protocol (validated `↳` replies + statuses — this DSL is that protocol promoted to the product).

## Demo specs

**dsl.html "Type it alive"** — editable outline, 4 seeded bullets. Ghost-typing writes `@claude: summarize the three points above`; on `↵` the token snaps into a colored pill, `[~]` spinner blooms, ancestor path glows (context made visible), 2 child bullets stream word-by-word as `↳ claude:`, status ticks `[x]` with a soft pop. Second beat: ghost types `>> shorter`, one bullet rewrites streaming. ~15s loop, replay button.

**harness-shape.html "Shape is the harness"** — split screen: outline left, live mini node-graph right. Ghost types a fan-out (three `[ ]` children under `@claude:`) → three spinners run concurrently, staggered finishes → sibling `@claude: pick the best of [[^i1]] [[^i2]] [[^i3]]` shows dotted edges snaking from the done nodes into it on the graph before running. Sells fan-out, pipelines, outline↔graph duality. ~20s loop.

## Risks

- `@name` vs human mentions — reserve `@` for agents.
- `>>` mid-flight steering is the hardest runtime bit; don't overpromise.
- When does `@claude:` fire? Explicit dispatch (Enter on line, or `[ ]`→`[>]` flip), not auto-run-on-edit.
- Transclusion cycles need detection.
- `{loop=}` may be one construct too many for v1.
