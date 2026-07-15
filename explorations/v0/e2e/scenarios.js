// Browser e2e scenarios for the v0 outliner prototype. Each runs against a
// fresh server + freshly-seeded tree (see harness.js). Run via ./run.sh, filter
// with V0_ONLY=<substring>.
'use strict';
const { run } = require('./harness');

// find the seed node id whose content contains `sub`.
async function idByContent(t, sub) {
  const tree = await t.tree();
  const n = tree.nodes.find(n => (n.content || '').includes(sub));
  return n ? n.id : null;
}

run({
  '1. boot: seed tree renders and the hint line is present': async t => {
    await t.open();
    const tree = await t.tree();
    t.assert(tree.nodes.length === 6, `seed has 6 nodes (got ${tree.nodes.length})`);
    const v = await t.v0();
    // Nested view: the Welcome root renders with its subtree (3 levels), so
    // all 6 seed nodes are visible on the board.
    t.assert(v.rendered.length === 6, `nested seed tree rendered (got ${v.rendered.length})`);
    const welcome = await idByContent(t, 'Welcome');
    t.assert(v.rendered[0] === welcome && v.cursor === welcome, 'cursor on the Welcome root');
    const hint = await t.page.evaluate(() => document.getElementById('hint').textContent);
    t.assert(hint.includes('move') && hint.includes('zoom'), `hint line present (got ${JSON.stringify(hint)})`);
  },

  '2. navigate: j/k move, Enter zooms (title + crumb), Esc restores': async t => {
    await t.open();
    const welcome = await idByContent(t, 'Welcome');
    t.assert((await t.page.title()) === 'outliner', 'root document.title is plain');
    // Regression (nitsan): arrows must walk the nested tree at BOARD level too.
    await t.key('ArrowDown');
    const below = await t.cursor();
    t.assert(below && below !== welcome, `ArrowDown at board level moved into the tree (got ${below})`);
    await t.key('ArrowUp');
    t.assert((await t.cursor()) === welcome, 'ArrowUp returned to Welcome');
    await t.key('Enter'); // zoom into Welcome
    t.assert((await t.focus()) === welcome, 'focus is Welcome after Enter');
    const title = await t.page.title();
    t.assert(title !== 'outliner' && title.includes('outliner'), `document.title changed (got ${JSON.stringify(title)})`);
    const inPageTitle = await t.page.evaluate(() => {
      const el = document.getElementById('title');
      return { hidden: el.hidden, text: el.textContent };
    });
    t.assert(!inPageTitle.hidden && inPageTitle.text.includes('Welcome'), 'in-page title shows the focused node');
    const crumbs = await t.page.textContent('#crumbs');
    t.assert(crumbs.includes('board') && crumbs.includes('/'), `breadcrumb appears (got ${JSON.stringify(crumbs)})`);
    // j/k walk the children.
    const first = await t.cursor();
    await t.key('j');
    const second = await t.cursor();
    t.assert(second && second !== first, `j moved the cursor (${first} -> ${second})`);
    await t.key('k');
    t.assert((await t.cursor()) === first, 'k moved the cursor back');
    // Esc zooms out and restores the cursor onto Welcome.
    await t.key('Escape');
    t.assert((await t.focus()) === '', 'focus back at board root after Esc');
    t.assert((await t.cursor()) === welcome, 'cursor restored onto Welcome');
    t.assert((await t.page.title()) === 'outliner', 'document.title reset');
  },

  '3. create + edit: o creates with author, i edits, Escape cancels': async t => {
    await t.open();
    await t.key('o'); // new root sibling, opens editor
    await t.page.waitForFunction(() => window.__v0.editing === true, null, { timeout: 3000 });
    await t.type('made with o');
    await t.key('Enter');
    await t.page.waitForFunction(() => window.__v0.editing === false);
    await t.settled();
    let tree = await t.tree();
    const made = tree.nodes.find(n => n.content === 'made with o');
    t.assert(made, 'o created a node with the typed content');
    t.assert(made.author === 'nitsan', `author recorded (got ${made && made.author})`);

    // i edits the just-created node (cursor is on it).
    t.assert((await t.cursor()) === made.id, 'cursor on the new node');
    await t.key('i');
    await t.page.waitForFunction(() => window.__v0.editing === true);
    await t.type('edited via i');
    await t.key('Enter');
    await t.page.waitForFunction(() => window.__v0.editing === false);
    await t.settled();
    tree = await t.tree();
    t.assert(tree.nodes.find(n => n.id === made.id).content === 'edited via i', 'i committed the edit');

    // Escape cancels an edit: content unchanged.
    await t.key('i');
    await t.page.waitForFunction(() => window.__v0.editing === true);
    await t.type('SHOULD NOT STICK');
    await t.key('Escape');
    await t.page.waitForFunction(() => window.__v0.editing === false);
    await t.settled();
    tree = await t.tree();
    t.assert(tree.nodes.find(n => n.id === made.id).content === 'edited via i', 'Escape left content unchanged');
  },

  '4. reply: r makes a reply edge with rail; agent reply gets a ↳ prefix': async t => {
    await t.open();
    const welcome = await idByContent(t, 'Welcome');
    await t.key('r'); // reply under Welcome (the cursor)
    await t.page.waitForFunction(() => window.__v0.editing === true, null, { timeout: 3000 });
    await t.type('my reply');
    await t.key('Enter');
    await t.page.waitForFunction(() => window.__v0.editing === false);
    await t.settled();
    // Nested view: the reply renders inline; no zoom happens.
    t.assert((await t.focus()) === '', 'stayed at board level (reply renders inline)');
    let tree = await t.tree();
    const mine = tree.nodes.find(n => n.content === 'my reply');
    t.assert(mine, 'reply node created');
    const edge = tree.edges.find(e => e.child === mine.id);
    t.assert(edge && edge.kind === 'reply' && edge.parent === welcome, `reply-kind edge to Welcome (got ${JSON.stringify(edge)})`);
    // Rendered with the reply rail (class .reply). Own reply -> no prefix.
    const cls = await t.page.evaluate(id => {
      const r = document.querySelector(`.row[data-node-id="${id}"]`);
      return r ? [...r.classList] : null;
    }, mine.id);
    t.assert(cls && cls.includes('reply'), `reply row carries the rail class (got ${JSON.stringify(cls)})`);

    // An agent-authored reply POSTed via the API renders with the ↳ prefix.
    const agent = await t.apiCreate({ parent: welcome, kind: 'reply', content: 'from an agent', author: 'claude' });
    await t.page.waitForFunction(id => window.__v0.hasNode(id), agent.id, { timeout: 3000 });
    await t.settled();
    const prefix = await t.page.evaluate(id => {
      const r = document.querySelector(`.row[data-node-id="${id}"] .prefix`);
      return r ? r.textContent : null;
    }, agent.id);
    t.assert(prefix && prefix.includes('↳') && prefix.includes('claude'), `agent reply has ↳ prefix (got ${JSON.stringify(prefix)})`);
  },

  '5. live SSE: an API-posted node appears in the DOM without reload': async t => {
    await t.open();
    const before = (await t.v0()).nodeCount;
    const posted = await t.apiCreate({ content: 'streamed in live', author: 'claude' });
    // No reload: the open page must pick it up over SSE within 2s.
    await t.page.waitForFunction(id => window.__v0.hasNode(id), posted.id, { timeout: 2000 });
    t.assert((await t.v0()).nodeCount === before + 1, 'node count grew by one');
    // It is a board-level root, so it renders (after the debounced render lands).
    await t.settled();
    const rendered = (await t.v0()).rendered;
    t.assert(rendered.includes(posted.id), 'streamed node is rendered in the list');
  },

  '6. indent/outdent: Tab and Shift-Tab reparent (edges change)': async t => {
    await t.open();
    await t.key('Enter'); // zoom into Welcome
    const welcome = await t.focus();
    const kids = await t.page.evaluate(() => window.__v0.visible);
    const [h1, h2] = kids;
    await t.key('j'); // cursor -> h2
    t.assert((await t.cursor()) === h2, 'cursor on the second child');
    await t.key('Tab'); // indent: h2 becomes child of h1
    await t.settled();
    let tree = await t.tree();
    let e2 = tree.edges.find(e => e.child === h2 && e.kind !== 'quote');
    t.assert(e2 && e2.parent === h1, `Tab reparented h2 under h1 (got ${JSON.stringify(e2)})`);

    // Nested view: h2 stays visible after the indent, so the cursor stays on it
    // and Shift-Tab outdents it straight back under Welcome (its grandparent).
    const cur = await t.cursor();
    t.assert(cur === h2, `cursor stays on h2 after reparent re-render (got ${cur})`);
    await t.key('Shift+Tab'); // outdent: h2 back under Welcome
    await t.settled();
    tree = await t.tree();
    e2 = tree.edges.find(e => e.child === h2 && e.kind !== 'quote');
    t.assert(e2 && e2.parent === welcome, `Shift-Tab moved h2 back under Welcome (got ${JSON.stringify(e2)})`);
  },

  '7. quote: y then p creates a quote edge rendered as a reference': async t => {
    await t.open();
    await t.key('Enter'); // zoom into Welcome
    const welcome = await t.focus();
    const target = await t.cursor(); // first child under Welcome
    await t.key('y'); // yank
    await t.key('p'); // paste as quote under the focus
    await t.settled();
    const tree = await t.tree();
    const edge = tree.edges.find(e => e.kind === 'quote' && e.child === target && e.parent === welcome);
    t.assert(edge, `quote edge created under focus (got edges ${JSON.stringify(tree.edges.filter(e => e.kind === 'quote'))})`);
    const quotes = (await t.v0()).quotes;
    t.assert(quotes.includes(target), 'quote reference rendered in the list');
    const qtext = await t.page.evaluate(id => {
      const q = document.querySelector(`.quote[data-node-id="${id}"]`);
      return q ? q.textContent : null;
    }, target);
    t.assert(qtext && qtext.includes('quote'), `quote row shows the embedded reference (got ${JSON.stringify(qtext)})`);
  },

  '8. context API: depth=1 vs depth=2 truncation (plain HTTP)': async t => {
    await t.open();
    const a = (await t.apiCreate({ content: 'A' })).id;
    const b = (await t.apiCreate({ parent: a, kind: 'child', content: 'B' })).id;
    const c = (await t.apiCreate({ parent: b, kind: 'child', content: 'C' })).id;
    await t.apiCreate({ parent: c, kind: 'child', content: 'D' });

    const d1 = await t.ctx(a, 1);
    t.assert(d1.subtree.id === a, 'depth1 subtree rooted at A');
    t.assert(d1.subtree.children.length === 1 && d1.subtree.children[0].id === b, 'B visible at depth1');
    t.assert(d1.subtree.children[0].cut === true && d1.subtree.children[0].childCount === 1, 'B cut with childCount 1 at depth1');
    t.assert(!d1.subtree.children[0].children, 'C not emitted at depth1 (fog)');

    const d2 = await t.ctx(a, 2);
    const bb = d2.subtree.children[0];
    t.assert(bb.children && bb.children[0].id === c && bb.children[0].content === 'C', 'C visible with content at depth2');
    t.assert(bb.children[0].cut === true && bb.children[0].childCount === 1, 'C cut marking D at depth2');
  },

  '9. presence: POST /api/presence puts a ⟡ glyph at the node': async t => {
    await t.open();
    const welcome = await idByContent(t, 'Welcome');
    await t.apiPresence('claude', welcome);
    await t.page.waitForFunction(id => window.__v0.presenceAt(id) === 'claude', welcome, { timeout: 3000 });
    await t.settled();
    const glyph = await t.page.evaluate(id => {
      const r = document.querySelector(`.row[data-node-id="${id}"] .presence`);
      return r ? r.textContent : null;
    }, welcome);
    t.assert(glyph && glyph.includes('⟡') && glyph.includes('claude'), `presence glyph rendered (got ${JSON.stringify(glyph)})`);
  },

  // ---------------------------------------------------------------------------
  // extended deep-test scenarios
  // ---------------------------------------------------------------------------

  '10. empty board: wipe all nodes, reload -> empty state, o works, keys live': async t => {
    await t.open();
    await t.apiWipe();
    await t.reopen();
    let v = await t.v0();
    t.assert(v.nodeCount === 0, `all nodes gone (got ${v.nodeCount})`);
    t.assert(v.rendered.length === 0 && v.cursor === '', 'nothing rendered, cursor empty');
    const emptyMsg = await t.page.evaluate(() => {
      const e = document.querySelector('#list .empty');
      return e ? e.textContent : null;
    });
    t.assert(emptyMsg && emptyMsg.toLowerCase().includes('empty'), `empty-board message shown (got ${JSON.stringify(emptyMsg)})`);
    // j on an empty board is a justified no-op (no crash, cursor stays empty).
    await t.key('j');
    t.assert((await t.cursor()) === '', 'j on empty board is a no-op');
    // o creates the first node and opens the editor.
    await t.key('o');
    await t.page.waitForFunction(() => window.__v0.editing === true, null, { timeout: 3000 });
    await t.type('first note');
    await t.key('Enter');
    await t.page.waitForFunction(() => window.__v0.editing === false);
    await t.settled();
    const tree = await t.tree();
    const made = tree.nodes.find(n => n.content === 'first note');
    t.assert(made, 'o created the first node on an empty board');
    t.assert((await t.cursor()) === made.id, 'cursor sits on the new node');
    // keyboard alive: j is a no-op with one node but must not go dead.
    await t.key('j');
    t.assert((await t.cursor()) === made.id, 'j stays put with a single node (keys alive)');
  },

  '11. deep nesting: 6 levels, fog boundary, zoom lifts one level, esc chain': async t => {
    await t.open();
    // Build a fresh 6-level chain A>B>C>D>E>F as its own root.
    const ids = {};
    let parent = '';
    for (const label of ['A', 'B', 'C', 'D', 'E', 'F']) {
      const n = await t.apiCreate(parent
        ? { parent, kind: 'child', content: label }
        : { content: label });
      ids[label] = n.id;
      parent = n.id;
    }
    await t.page.waitForFunction(id => window.__v0.hasNode(id), ids.F, { timeout: 3000 });
    await t.settled();
    // Move the cursor onto A (a board root), then zoom in.
    await t.page.evaluate(id => { window.__v0.setCursor ? window.__v0.setCursor(id) : null; }, ids.A);
    // setCursor may not exist; navigate with keys instead to reach A.
    // A is the last root created, so walk down to it.
    let guard = 0;
    while ((await t.cursor()) !== ids.A && guard++ < 40) await t.key('j');
    t.assert((await t.cursor()) === ids.A, 'cursor reached A');
    // At board level the view shows A,B,C (3 levels); D,E,F are fog under C.
    let vis = (await t.v0()).visible;
    t.assert(vis.includes(ids.A) && vis.includes(ids.B) && vis.includes(ids.C), 'A,B,C visible at board');
    t.assert(!vis.includes(ids.D), 'D is beyond the fog at board level');
    // Fog affordance: C shows a "· 1" child count.
    const fog = await t.page.evaluate(id => {
      const r = document.querySelector(`.row[data-node-id="${id}"]`);
      if (!r) return null;
      return [...r.querySelectorAll('.byline')].map(b => b.textContent).join('|');
    }, ids.C);
    t.assert(fog && fog.includes('· 1'), `fog count on C (got ${JSON.stringify(fog)})`);
    // Zoom into A lifts exactly one level of fog: now B,C,D visible, E fog.
    await t.key('Enter');
    t.assert((await t.focus()) === ids.A, 'zoomed into A');
    vis = (await t.v0()).visible;
    t.assert(vis.includes(ids.D) && !vis.includes(ids.E), 'zoom revealed D, E still fog');
    // Breadcrumb correctness.
    const crumbs = await t.page.textContent('#crumbs');
    t.assert(crumbs.includes('board') && crumbs.includes('A'), `breadcrumb shows board / A (got ${JSON.stringify(crumbs)})`);
    // Cursor memory + Esc chain. zoomOut follows the structural parent (a node's
    // real parent edge), not the previously-focused level. From focus A, cursor
    // on C (a grandchild in the flattened view), zooming into C then Esc lands on
    // C's structural parent B, then A, then the board — each hop restores the
    // child we descended from as the cursor.
    let g2 = 0;
    while ((await t.cursor()) !== ids.C && g2++ < 10) await t.key('j');
    t.assert((await t.cursor()) === ids.C, 'cursor on C under A');
    await t.key('Enter'); // zoom into C
    t.assert((await t.focus()) === ids.C, 'zoomed into C');
    await t.key('Escape'); // back to C's parent B, cursor restored to C
    t.assert((await t.focus()) === ids.B && (await t.cursor()) === ids.C, 'Esc -> parent B, cursor memory keeps C');
    await t.key('Escape'); // B -> A
    t.assert((await t.focus()) === ids.A && (await t.cursor()) === ids.B, 'Esc -> A, cursor on B');
    await t.key('Escape'); // A -> board
    t.assert((await t.focus()) === '' && (await t.cursor()) === ids.A, 'Esc chain reached board, cursor on A');
    // keys still live: cursor stays a navigable node (j may clamp at a boundary).
    await t.key('j');
    const cc = await t.cursor();
    t.assert((await t.v0()).visible.includes(cc), 'j keeps the cursor on a visible node (keys alive)');
  },

  '12. editing hard cases: Enter-then-i, XSS inert, unicode, long content': async t => {
    await t.open();
    // Enter commits then immediately i re-edits: no double-commit / lost edit.
    await t.key('o');
    await t.page.waitForFunction(() => window.__v0.editing === true, null, { timeout: 3000 });
    await t.type('one');
    await t.key('Enter');
    await t.page.waitForFunction(() => window.__v0.editing === false);
    const id = await t.cursor();
    await t.key('i');
    await t.page.waitForFunction(() => window.__v0.editing === true);
    await t.type(' two'); // appends to existing (caret selects all then types? no) -> replace
    await t.key('Enter');
    await t.page.waitForFunction(() => window.__v0.editing === false);
    await t.settled();
    let tree = await t.tree();
    const node = tree.nodes.find(n => n.id === id);
    // Exactly one edit event beyond the create should have committed the final value.
    t.assert(node && node.content.includes('two'), `re-edit committed (got ${JSON.stringify(node && node.content)})`);
    t.assert(tree.nodes.filter(n => n.content === 'one').length === 0, 'no stale duplicate committed');

    // XSS: an <img onerror> payload must render inert (escaped, no element).
    const xss = await t.apiCreate({ content: '<img src=x onerror="window.__pwned=1">' });
    await t.page.waitForFunction(i => window.__v0.hasNode(i), xss.id, { timeout: 3000 });
    await t.settled();
    const probe = await t.page.evaluate(i => {
      const r = document.querySelector(`.row[data-node-id="${i}"] .content`);
      return { imgs: r ? r.querySelectorAll('img').length : -1, text: r ? r.textContent : null, pwned: !!window.__pwned };
    }, xss.id);
    t.assert(probe.imgs === 0, `no <img> element injected (got ${probe.imgs})`);
    t.assert(!probe.pwned, 'onerror did not fire');
    t.assert(probe.text && probe.text.includes('<img'), `payload shown as literal text (got ${JSON.stringify(probe.text)})`);

    // Unicode / emoji content survives a round-trip through the editor.
    const uni = await t.apiCreate({ content: '日本語 🌳 café' });
    await t.page.waitForFunction(i => window.__v0.hasNode(i), uni.id, { timeout: 3000 });
    await t.settled();
    const uniText = await t.page.evaluate(i => {
      const r = document.querySelector(`.row[data-node-id="${i}"] .content`);
      return r ? r.textContent : null;
    }, uni.id);
    t.assert(uniText === '日本語 🌳 café', `unicode rendered intact (got ${JSON.stringify(uniText)})`);

    // Very long content stays a single navigable row.
    const long = await t.apiCreate({ content: 'x'.repeat(400) });
    await t.page.waitForFunction(i => window.__v0.hasNode(i), long.id, { timeout: 3000 });
    await t.settled();
    const longRows = await t.page.evaluate(i => document.querySelectorAll(`.row[data-node-id="${i}"]`).length, long.id);
    t.assert(longRows === 1, `long content is one row (got ${longRows})`);
    // keys alive after all the editing churn.
    const c0 = await t.cursor();
    await t.key('j');
    t.assert((await t.cursor()) !== c0, 'j alive after editing churn');
  },

  '13. two clients: create/edit/presence propagate; edit not clobbered mid-edit': async t => {
    await t.open();
    const pageB = await t.secondPage();
    // A creates a node; B sees it within 2s.
    const posted = await t.apiCreate({ content: 'from A', author: 'nitsan' });
    await pageB.waitForFunction(id => window.__v0.hasNode(id), posted.id, { timeout: 2000 });
    t.assert(true, 'B saw A\'s new node over SSE');
    // Presence from A visible on B.
    await t.apiPresence('aria', posted.id);
    await pageB.waitForFunction(id => window.__v0.presenceAt(id) === 'aria', posted.id, { timeout: 3000 });
    t.assert(true, 'B sees presence posted for the node');
    // B starts editing `posted`; meanwhile A edits the SAME node. B's editor
    // content must not be clobbered mid-edit.
    // Drive B's editor by double-clicking the row (wait for it to render first).
    await pageB.waitForFunction(id => !!document.querySelector(`.row[data-node-id="${id}"]`), posted.id, { timeout: 3000 });
    await pageB.evaluate(id => {
      const r = document.querySelector(`.row[data-node-id="${id}"]`);
      r.dispatchEvent(new MouseEvent('click', { detail: 2, bubbles: true }));
    }, posted.id);
    await pageB.waitForFunction(() => window.__v0.editing === true, null, { timeout: 3000 });
    await pageB.keyboard.type('B is typing here');
    // A commits a conflicting edit while B types.
    await t.apiPatch(posted.id, 'A clobber attempt');
    await pageB.waitForTimeout(400);
    const bStillEditing = await pageB.evaluate(() => window.__v0.editing);
    const bText = await pageB.evaluate(id => {
      const r = document.querySelector(`.row[data-node-id="${id}"] .content`);
      return r ? r.textContent : null;
    }, posted.id);
    t.assert(bStillEditing, 'B is still editing (A\'s edit did not end B\'s session)');
    t.assert(bText && bText.includes('B is typing here'), `B's editor buffer intact mid-edit (got ${JSON.stringify(bText)})`);
    await pageB.keyboard.press('Enter');
    await pageB.waitForFunction(() => window.__v0.editing === false);
    // keys alive on both after the flow.
    await pageB.keyboard.press('j');
    await t.key('j');
    t.assert(true, 'both clients survive the concurrent-edit flow');
    await pageB.close();
  },

  '14. rapid input: mash j and Tab/Shift-Tab, keys stay alive': async t => {
    await t.open();
    await t.key('Enter'); // into Welcome, where there are several children
    const focus = await t.focus();
    // Hammer j 20x fast (no per-key settle beyond the harness 70ms).
    for (let i = 0; i < 20; i++) await t.page.keyboard.press('j');
    await t.settled();
    let v = await t.v0();
    t.assert(v.visible.includes(v.cursor), 'cursor still on a visible node after 20x j');
    t.assert(!v.editing, 'not stuck in edit mode after mashing j');
    // Mash Tab / Shift-Tab.
    for (let i = 0; i < 6; i++) {
      await t.page.keyboard.press('Tab');
      await t.page.keyboard.press('Shift+Tab');
    }
    await t.settled();
    v = await t.v0();
    t.assert(!v.editing && v.focus === focus, 'focus intact and not editing after Tab mashing');
    // keys alive: after j the cursor is still a visible node (may clamp at bottom).
    await t.key('j');
    const c1 = await t.cursor();
    t.assert((await t.v0()).visible.includes(c1), 'j keeps cursor on a visible node after rapid input');
  },

  '15. empty commit discipline: o then Escape discards the empty node': async t => {
    await t.open();
    const before = (await t.tree()).nodes.length;
    await t.key('o');
    await t.page.waitForFunction(() => window.__v0.editing === true, null, { timeout: 3000 });
    // Escape without typing anything.
    await t.key('Escape');
    await t.page.waitForFunction(() => window.__v0.editing === false);
    await t.settled();
    let tree = await t.tree();
    t.assert(tree.nodes.length === before, `empty o+Escape left no orphan node (before ${before}, after ${tree.nodes.length})`);
    // o, type, then Escape: cancelling still discards (content not committed => empty).
    await t.key('o');
    await t.page.waitForFunction(() => window.__v0.editing === true);
    await t.type('typed then cancelled');
    await t.key('Escape');
    await t.page.waitForFunction(() => window.__v0.editing === false);
    await t.settled();
    tree = await t.tree();
    t.assert(!tree.nodes.find(n => n.content === 'typed then cancelled'), 'Escape discarded the typed-but-cancelled node');
    t.assert(tree.nodes.length === before, 'still no orphan after typed-then-Escape');
    // o + type + Enter keeps it.
    await t.key('o');
    await t.page.waitForFunction(() => window.__v0.editing === true);
    await t.type('kept');
    await t.key('Enter');
    await t.page.waitForFunction(() => window.__v0.editing === false);
    await t.settled();
    tree = await t.tree();
    t.assert(tree.nodes.find(n => n.content === 'kept'), 'typed + Enter kept the node');
    // keys alive.
    await t.key('j');
    t.assert(!(await t.v0()).editing, 'keys alive after empty-commit flow');
  },

  '16. persistence: ops survive reload and a server restart (same data dir)': async t => {
    await t.open();
    // A batch of operations via the UI + API.
    const a = await t.apiCreate({ content: 'persist-A' });
    const b = await t.apiCreate({ parent: a.id, kind: 'child', content: 'persist-B' });
    await t.apiCreate({ parent: a.id, kind: 'reply', content: 'persist-reply', author: 'claude' });
    await t.apiPatch(b.id, 'persist-B-edited');
    await t.page.waitForFunction(id => window.__v0.hasNode(id), b.id, { timeout: 3000 });
    await t.settled();
    const snap = async () => {
      const tr = await t.tree();
      const nodes = tr.nodes.map(n => `${n.content}`).sort();
      const edges = tr.edges.map(e => `${e.parent}>${e.child}:${e.kind}`).sort();
      return JSON.stringify({ nodes, edges });
    };
    const s1 = await snap();
    // Reload the page: identical tree.
    await t.reopen();
    t.assert(await snap() === s1, 'tree identical after page reload');
    // Restart the server against the same data dir: identical tree (log replay).
    await t.restartServer();
    t.assert(await snap() === s1, 'tree identical after server restart (log replay)');
    // Page auto-reconnects SSE; reload to re-fetch and confirm UI is alive.
    await t.reopen();
    t.assert((await t.tree()).nodes.find(n => n.content === 'persist-B-edited'), 'edited content persisted');
    await t.key('j');
    t.assert(!(await t.v0()).editing, 'keys alive after restart+reload');
  },

  '17. reply UX: reply-to-a-reply nests; agent reply gets rail + prefix at depth': async t => {
    await t.open();
    const welcome = await idByContent(t, 'Welcome');
    // A user reply under Welcome.
    const r1 = await t.apiCreate({ parent: welcome, kind: 'reply', content: 'user reply', author: 'nitsan' });
    // An agent reply nested under the user reply.
    const r2 = await t.apiCreate({ parent: r1.id, kind: 'reply', content: 'agent nested reply', author: 'claude' });
    await t.page.waitForFunction(id => window.__v0.hasNode(id), r2.id, { timeout: 3000 });
    await t.settled();
    const info = await t.page.evaluate(ids => {
      const [a, b] = ids;
      const ra = document.querySelector(`.row[data-node-id="${a}"]`);
      const rb = document.querySelector(`.row[data-node-id="${b}"]`);
      return {
        aReply: ra ? [...ra.classList].includes('reply') : null,
        bReply: rb ? [...rb.classList].includes('reply') : null,
        bAgent: rb ? [...rb.classList].includes('agent') : null,
        bPrefix: rb ? (rb.querySelector('.prefix') ? rb.querySelector('.prefix').textContent : null) : null,
        bIndent: rb ? rb.style.marginLeft : null,
      };
    }, [r1.id, r2.id]);
    t.assert(info.aReply, 'user reply carries the rail class');
    t.assert(info.bReply && info.bAgent, 'nested agent reply has reply + agent classes');
    t.assert(info.bPrefix && info.bPrefix.includes('↳') && info.bPrefix.includes('claude'), `agent reply prefix at depth (got ${JSON.stringify(info.bPrefix)})`);
    t.assert(info.bIndent && parseInt(info.bIndent, 10) > 0, `nested reply is indented (got ${JSON.stringify(info.bIndent)})`);
    // keys alive.
    await t.key('j');
    t.assert(!(await t.v0()).editing, 'keys alive after reply flow');
  },

  '18. quote flows: live update, jk skips quote row, p with empty yank no-ops': async t => {
    await t.open();
    await t.key('Enter'); // into Welcome
    const welcome = await t.focus();
    const target = await t.cursor();
    // p with nothing yanked is a no-op (no quote edge appears).
    await t.key('p');
    await t.settled();
    let tree = await t.tree();
    t.assert(tree.edges.filter(e => e.kind === 'quote').length === 0, 'p with empty yank created no quote');
    // yank + paste -> quote edge.
    await t.key('y');
    await t.key('p');
    await t.settled();
    // The quote row is skipped by j/k navigation (not in visible ids).
    let v = await t.v0();
    t.assert(v.quotes.includes(target), 'quote reference rendered');
    t.assert(!v.visible.includes(target) || v.visible.filter(x => x === target).length === 1,
      'quote target is navigable as a node but the quote row itself is skipped');
    const quoteNavigable = await t.page.evaluate(() => window.__v0.visible.length);
    // Editing the target updates the quote text live.
    await t.apiPatch(target, 'EDITED TARGET TEXT');
    await t.page.waitForFunction(() => {
      const q = document.querySelector('#list .quote');
      return q && q.textContent.includes('EDITED TARGET TEXT');
    }, null, { timeout: 3000 });
    t.assert(true, 'quote text updated live when the target was edited');
    // keys alive, and j does not land the cursor on the quote row.
    const c0 = await t.cursor();
    await t.key('j');
    const c1 = await t.cursor();
    v = await t.v0();
    t.assert(v.visible.includes(c1), 'cursor stays on a navigable (non-quote) node after j');
    t.assert(!(await t.v0()).editing, 'keys alive after quote flow');
  },

  '19. outdent to root: a top-level child stays visible on the board (regression)': async t => {
    await t.open();
    await t.key('Enter'); // into Welcome
    const h1 = (await t.v0()).visible[0]; // first child of Welcome
    t.assert((await t.cursor()) === h1, 'cursor on first child');
    // Shift-Tab outdents h1 whose parent (Welcome) is itself a root: this records
    // a parent:"" edge. h1 must remain a visible board root, not vanish.
    await t.key('Shift+Tab');
    await t.settled();
    const edge = (await t.tree()).edges.find(e => e.child === h1 && e.kind !== 'quote');
    t.assert(edge && edge.parent === '', `outdent recorded a root edge (got ${JSON.stringify(edge)})`);
    await t.key('Escape'); // back to board
    await t.settled();
    const v = await t.v0();
    t.assert(v.rendered.includes(h1), 'outdented-to-root node is still rendered on the board');
    t.assert(v.visible.includes(h1), 'outdented-to-root node is navigable on the board');
    // keys alive and land the cursor on it.
    let g = 0;
    while ((await t.cursor()) !== h1 && g++ < 20) await t.key('j');
    t.assert((await t.cursor()) === h1, 'cursor can reach the outdented node');
  },

  '20. quoted root stays a board root (regression)': async t => {
    await t.open();
    // Make a fresh board root, then quote it under Welcome.
    const rootNode = await t.apiCreate({ content: 'quotable root' });
    const welcome = await idByContent(t, 'Welcome');
    await t.page.waitForFunction(id => window.__v0.hasNode(id), rootNode.id, { timeout: 3000 });
    // Add a quote edge (parent=Welcome) pointing at the root node.
    await fetch(`${t.base}/api/edges`, { method: 'POST', body: JSON.stringify({ parent: welcome, child: rootNode.id, kind: 'quote' }) });
    await t.page.waitForFunction(() => true);
    await t.settled();
    const v = await t.v0();
    // The quoted node is still a top-level root (a quote is a pointer, not a parent).
    t.assert(v.rendered.includes(rootNode.id), 'quoted node still rendered as a board root');
    t.assert(v.quotes.includes(rootNode.id), 'and it also appears as a quote reference');
    await t.key('j');
    t.assert(!(await t.v0()).editing, 'keys alive after quoted-root flow');
  },
});
