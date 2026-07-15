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
    // Only the Welcome root is a board-level root, so one row renders.
    t.assert(v.rendered.length === 1, `one root row rendered (got ${v.rendered.length})`);
    const welcome = await idByContent(t, 'Welcome');
    t.assert(v.rendered[0] === welcome && v.cursor === welcome, 'cursor on the Welcome root');
    const hint = await t.page.evaluate(() => document.getElementById('hint').textContent);
    t.assert(hint.includes('move') && hint.includes('zoom'), `hint line present (got ${JSON.stringify(hint)})`);
  },

  '2. navigate: j/k move, Enter zooms (title + crumb), Esc restores': async t => {
    await t.open();
    const welcome = await idByContent(t, 'Welcome');
    t.assert((await t.page.title()) === 'outliner', 'root document.title is plain');
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
    t.assert((await t.focus()) === welcome, 'zoomed into the reply target');
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

    // Now zoom into h1 and outdent h2 back under Welcome. After the reparent
    // re-render the cursor reset to the first visible child (h1).
    const cur = await t.cursor();
    t.assert(cur === h1, `cursor rests on h1 after reparent re-render (got ${cur})`);
    await t.key('Enter'); // zoom into h1; its only child is h2
    t.assert((await t.focus()) === h1, 'zoomed into h1');
    t.assert((await t.cursor()) === h2, 'cursor on h2 inside h1');
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
});
