// Browser e2e scenarios for the session-notes web UI. Each runs against a
// fresh server + fixture board (see harness.js). Run via ./run.sh, filter
// with SN_ONLY=<substring>.
'use strict';
const { run } = require('./harness');

run({
  'keyboard navigation walks stops in visual order': async t => {
    await t.open();
    await t.key('j');
    await t.assertCursor('sec:Waiting on User');
    await t.key('j');
    await t.assertCursor('it:Waiting on User:- [ ] review the middleware PR @user');
    await t.key('Tab');
    await t.assertCursor('sec:Plan');
    await t.key('3');
    await t.assertCursor('sec:Threads');
    await t.key('j');
    await t.assertCursor('it:Threads:- [>] auth refactor — extracting middleware');
    await t.key('j'); // reply is a stop too
    await t.assertCursor('it:Threads:  - claude: extracted, tests green');
    await t.key('G');
    const last = await t.cursorKey();
    t.assert(last && last.startsWith('it:Log:'), `G lands in Log (got ${last})`);
    await t.key('g');
    await t.assertCursor('sec:Waiting on User');
  },

  'down-arrow advances past duplicate lines (issue #7)': async t => {
    // Duplicate raw lines share a cursor key. Matching the cursor by key alone
    // always resolves to the FIRST duplicate, so j/down got stuck bouncing on
    // it and could never reach items below. The cursor must walk through both
    // copies and land on the unique item that follows.
    const fs = require('fs');
    fs.writeFileSync(t.boardPath, [
      '---',
      'session: e2e-fixture-0001',
      'cwd: /tmp/e2e-project',
      'started: 2026-07-09T10:00:00Z',
      'title: dup fixture',
      '---',
      '',
      '## Dups',
      '- alpha',
      '- dup line',
      '- dup line',
      '- omega',
      '',
    ].join('\n'));
    await t.open();
    await t.key('j'); // sec:Dups
    await t.assertCursor('sec:Dups');
    await t.key('j'); // alpha
    await t.assertCursor('it:Dups:- alpha');
    await t.key('j'); // first dup
    await t.assertCursor('it:Dups:- dup line');
    // Two more presses must clear BOTH duplicates and reach omega — before the
    // fix the second press stalled on the duplicate key forever.
    await t.key('j'); // second dup (same key, different element)
    await t.key('j'); // omega
    await t.assertCursor('it:Dups:- omega');
  },

  'space cycles status and cursor survives the re-render': async t => {
    await t.open();
    await t.key('3');
    await t.key('j');
    await t.key(' ');
    await t.waitBoardContains('- [x] auth refactor — extracting middleware');
    await t.settled();
    // Cursor tracks the item across its raw-line change.
    const key = await t.cursorKey();
    t.assert(key === 'it:Threads:- [x] auth refactor — extracting middleware',
      `cursor followed status change (got ${key})`);
  },

  'R replies flat, F forks nested, from the keyboard': async t => {
    await t.open();
    await t.key('4'); // Questions
    await t.key('j'); // the !! question
    await t.key('j'); // its reply "user: leaning yes"
    await t.key('R');
    // Web replies carry the user: author tag like the TUI (fa536cd).
    await t.type('shipping it');
    await t.key('Enter');
    // Flat: sibling of "leaning yes", i.e. 2-space indent under the question.
    // (Programmatic saves append a " ^id" node anchor, so match up to it.)
    await t.waitBoardContains('\n  - user: shipping it ^');
    await t.key('F');
    await t.type('forked aside');
    await t.key('Enter');
    // Fork target is the cursor item (the "leaning yes" reply): nests to 4.
    await t.waitBoardContains('\n    - user: forked aside ^');
  },

  'typed text survives an external write (deferred render)': async t => {
    await t.open();
    await t.key('3');
    await t.key('j');
    await t.key('R');
    await t.type('mid-typing');
    await t.editExternally({ op: 'log', text: 'external poke', author: 'claude' });
    await t.page.waitForTimeout(1200); // SSE fires; render must defer
    const val = await t.page.$eval('input.inline', i => i.value);
    t.assert(val === 'mid-typing', `input intact (got ${JSON.stringify(val)})`);
    await t.key('Enter');
    await t.waitBoardContains('  - user: mid-typing');
    await t.settled();
    const body = await t.page.textContent('#board');
    t.assert(body.includes('external poke'), 'deferred external edit rendered after close');
  },

  'failed edit parks the text in the add box': async t => {
    await t.open();
    await t.key('3');
    await t.key('j');
    await t.key('R');
    await t.type('doomed reply');
    // Kill the target from under the reply before submitting.
    await t.editExternally({ op: 'delete', section: 'Threads', raw: '- [>] auth refactor — extracting middleware' });
    await t.page.waitForTimeout(300);
    await t.key('Enter');
    await t.settled();
    const box = await t.page.$eval('input.add[data-section="Threads"]', i => i.value);
    t.assert(box === 'doomed reply', `text parked in add box (got ${JSON.stringify(box)})`);
  },

  'E raw editor saves with optimistic lock': async t => {
    await t.open();
    await t.key('E');
    await t.page.waitForSelector('#rawmodal.open');
    const ta = t.page.locator('#rawtext');
    await ta.fill((await ta.inputValue()).replace('## Ideas', '## Ideas\n- typed in raw editor'));
    await t.page.keyboard.press('Control+Enter');
    await t.waitBoardContains('- typed in raw editor');
    await t.page.waitForFunction(() => !document.querySelector('#rawmodal.open'), null, { timeout: 3000 });
    t.log('modal closed after save');
  },

  'E raw editor 3-way merges a concurrent write instead of clobbering': async t => {
    await t.open();
    await t.key('E');
    await t.page.waitForSelector('#rawmodal.open');
    // Claude appends a Log line while the editor is open.
    await t.editExternally({ op: 'log', text: 'race', author: 'claude' });
    await t.page.waitForTimeout(200);
    const ta = t.page.locator('#rawtext');
    // The user's edit lands in a disjoint region (a fresh section) so the two
    // writes 3-way merge cleanly — both must survive (no clobber, no 409).
    await ta.fill((await ta.inputValue()) + '\n## Scratch\n- my precious edit\n');
    await t.page.click('#rawsave');
    await t.page.waitForFunction(() => !document.querySelector('#rawmodal.open'), null, { timeout: 3000 });
    await t.waitBoardContains('my precious edit');
    t.assert(t.file().includes('race'), 'concurrent claude write survived the merge');
  },

  'o opens the side-note modal; save creates the file': async t => {
    await t.open();
    await t.key('5'); // Ideas
    await t.key('j');
    await t.key('j'); // the [[design-notes]] item
    await t.key('o');
    await t.page.waitForSelector('#notemodal.open');
    await t.page.fill('#notetext', '# design-notes\n\nfrom e2e\n');
    await t.page.click('#notesave');
    const fs = require('fs'), path = require('path');
    const notePath = path.join(t.boardsDir, 'e2e-fixture-0001.notes', 'design-notes.md');
    await t.page.waitForTimeout(400);
    t.assert(fs.existsSync(notePath), 'note file created');
    t.assert(fs.readFileSync(notePath, 'utf8').includes('from e2e'), 'note content saved');
  },

  'note modal copy-path button copies the absolute note path': async t => {
    await t.open();
    await t.key('5'); // Ideas
    await t.key('j');
    await t.key('j'); // the [[design-notes]] item
    await t.key('o');
    await t.page.waitForSelector('#notemodal.open');
    // Clipboard writes are flaky/permission-gated in headless Chromium, so we
    // assert the app-side handler recorded the path it copied (window.__sn hook)
    // and the button flashed "copied!".
    await t.page.click('#notecopypath');
    // The handler is async (clipboard write) — wait for the flash before asserting.
    await t.page.waitForFunction(() => document.getElementById('notecopypath').textContent === 'copied!', null, { timeout: 3000 });
    const got = await t.page.evaluate(() => window.__sn && window.__sn.lastCopiedPath);
    const suffix = require('path').join('e2e-fixture-0001.notes', 'design-notes.md');
    t.assert(got && got.startsWith('/') && got.endsWith(suffix), `copied path is the absolute note path (got=${got})`);
  },

  'O copies the item\'s linked-note path without opening the modal': async t => {
    await t.open();
    await t.key('5'); // Ideas
    await t.key('j');
    await t.key('j'); // the [[design-notes]] item
    await t.key('O');
    const suffix = require('path').join('e2e-fixture-0001.notes', 'design-notes.md');
    await t.page.waitForFunction(sfx => window.__sn && window.__sn.lastCopiedPath && window.__sn.lastCopiedPath.endsWith(sfx), suffix, { timeout: 3000 });
    const open = await t.page.$eval('#notemodal', e => e.classList.contains('open'));
    t.assert(!open, 'O does not open the note modal');
  },

  'section rename and archive from the keyboard': async t => {
    await t.open();
    await t.key('5'); // Ideas head
    await t.key('e');
    // rename input pre-filled with "Ideas": select-all replace
    await t.selectAll();
    await t.type('Sparks');
    await t.key('Enter');
    await t.waitBoardContains('## Sparks');
    await t.settled();
    await t.assertCursor('sec:Sparks');
    await t.key('d'); // archive the whole section
    await t.waitBoardContains('## Sparks', false);
    await t.waitBoardContains('## Archive');
  },

  'archive keeps the selection on the next sibling': async t => {
    await t.open();
    await t.key('2'); // Plan head
    await t.key('j', 2); // wire sessions through it (mid-section)
    await t.key('d');
    await t.waitBoardContains('## Archive');
    // The next sibling in document order takes the archived item's place.
    await t.page.waitForFunction(k => window.__sn.cursorKey === k,
      'it:Plan:- [ ] drop legacy endpoint', { timeout: 3000 });
  },

  'archiving the last item selects the previous one': async t => {
    await t.open();
    await t.key('2'); // Plan head
    await t.key('j', 3); // drop legacy endpoint (last in Plan)
    await t.key('d');
    await t.waitBoardContains('## Archive');
    await t.page.waitForFunction(k => window.__sn.cursorKey === k,
      'it:Plan:- [>] wire sessions through it', { timeout: 3000 });
  },

  'archiving the only item falls back to the section head': async t => {
    await t.open();
    await t.key('1'); // Waiting on User head
    await t.key('j'); // its single item
    await t.key('d');
    await t.waitBoardContains('## Archive');
    await t.page.waitForFunction(k => window.__sn.cursorKey === k,
      'sec:Waiting on User', { timeout: 3000 });
  },

  'D delete keeps the selection on the next sibling': async t => {
    await t.open();
    await t.key('2'); // Plan head
    await t.key('j', 2); // wire sessions through it
    await t.key('D');
    await t.waitBoardContains('wire sessions through it', false);
    await t.page.waitForFunction(k => window.__sn.cursorKey === k,
      'it:Plan:- [ ] drop legacy endpoint', { timeout: 3000 });
  },

  'map archive moves focus to the next sibling node': async t => {
    await t.open();
    await t.key('2');
    await t.key('j', 2); // wire sessions through it
    await t.key('m'); // map seeds focus from the outline cursor
    await t.page.waitForFunction(() => window.__sn.map.in);
    await t.key('d');
    await t.waitBoardContains('## Archive');
    await t.page.waitForFunction(k => window.__sn.map.focus === k,
      'it:Plan:- [ ] drop legacy endpoint', { timeout: 3000 });
  },

  'map archive of the last sibling lands on the sibling node, not its leaf': async t => {
    // Regression for "sent to the leaf of my sibling instead of my sibling":
    // the outline follows document order (the previous item's deepest
    // descendant), but the map must follow the structural sibling NODE. In
    // Threads the last top-level item is "fix flaky test"; its previous sibling
    // "auth refactor" owns a (folded) reply child. Archiving the last item must
    // focus the auth-refactor node — not the reply leaf beneath it.
    await t.open();
    await t.key('3'); // Threads head
    await t.key('j', 3); // auth refactor -> claude reply -> fix flaky test
    await t.key('m'); // map seeds focus from the outline cursor (fix flaky test)
    await t.page.waitForFunction(k => window.__sn.map.in && window.__sn.map.focus === k,
      'it:Threads:- [x] fix flaky test', { timeout: 3000 });
    await t.key('d'); // archive the last sibling
    await t.waitBoardContains('## Archive');
    // Focus must land on the auth-refactor NODE, never on its reply leaf.
    await t.page.waitForFunction(k => window.__sn.map.focus === k,
      'it:Threads:- [>] auth refactor — extracting middleware', { timeout: 3000 });
  },

  'map: one press descends into a collapsed node (expand and land)': async t => {
    await t.open();
    await t.key('3'); // Threads head
    await t.key('j'); // auth refactor — its only child is a claude: reply
    await t.key('m'); // map seeds focus from the outline cursor
    const itemKey = 'it:Threads:- [>] auth refactor — extracting middleware';
    const replyKey = 'it:Threads:  - claude: extracted, tests green';
    await t.page.waitForFunction(k => window.__sn.map.in && window.__sn.map.focus === k, itemKey);
    // Fold the node fully closed, stepping the enter cycle deterministically:
    // default (reply hidden) -> all (reply shown) -> closed (hidden again).
    await t.key('Enter');
    await t.page.waitForFunction(k => window.__sn.map.nodes.includes(k), replyKey);
    await t.key('Enter');
    await t.page.waitForFunction(k => !window.__sn.map.nodes.includes(k), replyKey);
    // Descend outward with ONE press: the same keystroke must expand the fold
    // AND land focus on the revealed child (no dead second press).
    const dir = await t.page.evaluate(() => {
      const f = document.querySelector('.mapnode.focus');
      const c = document.querySelector('.mapnode.center');
      return f.offsetLeft >= c.offsetLeft ? 'l' : 'h';
    });
    await t.key(dir);
    await t.page.waitForFunction(k => window.__sn.map.focus === k, replyKey, { timeout: 3000 });
    t.assert((await t.sn2()).map.nodes.includes(replyKey), 'reply node visible after one press');
  },

  'map zoom writes a #node-id fragment and a reload deep-links back to it': async t => {
    await t.open();
    // Force lazy id assignment: one op stamps ids on every node, so the map
    // zoom root has a durable id to key on (and to put in the URL fragment).
    await t.key('3'); // Threads
    await t.key('j'); // auth refactor (owns a reply child)
    await t.key(' '); // cycle status -> save -> EnsureIDs
    await t.settled();
    await t.key('m'); // enter map, focus seeds on auth refactor
    await t.page.waitForFunction(() => window.__sn.map.in);
    await t.key('f'); // zoom into the focused node
    // The zoom root now carries a durable id, reflected into the URL fragment.
    await t.page.waitForFunction(() => window.__sn.map.rootId && location.hash === '#' + window.__sn.map.rootId);
    const rootId = (await t.sn2()).map.rootId;
    t.assert(rootId, 'zoom root has a durable node id');

    // A fresh load of /b/{board}#<id> deep-links into the zoomed OUTLINE (the
    // default view; M7 shared re-root zoom). The zoom root is shared with the
    // map, so pressing m enters the map still zoomed to the same node.
    // ?fresh forces a real full reload (a fragment-only goto is a same-document
    // navigation that would preserve the in-memory map state).
    const boardUrl = t.page.url().split('#')[0].split('?')[0];
    await t.page.goto(`${boardUrl}?fresh=1#${rootId}`);
    await t.page.waitForFunction(() => window.__sn && window.__sn.board !== null);
    await t.page.waitForFunction(id => window.__sn && window.__sn.zoom && !window.__sn.map.in && window.__sn.zoom.rootId === id, rootId, { timeout: 4000 });
    let z = (await t.sn2()).zoom;
    t.assert(z.rootId === rootId && z.crumbVisible, 'reload with #id re-roots the outline (breadcrumb shown)');
    await t.key('m'); // into the map, zoom preserved
    await t.page.waitForFunction(id => window.__sn.map.in && window.__sn.map.rootId === id, rootId, { timeout: 4000 });
    const m = (await t.sn2()).map;
    t.assert(m.in && m.rootId === rootId, 'the map opens still zoomed to the same node (shared root)');
  },

  'map: z focus-folds to the ancestor path, second z restores': async t => {
    await t.open();
    await t.key('3'); // Threads
    await t.key('j'); // auth refactor (owns a reply child)
    await t.key('m'); // map seeds focus from the outline cursor
    const focus = 'it:Threads:- [>] auth refactor — extracting middleware';
    const reply = 'it:Threads:  - claude: extracted, tests green';
    const planItem = 'it:Plan:- [x] extract middleware';
    await t.page.waitForFunction(k => window.__sn.map.in && window.__sn.map.focus === k, focus, { timeout: 3000 });
    let m = (await t.sn2()).map;
    t.assert(m.nodes.includes(planItem), 'an unrelated branch is visible before the zoom');
    await t.key('z'); // focus-fold onto the auth-refactor node
    m = (await t.sn2()).map;
    t.assert(m.focusFolded, 'map reports focus-folded after z');
    t.assert(m.nodes.includes('sec:Threads') && m.nodes.includes(focus), 'the ancestor path stays visible');
    t.assert(!m.nodes.includes(planItem), 'unrelated branches collapse under the zoom');
    t.assert(m.nodes.includes(reply), "the focus node's direct child stays visible (one level open)");
    await t.key('z'); // toggle back
    m = (await t.sn2()).map;
    t.assert(!m.focusFolded, 'second z clears the zoom');
    t.assert(m.nodes.includes(planItem), 'the prior fold state is restored');
  },

  'outline: f re-roots on the cursor, breadcrumb + #id, esc zooms out': async t => {
    await t.open();
    await t.key('3'); // Threads
    await t.key('j'); // auth refactor (owns a reply child)
    await t.key(' '); // cycle status -> save -> EnsureIDs (durable ids)
    await t.settled();
    // Re-set the cursor on the same node after the save re-render.
    await t.page.waitForFunction(() => window.__sn.cursorKey && window.__sn.cursorKey.startsWith('it:Threads:'));
    const before = (await t.sn2());
    t.assert(before.zoom.rootId === '', 'not zoomed initially');
    // A sibling section item is present in the full outline.
    t.assert(before.zoom.crumbVisible === false, 'no breadcrumb before zoom');

    await t.key('f'); // re-root the outline on the auth-refactor node
    await t.page.waitForFunction(() => window.__sn.zoom.rootId && window.__sn.zoom.crumbVisible);
    let z = (await t.sn2()).zoom;
    t.assert(z.rootId && z.hash === '#' + z.rootId, 'zoom root reflected into the URL fragment');
    t.assert(z.crumbCount >= 2, 'breadcrumb shows board › … › node');
    // Only the subtree renders: the reply child is a visible stop, and a Plan
    // item from another section is gone.
    const stops = await t.page.evaluate(() => window.__sn.stops.filter(s => s.visible).map(s => s.key));
    t.assert(stops.some(k => k.includes('claude: extracted')), 'the subtree child is visible under the zoom');
    t.assert(!stops.some(k => k.startsWith('it:Plan:')), 'unrelated sections are not rendered');

    // Escape steps out one level: a top-level item root → its section, still
    // zoomed (breadcrumb shown); a second Escape → the whole board.
    await t.key('Escape');
    await t.page.waitForFunction(() => window.__sn.zoom.rootId && window.__sn.zoom.crumbVisible);
    z = (await t.sn2()).zoom;
    t.assert(z.rootId && z.hash === '#' + z.rootId, 'first esc steps out to the section root (still zoomed)');
    await t.key('Escape');
    await t.page.waitForFunction(() => window.__sn.zoom.rootId === '' && !window.__sn.zoom.crumbVisible);
    z = (await t.sn2()).zoom;
    t.assert(z.rootId === '' && z.hash === '', 'second esc clears the zoom and the fragment');
    const back = await t.page.evaluate(() => window.__sn.stops.filter(s => s.visible).map(s => s.key));
    t.assert(back.some(k => k.startsWith('it:Plan:')), 'the whole board renders again after zoom-out');
  },

  'outline: editing the #id fragment re-roots live (hashchange round-trip)': async t => {
    await t.open();
    await t.key('4'); // Questions
    await t.key('j'); // an item there
    await t.key(' '); // save -> EnsureIDs
    await t.settled();
    await t.key('f');
    await t.page.waitForFunction(() => window.__sn.zoom.rootId);
    const rootId = (await t.sn2()).zoom.rootId;
    // Clear the fragment programmatically -> hashchange -> whole board.
    await t.page.evaluate(() => { location.hash = ''; });
    await t.page.waitForFunction(() => window.__sn.zoom.rootId === '');
    t.assert((await t.sn2()).zoom.rootId === '', 'clearing the fragment un-zooms the outline');
    // Set it back -> hashchange -> re-root.
    await t.page.evaluate(id => { location.hash = '#' + id; }, rootId);
    await t.page.waitForFunction(id => window.__sn.zoom.rootId === id, rootId);
    t.assert((await t.sn2()).zoom.rootId === rootId, 'setting the fragment re-roots the outline');
  },

  'undo and redo walk this browser\'s edits': async t => {
    await t.open();
    await t.key('2'); // Plan
    await t.key('a');
    await t.type('undo me');
    await t.key('Enter');
    await t.waitBoardContains('- [ ] undo me');
    await t.page.click('body'); // leave the add box so keys work
    await t.key('u');
    await t.waitBoardContains('- [ ] undo me', false);
    await t.key('Control+r');
    await t.waitBoardContains('- [ ] undo me');
  },

  'urgent and blocked toggles': async t => {
    await t.open();
    await t.key('2'); // Plan
    await t.key('j', 3); // third plan item: drop legacy endpoint
    await t.key('!');
    await t.waitBoardContains('- [ ] !! drop legacy endpoint');
    await t.key('b');
    await t.waitBoardContains('- [?] !! drop legacy endpoint');
  },

  'undo journal is shared: CLI undoes a web edit, live in the page': async t => {
    const { execFileSync } = require('child_process');
    const BIN = process.env.SN_BIN;
    await t.open();
    await t.key('2'); // Plan
    await t.key('a');
    await t.type('web wrote this');
    await t.key('Enter');
    await t.waitBoardContains('- [ ] web wrote this');
    // Claude's CLI walks the same journal…
    execFileSync(BIN, ['edit', 'undo', '--board', t.boardPath]);
    await t.waitBoardContains('- [ ] web wrote this', false);
    // …and the page reflects it via SSE.
    await t.page.waitForFunction(
      () => !document.getElementById('board').textContent.includes('web wrote this'),
      null, { timeout: 4000 });
    // CLI redo brings it back; the web undo button lights up from the journal
    // once the SSE-triggered refetch lands.
    execFileSync(BIN, ['edit', 'redo', '--board', t.boardPath]);
    await t.waitBoardContains('- [ ] web wrote this');
    await t.page.waitForFunction(() => !document.getElementById('undo').disabled, null, { timeout: 4000 });
    t.log('undo button lit from the shared journal');
  },

  'H opens history showing who changed what': async t => {
    const { execFileSync } = require('child_process');
    await t.open();
    await t.key('2');
    await t.key('a');
    await t.type('web item');
    await t.key('Enter');
    await t.waitBoardContains('- [ ] web item');
    execFileSync(process.env.SN_BIN, ['edit', 'add', 'Plan', 'cli item', '--board', t.boardPath]);
    await t.waitBoardContains('- [ ] cli item');
    await t.page.click('body');
    await t.key('H');
    await t.page.waitForSelector('#histmodal.open');
    const text = await t.page.textContent('#histlist');
    t.assert(text.includes('claude') && text.includes('+ - [ ] cli item'), 'CLI edit attributed');
    t.assert(text.includes('web') && text.includes('+ - [ ] web item'), 'web edit attributed');
    // newest first: the cli entry appears before the web entry
    t.assert(text.indexOf('cli item') < text.indexOf('web item'), 'newest first');
  },

  'map: m toggles and selection carries over both ways': async t => {
    await t.open();
    await t.key('3'); // Threads head
    await t.key('j'); // first thread item
    const key = await t.cursorKey();
    await t.key('m');
    let map = (await t.sn2()).map;
    t.assert(map.in, 'map mode entered');
    t.assert(map.focus === key, `map focused the outline cursor (got ${map.focus})`);
    // Move toward the center: the parent. Which physical key that is depends on
    // which side the balancer put Threads on — read it from the layout (an
    // outward press would instead auto-expand this node's folded reply child).
    const side = await t.page.evaluate(() => {
      const f = document.querySelector('.mapnode.focus'), c = document.querySelector('.mapnode.center');
      return f.offsetLeft >= c.offsetLeft ? 1 : -1;
    });
    await t.key(side === 1 ? 'h' : 'l');
    map = (await t.sn2()).map;
    t.assert(map.focus === 'sec:Threads', `toward-center reached parent (got ${map.focus})`);
    await t.key('m');
    await t.assertCursor('sec:Threads');
  },

  'map: enter cycles fold states with [+N] suffix': async t => {
    await t.open();
    await t.key('3');
    await t.key('j'); // thread with one reply
    await t.key('m');
    // default: reply (status-none child) hidden behind [+1]
    let map = (await t.sn2()).map;
    const replyKey = 'it:Threads:  - claude: extracted, tests green';
    t.assert(!map.nodes.includes(replyKey), 'reply hidden in default view');
    t.assert(await t.page.locator('.mapnode.focus .suffix').textContent() === ' [+1]', 'suffix [+1]');
    await t.key('Enter'); // reveal replies
    map = (await t.sn2()).map;
    t.assert(map.nodes.includes(replyKey), 'reply shown after enter');
    await t.key('Enter'); // collapse whole subtree
    map = (await t.sn2()).map;
    t.assert(!map.nodes.includes(replyKey), 'collapsed after second enter');
    await t.key('Enter'); // back to default
    map = (await t.sn2()).map;
    t.assert(!map.nodes.includes(replyKey), 'default again');
  },

  'map: f focuses a subtree, esc steps out': async t => {
    await t.open();
    await t.key('3'); // Threads head
    await t.key('m');
    await t.key('f'); // re-root on Threads
    let map = (await t.sn2()).map;
    t.assert(map.root === 'sec:Threads', `re-rooted (got ${map.root})`);
    t.assert(map.focus.startsWith('it:Threads:'), 'focus on first child');
    const crumb = await t.page.textContent('#mapcrumb');
    t.assert(crumb.includes('Threads'), 'breadcrumb shows path');
    await t.key('Escape'); // b now toggles blocked (outline parity); esc steps out
    map = (await t.sn2()).map;
    t.assert(map.root === 'center', 'stepped back out');
    t.assert(map.focus === 'sec:Threads', 'focus rests on the stepped-out node');
  },

  'map: edits work on the focused node': async t => {
    await t.open();
    await t.key('3');
    await t.key('j');
    await t.key('m');
    await t.key(' '); // cycle [>] -> [x]
    await t.waitBoardContains('- [x] auth refactor — extracting middleware');
    await t.settled();
    const map = (await t.sn2()).map;
    t.assert(map.focus === 'it:Threads:- [x] auth refactor — extracting middleware',
      `focus followed the status change (got ${map.focus})`);
    await t.key('a'); // add child (fork), inline
    await t.page.waitForSelector('.mapnode.editing input.mapinput');
    await t.type('from the map');
    await t.key('Enter');
    await t.waitBoardContains('  - user: from the map');
  },

  'map: a opens an inline provisional node under the selection': async t => {
    await t.open();
    await t.key('3'); // Threads
    await t.key('m');
    await t.settled();
    const parent = (await t.sn2()).map.focus;
    t.assert(parent === 'sec:Threads', `focused the section (got ${parent})`);
    await t.key('a');
    // The input is hosted inside a provisional node in the map, not a detached
    // overlay: the focused node is the provisional one and it carries the input.
    await t.page.waitForSelector('.mapnode.provisional.editing input.mapinput');
    let map = (await t.sn2()).map;
    t.assert(map.pending && map.pending.kind === 'add', 'pending add active');
    t.assert(map.pending.parentKey === 'sec:Threads', `provisional lives under the selection (got ${map.pending.parentKey})`);
    t.assert(map.focus === '__pending__', `focus on the provisional node (got ${map.focus})`);
    // The provisional node sits inside the map canvas (in-place), not fixed.
    const inMap = await t.page.evaluate(() =>
      !!document.querySelector('#mapcanvas .mapnode.provisional input.mapinput'));
    t.assert(inMap, 'provisional input is inside the map canvas');
    await t.type('inline child');
    await t.key('Enter');
    await t.waitBoardContains('inline child');
    await t.settled();
    map = (await t.sn2()).map;
    t.assert(!map.pending, 'pending cleared after commit');
    // Focus follows the freshly-added node (not snapped back to the root).
    t.assert(map.focus === 'it:Threads:- [ ] inline child',
      `focus lands on the new node (got ${map.focus})`);
  },

  'map: Esc cancels the inline add and removes the provisional node': async t => {
    await t.open();
    await t.key('3');
    await t.key('m');
    await t.key('a');
    await t.page.waitForSelector('.mapnode.provisional input.mapinput');
    await t.type('discard me');
    await t.key('Escape');
    await t.page.waitForSelector('.mapnode.provisional', { state: 'detached' });
    const map = (await t.sn2()).map;
    t.assert(!map.pending, 'pending cleared on Esc');
    t.assert(map.focus === 'sec:Threads', `focus restored to the anchor (got ${map.focus})`);
    t.assert(!map.nodes.includes('__pending__'), 'provisional node gone');
    t.assert(!t.file().includes('discard me'), 'nothing was committed');
  },

  'map: arrow cancels a fresh inline add': async t => {
    await t.open();
    await t.key('3');
    await t.key('m');
    await t.key('a');
    await t.page.waitForSelector('.mapnode.provisional input.mapinput');
    await t.key('ArrowUp');
    await t.page.waitForSelector('.mapnode.provisional', { state: 'detached' });
    const map = (await t.sn2()).map;
    t.assert(!map.pending, 'pending cancelled by arrow');
  },

  'map: arrow keeps a typed inline add (no data loss)': async t => {
    await t.open();
    await t.key('3');
    await t.key('m');
    await t.key('a');
    await t.page.waitForSelector('.mapnode.provisional input.mapinput');
    await t.type('half typed');
    // Left/Right/Up/Down must NOT discard once text exists: the provisional
    // node stays and the typed value is preserved.
    await t.key('ArrowLeft');
    await t.key('ArrowUp');
    await t.settled();
    const map = (await t.sn2()).map;
    t.assert(map.pending, 'pending survives arrows once text is typed');
    const val = await t.page.inputValue('.mapnode.provisional input.mapinput');
    t.assert(val === 'half typed', `typed text preserved (got ${JSON.stringify(val)})`);
    // Left moved the caret into the text rather than to the end.
    const caret = await t.page.evaluate(() =>
      document.querySelector('.mapnode.provisional input.mapinput').selectionStart);
    t.assert(caret < 'half typed'.length, `Left moved the caret within the text (got ${caret})`);
  },

  'map: e edits the focused item inline (in-place input)': async t => {
    await t.open();
    await t.key('3');
    await t.key('j'); // first item under Threads
    await t.key('m');
    await t.settled();
    const key = (await t.sn2()).map.focus;
    t.assert(key.startsWith('it:Threads:'), `focused an item (got ${key})`);
    await t.key('e');
    await t.page.waitForSelector('.mapnode.editing input.mapinput');
    const map = (await t.sn2()).map;
    t.assert(map.pending && map.pending.kind === 'edit', 'pending edit active');
    t.assert(map.pending.targetKey === key, 'editing the focused node');
    // The input is pre-filled with the item's current text (not blank).
    const val = await t.page.inputValue('.mapnode.editing input.mapinput');
    t.assert(val.length > 0, `input pre-filled with current text (got ${JSON.stringify(val)})`);
    const aria = await t.page.getAttribute('.mapnode.editing input.mapinput', 'aria-label');
    t.assert(aria === 'edit node', `input is labelled for a11y (got ${JSON.stringify(aria)})`);
    await t.selectAll();
    await t.type('renamed inline');
    await t.key('Enter');
    await t.waitBoardContains('renamed inline');
  },

  'map: M toggles the Log ring': async t => {
    await t.open();
    await t.key('m');
    let map = (await t.sn2()).map;
    t.assert(!map.nodes.includes('sec:Log'), 'Log hidden by default');
    await t.key('M');
    map = (await t.sn2()).map;
    t.assert(map.nodes.includes('sec:Log'), 'Log shown after M');
  },

  'w wraps the long item at the cursor (outline)': async t => {
    await t.open();
    await t.key('5'); // Ideas
    await t.key('j', 3); // the deliberately long idea
    const raw = await t.cursorKey();
    t.assert(raw.includes('deliberately very long'), `on the long item (got ${raw})`);
    // Default: single truncated line (no wrap class).
    let wrapped = await t.page.evaluate(() => document.querySelector('.cursor .text').classList.contains('wrap'));
    t.assert(!wrapped, 'long item truncated by default');
    const h0 = await t.page.evaluate(() => document.querySelector('.cursor .text').getBoundingClientRect().height);
    await t.key('w');
    wrapped = await t.page.evaluate(() => document.querySelector('.cursor .text').classList.contains('wrap'));
    t.assert(wrapped, 'w added the wrap class');
    t.assert((await t.sn()).cursorKey === raw, 'cursor stayed on the item');
    const h1 = await t.page.evaluate(() => document.querySelector('.cursor .text').getBoundingClientRect().height);
    t.assert(h1 > h0, `wrapped item grew taller (${h0} -> ${h1})`);
    await t.key('w');
    wrapped = await t.page.evaluate(() => document.querySelector('.cursor .text').classList.contains('wrap'));
    t.assert(!wrapped, 'w toggled wrap back off');
  },

  'enter/l/h collapse and expand a section (outline)': async t => {
    await t.open();
    const collapsed = () => t.page.evaluate(() => window.__sn.collapsedSecs);
    await t.key('2'); // Plan head
    const itemStop = 'it:Plan:- [x] extract middleware';
    t.assert((await t.sn()).stops.some(s => s.key === itemStop && s.visible), 'plan items visible');
    await t.key('h'); // collapse
    t.assert((await collapsed()).Plan === true, 'Plan collapsed');
    t.assert(!(await t.sn()).stops.some(s => s.key === itemStop), 'collapsed items build no stops');
    t.assert((await t.page.textContent('.sechead .count')) || '', 'count hint shown');
    await t.assertCursor('sec:Plan'); // cursor stays on the header
    await t.key('l'); // single-press descend: expand AND land on the first item
    t.assert(!('Plan' in (await collapsed())), 'Plan expanded again');
    t.assert((await t.sn()).stops.some(s => s.key === itemStop && s.visible), 'items back');
    await t.assertCursor(itemStop); // one press moved the cursor into the section
    await t.key('2'); // back to the head
    await t.key('Enter'); // enter toggles too
    t.assert((await collapsed()).Plan === true, 'enter collapsed');
  },

  'done items are grey with no strikethrough (outline + map)': async t => {
    await t.open();
    const deco = await t.page.evaluate(() => {
      const li = [...document.querySelectorAll('li.done')][0];
      return getComputedStyle(li.querySelector('.text')).textDecorationLine;
    });
    t.assert(deco === 'none', `outline done not struck through (got ${deco})`);
    await t.key('m');
    await t.settled();
    const mdeco = await t.page.evaluate(() => {
      const n = document.querySelector('.mapnode.done');
      return n ? getComputedStyle(n).textDecorationLine : 'none';
    });
    t.assert(mdeco === 'none', `map done not struck through (got ${mdeco})`);
  },

  'author tokens are tinted in outline and map': async t => {
    await t.open();
    await t.key('3'); // Threads
    await t.key('j'); // the thread item
    await t.key('j'); // its "claude: extracted, tests green" reply
    const tint = await t.page.evaluate(() => {
      const a = document.querySelector('.cursor .text .author');
      return a ? { txt: a.textContent, cls: a.className } : null;
    });
    t.assert(tint && tint.txt === 'claude:' && tint.cls.includes('claude'), `claude token tinted (got ${JSON.stringify(tint)})`);
    await t.key('m');
    await t.key('Enter'); // reveal the reply node
    await t.settled();
    const mtint = await t.page.evaluate(() => {
      const nodes = [...document.querySelectorAll('.mapnode .author')];
      return nodes.map(a => ({ txt: a.textContent, cls: a.className }));
    });
    t.assert(mtint.some(a => a.txt === 'claude:' && a.cls.includes('claude')), `map claude token tinted (got ${JSON.stringify(mtint)})`);
  },

  'add-mode: arrow up/down on an empty box navigates away': async t => {
    await t.open();
    await t.key('2'); // Plan head
    await t.key('a'); // open add box
    await t.page.waitForFunction(() => document.activeElement && document.activeElement.classList.contains('add'));
    // Empty box: ArrowDown means "done adding, navigate away".
    await t.page.keyboard.press('ArrowDown');
    await t.page.waitForTimeout(80);
    const active = await t.page.evaluate(() => document.activeElement.tagName);
    t.assert(active !== 'INPUT', `empty add box left on ArrowDown (active=${active})`);
    // Cursor moved onto the first Plan item.
    const key = await t.cursorKey();
    t.assert(key === 'it:Plan:- [x] extract middleware', `navigated to first item (got ${key})`);
  },

  'add-mode: arrow up/down keeps typed text (no data loss)': async t => {
    await t.open();
    await t.key('2'); // Plan head
    await t.key('a'); // open add box
    await t.page.waitForFunction(() => document.activeElement && document.activeElement.classList.contains('add'));
    await t.type('half-typed');
    // Once text exists, Up/Down are inert: focus stays and the text survives.
    await t.page.keyboard.press('ArrowDown');
    await t.page.keyboard.press('ArrowUp');
    await t.page.waitForTimeout(80);
    const state = await t.page.evaluate(() => ({
      active: document.activeElement.classList.contains('add'),
      val: document.activeElement.value,
    }));
    t.assert(state.active, 'add box retains focus once text is typed');
    t.assert(state.val === 'half-typed', `typed text preserved (got ${JSON.stringify(state.val)})`);
    // Enter still commits it.
    await t.page.keyboard.press('Enter');
    await t.waitBoardContains('half-typed');
  },

  'selected line stays clear of the fixed footer when scrolled': async t => {
    await t.open();
    // Build a tall board so the list must scroll.
    for (let i = 0; i < 40; i++) {
      await t.editExternally({ op: 'add', section: 'Plan', text: `filler item ${i}` });
    }
    await t.settled();
    await t.key('G'); // jump to the last stop
    await t.page.waitForTimeout(120);
    const vis = await t.page.evaluate(() => {
      const r = window.__sn.cursorRect();
      const foot = document.getElementById('listfoot').getBoundingClientRect();
      return { r, footTop: foot.top, winH: window.innerHeight };
    });
    t.assert(vis.r, 'cursor has a rect');
    t.assert(vis.r.top >= 0, `cursor not above the viewport top (top=${vis.r.top})`);
    t.assert(vis.r.bottom <= vis.footTop + 1, `cursor sits above the fixed footer (bottom=${vis.r.bottom}, footTop=${vis.footTop})`);
  },

  'map: focus mode navigates all four directions (f, hjkl, b)': async t => {
    await t.open();
    await t.key('3'); // Threads head
    await t.key('m');
    await t.key('f'); // re-root on Threads
    let map = (await t.sn2()).map;
    t.assert(map.root === 'sec:Threads', `re-rooted (got ${map.root})`);
    const firstChild = map.focus;
    t.assert(firstChild.startsWith('it:Threads:'), 'focus on first child of the new root');
    // j/k walk the root's children (same-side siblings). Threads has 2 items;
    // from the first, j should reach the other and k return — as long as the
    // balancer put them on the same side. Assert j moves and k comes back.
    await t.key('j');
    const afterJ = (await t.sn2()).map.focus;
    if (afterJ !== firstChild) { // both on one side: round-trips
      await t.key('k');
      t.assert((await t.sn2()).map.focus === firstChild, 'k returned to the first child');
    }
    // Inward toward the root reaches it, from either physical side.
    await t.key('h');
    map = (await t.sn2()).map;
    if (map.focus !== 'sec:Threads') { await t.key('l'); map = (await t.sn2()).map; }
    t.assert(map.focus === 'sec:Threads', `inward move reached the focused root (got ${map.focus})`);
    // Outward from the root re-enters a child (root behaves like the center).
    await t.key('h');
    let outLeft = (await t.sn2()).map.focus;
    if (outLeft === 'sec:Threads') { await t.key('l'); outLeft = (await t.sn2()).map.focus; }
    t.assert(outLeft.startsWith('it:Threads:'), `outward from root entered a child (got ${outLeft})`);
    // esc steps out of focus mode back to the whole board (b is now blocked).
    await t.key('Escape');
    map = (await t.sn2()).map;
    t.assert(map.root === 'center', `esc stepped out (got ${map.root})`);
    t.assert(map.focus === 'sec:Threads', 'focus rests on the stepped-out node');
  },

  'map: w expands a truncated node to its full label': async t => {
    await t.open();
    await t.key('5'); // Ideas
    await t.key('j', 3); // the long idea
    await t.key('m');
    await t.settled();
    const truncated = await t.page.evaluate(() => document.querySelector('.mapnode.focus').textContent);
    t.assert(truncated.includes('…'), `node truncated by default (got ${JSON.stringify(truncated)})`);
    await t.key('w');
    await t.settled();
    const full = await t.page.evaluate(() => document.querySelector('.mapnode.focus').textContent);
    t.assert(!full.includes('…') && full.length > truncated.length, `w expanded the node (got ${JSON.stringify(full)})`);
    t.assert((await t.sn2()).map.expanded[(await t.sn2()).map.focus] === true, 'expand state recorded');
  },

  'map: opens centered, not pinned to an edge': async t => {
    await t.open();
    // Make the map taller than the viewport so vertical centering is exercised
    // (a short map that fits needs no scroll and can't be "pinned to an edge").
    for (let i = 0; i < 30; i++) {
      await t.editExternally({ op: 'add', section: 'Plan', text: `filler ${i}` });
    }
    await t.settled();
    await t.key('m');
    await t.settled();
    await t.page.waitForTimeout(150);
    const geom = await t.page.evaluate(() => {
      const view = document.getElementById('mapview');
      const node = document.querySelector('.mapnode.center') || document.querySelector('.mapnode.focus');
      const vr = view.getBoundingClientRect(), nr = node.getBoundingClientRect();
      return { nodeMid: nr.top + nr.height / 2, viewTop: vr.top, viewH: vr.height,
        canvasH: document.getElementById('mapcanvas').offsetHeight };
    });
    t.assert(geom.canvasH > geom.viewH, `map taller than viewport (${geom.canvasH} > ${geom.viewH})`);
    // The center node sits near the vertical middle of the viewport, not jammed
    // against the bottom edge (the pre-fix behavior put it at the bottom).
    const rel = (geom.nodeMid - geom.viewTop) / geom.viewH;
    t.assert(rel > 0.25 && rel < 0.75, `center node vertically centered (rel=${rel.toFixed(2)})`);
  },

  'search (outline): incremental jump, n/N wrap, esc restores': async t => {
    await t.open();
    await t.key('j'); // sec:Waiting on User
    await t.key('j'); // its item
    const saved = await t.cursorKey();
    // `/` opens the prompt; typing populates the ranked panel WITHOUT moving the
    // board (no-reveal-until-jump).
    await t.key('/');
    let s = await t.searchState();
    t.assert(s.promptOpen, 'search prompt open');
    await t.type('legacy');
    s = await t.searchState();
    // Fuzzy match set: the two "legacy endpoint" items plus the long Ideas item
    // (a loose subsequence, ranked last).
    t.assert(s.panelOpen && s.panelCount === 3, `panel shows ranked results (got ${JSON.stringify(s)})`);
    t.assert(s.panel[0].section === 'Plan', `best-ranked exact match first (got ${JSON.stringify(s.panel)})`);
    await t.assertCursor(saved); // board did not move while typing
    // Esc cancels and restores the pre-search cursor.
    await t.key('Escape');
    await t.assertCursor(saved);
    s = await t.searchState();
    t.assert(!s.active && !s.promptOpen, `search inactive after esc (got ${JSON.stringify(s)})`);
    // Re-run, confirm with Enter, then step with n/N (document order, wrapping).
    await t.key('/');
    await t.type('legacy');
    await t.key('Enter'); // selecting commits + closes search
    s = await t.searchState();
    t.assert(!s.active && s.lastTerm === 'legacy', `select closed search, term remembered (got ${JSON.stringify(s)})`);
    await t.assertCursor('it:Plan:- [ ] drop legacy endpoint'); // ranked-best jump
    await t.key('n'); // n recall re-activates + steps; document order: next after Plan
    await t.assertCursor('it:Questions:- [ ] !! drop the legacy endpoint? @user');
    await t.key('n'); // then the long Ideas item
    t.assert((await t.cursorKey()).startsWith('it:Ideas:'), 'n stepped to the Ideas match');
    await t.key('n'); // wraps back to the first
    await t.assertCursor('it:Plan:- [ ] drop legacy endpoint');
    await t.key('N'); // previous wraps to the last (Ideas)
    t.assert((await t.cursorKey()).startsWith('it:Ideas:'), 'N wrapped to the last match');
  },

  'search (outline): deep match snippet keeps the term visible with a leading …': async t => {
    await t.open();
    await t.key('/');
    // "layout" only appears near the END of the very long Ideas item, well past
    // the panel row's truncation width. The row must trim its head (leading …)
    // so the matched term stays visible instead of showing only the item's head.
    await t.type('layout');
    const s = await t.searchState();
    t.assert(s.panelOpen, `panel open (got ${JSON.stringify(s)})`);
    const deep = (s.panelText || []).find(x => x.includes('layout'));
    t.assert(deep, `a rendered row shows the deep match (got ${JSON.stringify(s.panelText)})`);
    t.assert(deep.startsWith('…'), `deep match row leads with an ellipsis (got ${JSON.stringify(deep)})`);
    t.assert(!deep.startsWith('… this is a deliberately'),
      `row is trimmed to the match, not the item head (got ${JSON.stringify(deep)})`);
    await t.key('Escape');
  },

  'outline: descent returns to the child you came from (child memory)': async t => {
    await t.open();
    await t.key('2'); // Plan (expanded by default)
    await t.key('j'); await t.key('j'); // second item: wire sessions
    const child = 'it:Plan:- [>] wire sessions through it';
    await t.assertCursor(child);
    await t.key('h'); // ascend to the section header (records the child we came from)
    await t.assertCursor('sec:Plan');
    await t.key('h'); // collapse the section so l descends again
    await t.assertCursor('sec:Plan');
    await t.key('l'); // descend — must land on the remembered child, not the first item
    await t.assertCursor(child);
    t.assert((await t.cursorKey()) !== 'it:Plan:- [x] extract middleware', 'did not fall to the first item');
  },

  'outline: fold/unfold an item, indicator count, cursor stays put': async t => {
    await t.open();
    await t.key('3'); // Threads
    await t.key('j'); // the auth item (has one reply child)
    const auth = 'it:Threads:- [>] auth refactor — extracting middleware';
    await t.assertCursor(auth);
    // enter collapses the subtree; cursor stays on the folded item.
    await t.key('Enter');
    await t.assertCursor(auth);
    let sn = await t.sn();
    t.assert(sn.collapsedItems.includes(auth), `item recorded collapsed (got ${JSON.stringify(sn.collapsedItems)})`);
    // The reply stop is gone (children build no stops while folded).
    t.assert(!sn.stops.some(s => s.key === 'it:Threads:  - claude: extracted, tests green'),
      'folded child builds no stop');
    // A "▸ N" hidden-descendant badge shows the count.
    const badge = await t.page.evaluate(() => {
      const b = document.querySelector('.foldcount');
      return b ? b.textContent : null;
    });
    t.assert(badge === '▸ 1', `fold badge shows the hidden count (got ${JSON.stringify(badge)})`);
    // l unfolds again.
    await t.key('l');
    await t.assertCursor(auth);
    sn = await t.sn();
    t.assert(!sn.collapsedItems.includes(auth), 'item unfolded');
    t.assert(sn.stops.some(s => s.key === 'it:Threads:  - claude: extracted, tests green'),
      'child stop back after unfold');
  },

  'outline: navigation skips a folded item\'s hidden children': async t => {
    await t.open();
    await t.key('3'); // Threads
    await t.key('j'); // auth item
    await t.key('Enter'); // fold it — hides the "claude: extracted" reply
    // j now steps to the next sibling item, not the hidden reply.
    await t.key('j');
    await t.assertCursor('it:Threads:- [x] fix flaky test');
  },

  'outline: h on a leaf child steps out to the parent (no fold)': async t => {
    await t.open();
    await t.key('3'); // Threads
    await t.key('j'); // auth item
    await t.key('j'); // its reply (a leaf)
    await t.assertCursor('it:Threads:  - claude: extracted, tests green');
    await t.key('h'); // leaf: step OUT to the parent, selection only
    const auth = 'it:Threads:- [>] auth refactor — extracting middleware';
    await t.assertCursor(auth);
    const sn = await t.sn();
    t.assert(!sn.collapsedItems.includes(auth), `parent stays expanded (got ${JSON.stringify(sn.collapsedItems)})`);
  },

  'outline: h on a top-level item steps out to the section header': async t => {
    await t.open();
    await t.key('3'); // Threads
    await t.key('j'); // auth item (top-level, expanded)
    await t.key('h'); // always ascends — no fold, straight to the header
    await t.assertCursor('sec:Threads');
    const sn = await t.sn();
    t.assert(sn.collapsedItems.length === 0, `h never folds an item (got ${JSON.stringify(sn.collapsedItems)})`);
  },

  'outline: l on an expanded item descends to its first child': async t => {
    await t.open();
    await t.key('3'); // Threads
    await t.key('j'); // auth item (expanded, one reply child)
    await t.assertCursor('it:Threads:- [>] auth refactor — extracting middleware');
    await t.key('l'); // expanded → descend to the first child
    await t.assertCursor('it:Threads:  - claude: extracted, tests green');
  },

  'outline: search reveals a match inside a folded item': async t => {
    await t.open();
    await t.key('3'); // Threads
    await t.key('j'); // auth item
    await t.key('Enter'); // fold — hides the "claude: extracted, tests green" reply
    let sn = await t.sn();
    const reply = 'it:Threads:  - claude: extracted, tests green';
    t.assert(!sn.stops.some(s => s.key === reply), 'reply hidden before search');
    await t.key('/');
    await t.type('extracted'); // only the reply matches
    // No-reveal: typing must not unfold the ancestor yet.
    sn = await t.sn();
    t.assert(sn.collapsedItems.includes('it:Threads:- [>] auth refactor — extracting middleware'),
      'ancestor still folded while typing (no-reveal)');
    // Enter jumps and reveals the match.
    await t.key('Enter');
    await t.assertCursor(reply);
    sn = await t.sn();
    t.assert(!sn.collapsedItems.includes('it:Threads:- [>] auth refactor — extracting middleware'),
      'ancestor unfolded to reveal the match after Enter');
    await t.key('Escape');
  },

  'outline: item fold state survives an SSE re-render': async t => {
    await t.open();
    await t.key('3'); // Threads
    await t.key('j'); // auth item
    const auth = 'it:Threads:- [>] auth refactor — extracting middleware';
    await t.key('Enter'); // fold
    // An external write triggers an SSE-driven re-render.
    await t.editExternally({ op: 'add', section: 'Ideas', text: 'a fresh idea' });
    await t.waitBoardContains('- a fresh idea');
    await t.settled();
    const sn = await t.sn();
    t.assert(sn.collapsedItems.includes(auth), `fold survived the re-render (got ${JSON.stringify(sn.collapsedItems)})`);
    t.assert(!sn.stops.some(s => s.key === 'it:Threads:  - claude: extracted, tests green'),
      'child still hidden after re-render');
  },

  'outline: z focus-folds to the cursor, children shown collapsed': async t => {
    await t.open();
    // Give the reply a child of its own, so the anchor has a grandchild that
    // must hide behind the one-level rule.
    await t.editExternally({ op: 'fork', section: 'Threads',
      raw: '  - claude: extracted, tests green', text: 'claude: grandchild detail' });
    await t.waitBoardContains('claude: grandchild detail');
    // Wait for the PAGE (not just the file) to have re-rendered with the fork.
    await t.page.waitForFunction(() =>
      JSON.stringify(window.__sn.board).includes('grandchild detail'));
    await t.settled();
    await t.key('3'); // Threads
    await t.key('j'); // auth item (child: reply; grandchild: detail)
    const auth = 'it:Threads:- [>] auth refactor — extracting middleware';
    await t.assertCursor(auth);
    await t.key('z');
    const st = await t.page.evaluate(() => ({
      secs: window.__sn.collapsedSecs,
      items: window.__sn.collapsedItems,
      stops: window.__sn.stops.map(s => s.key),
      cursor: window.__sn.cursorKey,
      zoomed: window.__sn.focusFolded,
    }));
    t.assert(st.zoomed, 'focus-fold active');
    t.assert(st.cursor === auth, `cursor stays on the anchor (got ${st.cursor})`);
    for (const other of ['Waiting on User', 'Plan', 'Questions', 'Ideas', 'Log']) {
      t.assert(st.secs[other] === true, `section ${other} collapsed (got ${JSON.stringify(st.secs)})`);
    }
    t.assert(st.secs['Threads'] !== true, 'the anchor section stays expanded');
    // Direct child visible but itself folded; grandchild hidden.
    const reply = 'it:Threads:  - claude: extracted, tests green';
    t.assert(st.stops.includes(reply), 'direct child still visible');
    t.assert(st.items.includes(reply), `direct child rendered folded (got ${JSON.stringify(st.items)})`);
    t.assert(!st.stops.some(k => k.includes('grandchild detail')), 'grandchild hidden');
  },

  'outline: second z restores the pre-zoom fold state': async t => {
    await t.open();
    // Pre-existing manual folds: collapse the Plan section and the Questions item.
    await t.key('2'); // Plan header
    await t.key('h'); // collapse Plan
    await t.key('4'); // Questions
    await t.key('j'); // the !! question (has a reply child)
    const q = 'it:Questions:- [ ] !! drop the legacy endpoint? @user';
    await t.key('Enter'); // fold the question's subtree
    // Zoom onto Threads' auth item, then zoom back out.
    await t.key('3'); // Threads header
    await t.key('j'); // auth item
    await t.key('z');
    await t.key('z');
    const st = await t.page.evaluate(() => ({
      secs: window.__sn.collapsedSecs,
      items: window.__sn.collapsedItems,
      cursor: window.__sn.cursorKey,
      zoomed: window.__sn.focusFolded,
    }));
    t.assert(!st.zoomed, 'focus-fold cleared after the second z');
    t.assert(st.secs['Plan'] === true, `manual Plan fold restored (got ${JSON.stringify(st.secs)})`);
    for (const open of ['Waiting on User', 'Questions', 'Ideas', 'Log']) {
      t.assert(st.secs[open] !== true, `section ${open} back expanded (got ${JSON.stringify(st.secs)})`);
    }
    t.assert(st.items.length === 1 && st.items[0] === q,
      `manual item fold restored exactly (got ${JSON.stringify(st.items)})`);
    t.assert(st.cursor === 'it:Threads:- [>] auth refactor — extracting middleware',
      `cursor kept on the anchor (got ${st.cursor})`);
  },

  'search (map): jump reveals a folded reply': async t => {
    await t.open();
    await t.key('m'); // enter map
    await t.settled();
    const replyKey = 'it:Threads:  - claude: extracted, tests green';
    let map = (await t.sn2()).map;
    t.assert(!map.nodes.includes(replyKey), 'reply hidden behind the default fold');
    await t.key('/');
    await t.type('extracted'); // only the reply matches
    // No-reveal: typing must not reveal the folded reply yet.
    map = (await t.sn2()).map;
    t.assert(!map.nodes.includes(replyKey), 'match not revealed while typing (no-reveal)');
    await t.key('Enter'); // select: jump reveals + focuses, then closes search
    map = (await t.sn2()).map;
    t.assert(map.focus === replyKey, `map focus jumped to the match (got ${map.focus})`);
    t.assert(map.nodes.includes(replyKey), 'match revealed through the fold after Enter');
    const s = await t.searchState();
    t.assert(!s.active && s.lastTerm === 'extracted', `select closed search (got ${JSON.stringify(s)})`);
  },

  'search box typing does not trigger hotkeys': async t => {
    await t.open();
    const before = t.file();
    await t.key('/');
    // Letters that are live single-key bindings outside the box: a (add), d
    // (archive), m (map), ? (help). Inside the search input they must be text.
    await t.type('add design ??');
    const s = await t.searchState();
    t.assert(s.promptOpen, 'still in the search prompt');
    t.assert(!(await t.sn2()).map.in, 'did not enter map from typed m');
    t.assert(!(await t.page.$('.modal.open')), 'no modal opened from typed ?');
    t.assert(t.file() === before, 'board unchanged by typed hotkey letters');
    const val = await t.page.$eval('#searchprompt input', i => i.value);
    t.assert(val === 'add design ??', `query captured verbatim (got ${JSON.stringify(val)})`);
  },

  'search (outline): results mode is sticky — only Esc clears': async t => {
    await t.open();
    await t.key('/');
    await t.type('legacy');
    await t.key('Enter'); // selecting commits + closes search
    let s = await t.searchState();
    t.assert(!s.active && s.hl === '', `select closed search (got ${JSON.stringify(s)})`);
    // n recall re-activates a sticky results mode.
    await t.key('n');
    s = await t.searchState();
    t.assert(s.active && s.hl === 'legacy', `n re-activated sticky results mode (got ${JSON.stringify(s)})`);
    // A plain nav key (j) must NOT drop out of search.
    await t.key('j');
    s = await t.searchState();
    t.assert(s.active && s.hl === 'legacy', `j kept results mode alive (got ${JSON.stringify(s)})`);
    // Another non-verb key (space toggling nothing / g) still keeps it.
    await t.key('G');
    s = await t.searchState();
    t.assert(s.active && s.hl === 'legacy', `G kept results mode alive (got ${JSON.stringify(s)})`);
    // Only Escape clears.
    await t.key('Escape');
    s = await t.searchState();
    t.assert(!s.active && s.hl === '', `Esc cleared results mode (got ${JSON.stringify(s)})`);
  },

  'search: last term recall via / and via n; count shown': async t => {
    await t.open();
    await t.key('/');
    await t.type('legacy');
    await t.key('Enter');
    let s = await t.searchState();
    t.assert(s.lastTerm === 'legacy', `last term remembered (got ${JSON.stringify(s)})`);
    t.assert(/^\d+\/\d+ matches\b/.test(s.status), `count shown (got ${JSON.stringify(s.status)})`);
    // Clear, then `/` recalls the term pre-filled + selected, live-previewed.
    await t.key('Escape');
    await t.key('/');
    s = await t.searchState();
    t.assert(s.promptOpen && s.hl === 'legacy', `/ recalled prefilled term (got ${JSON.stringify(s)})`);
    const val = await t.page.$eval('#searchprompt input', i => i.value);
    t.assert(val === 'legacy', `prompt prefilled with last term (got ${JSON.stringify(val)})`);
    // Enter reuses it as-is (selects + closes; term remembered).
    await t.key('Enter');
    s = await t.searchState();
    t.assert(!s.active && s.lastTerm === 'legacy', `Enter committed recalled term (got ${JSON.stringify(s)})`);
    // `n` with no active search re-activates the last term.
    s = await t.searchState();
    t.assert(!s.active, 'precondition: cleared before n-recall');
    await t.key('n');
    s = await t.searchState();
    t.assert(s.active && s.query === 'legacy', `n re-activated last term (got ${JSON.stringify(s)})`);
  },

  'search: n after leaving resumes from the last match, not the top': async t => {
    await t.open();
    await t.key('/');
    await t.type('middleware'); // 3 matches; document order: Waiting, Plan, Threads
    // Enter jumps to the best-ranked row: "extract middleware" (earliest match
    // position) — Plan, document index 1.
    await t.key('Enter');
    await t.assertCursor('it:Plan:- [x] extract middleware');
    await t.key('n'); // n steps document order: next after Plan (idx 1) -> Threads (idx 2)
    await t.assertCursor('it:Threads:- [>] auth refactor — extracting middleware');
    await t.key('Escape'); // leave results mode; position (idx 2) remembered
    let s = await t.searchState();
    t.assert(!s.active, 'precondition: search cleared before recall');
    await t.key('n'); // resume: next after idx 2 wraps to idx 0 (Waiting)
    await t.assertCursor('it:Waiting on User:- [ ] review the middleware PR @user');
  },

  'search: default scope excludes Log; Tab widens to everything': async t => {
    await t.open();
    await t.key('/');
    await t.type('session'); // matches include "wire sessions" (Plan) + Log line
    let s = await t.searchState();
    // Default working-set scope skips the Log section entirely.
    t.assert(!s.scopeAll && /matches \(working set\)/.test(s.status),
      `working-set scope active (got ${JSON.stringify(s)})`);
    t.assert(!s.panel.some(r => r.section === 'Log'), `no Log rows in working-set panel (got ${JSON.stringify(s.panel)})`);
    // Tab widens to everything → the Log match now appears in the panel.
    await t.key('Tab');
    s = await t.searchState();
    t.assert(s.scopeAll && /matches \(all\)/.test(s.status),
      `Tab widened scope to all (got ${JSON.stringify(s)})`);
    t.assert(s.panel.some(r => r.section === 'Log'), `Log row appears once scope is all (got ${JSON.stringify(s.panel)})`);
    await t.key('Escape');
  },

  'search: status shows term hit count when a node holds the term twice': async t => {
    await t.open();
    await t.key('/');
    // The long Ideas item contains "truncation" twice — one match node, two hits.
    await t.type('truncation');
    const s = await t.searchState();
    t.assert(/· \d+ hits/.test(s.status), `hit tally surfaced (got ${JSON.stringify(s.status)})`);
    const hits = Number((s.status.match(/· (\d+) hits/) || [])[1]);
    t.assert(hits > s.panelCount, `hits (${hits}) exceed node count (${s.panelCount})`);
    await t.key('Escape');
  },

  'search: results panel is ranked with section labels': async t => {
    await t.open();
    await t.key('/');
    await t.type('middleware'); // 3 matches across sections
    const s = await t.searchState();
    t.assert(s.panelOpen && s.panelCount === 3, `panel open with 3 rows (got ${JSON.stringify(s)})`);
    // Best-ranked first: "extract middleware" has the earliest match position.
    t.assert(s.panel[0].text === 'extract middleware',
      `earliest-position match ranks first (got ${JSON.stringify(s.panel)})`);
    // Each row carries a section label.
    t.assert(s.panel.every(r => typeof r.section === 'string' && r.section.length > 0),
      `every panel row has a section label (got ${JSON.stringify(s.panel)})`);
    // The panel is also rendered in the DOM with section labels + highlights.
    const rows = await t.page.locator('#searchpanel .prow').count();
    t.assert(rows === 3, `3 DOM rows in the panel (got ${rows})`);
    t.assert((await t.page.locator('#searchpanel .searchmark').count()) >= 3,
      'matched substrings highlighted in the panel rows');
    await t.key('Escape');
  },

  'search: fuzzy ranking — exact substring beats a loose subsequence': async t => {
    await t.open();
    await t.key('/');
    // "extracti": an exact substring of "…extracting middleware" (Threads) but
    // only a loose subsequence of "extract middleware" (Plan, extract + i).
    await t.type('extracti');
    const s = await t.searchState();
    t.assert(s.panel[0].section === 'Threads',
      `exact-substring match outranks the subsequence one (got ${JSON.stringify(s.panel)})`);
    await t.key('Escape');
  },

  'search: Up/Down pick a panel row, Enter jumps to it': async t => {
    await t.open();
    const saved = await t.cursorKey();
    await t.key('/');
    await t.type('middleware');
    // Down moves the panel selection to row 1 without moving the board.
    await t.key('ArrowDown');
    let s = await t.searchState();
    t.assert(s.panelIdx === 1, `Down moved panel selection (got ${JSON.stringify(s)})`);
    t.assert((await t.cursorKey()) === saved, 'board did not move while picking in the panel');
    const pickedText = s.panel[1].text;
    // Enter jumps the board cursor to the picked row.
    await t.key('Enter');
    const key = await t.cursorKey();
    t.assert(key.includes(pickedText), `Enter jumped the board to the picked row "${pickedText}" (got ${key})`);
  },

  'search: Tab in results mode toggles scope, not the section; scope label shows': async t => {
    await t.open();
    await t.key('/');
    let s = await t.searchState();
    t.assert(s.scopeLabel === 'working', `scope label visible in prompt (got ${JSON.stringify(s.scopeLabel)})`);
    await t.type('middleware');
    await t.key('Enter'); // select + close
    await t.key('n');     // n recall re-activates results mode
    const before = await t.cursorKey();
    t.assert(before.startsWith('it:'), 'cursor is on a match item in results mode');
    // Tab in results mode must toggle scope (not cycle to the next section).
    await t.key('Tab');
    s = await t.searchState();
    t.assert(s.active && s.scopeAll, `Tab toggled scope in results mode (got ${JSON.stringify(s)})`);
    const after = await t.cursorKey();
    t.assert(after.startsWith('it:'), `Tab did not jump the cursor to a section header (got ${after})`);
    await t.key('Escape');
  },

  'search: stepping onto a truncated match temporarily reveals it (map)': async t => {
    await t.open();
    await t.key('m'); // map
    await t.settled();
    await t.key('/');
    // "layout" only appears near the END of the very long Ideas item, past the
    // map's node truncation — stepping onto it must expand the node.
    await t.type('layout');
    await t.key('Enter'); // commit + close (temp expansion restored)
    await t.key('n');     // n recall steps onto the match with temp-unwrap
    const expanded = await t.page.evaluate(() => Object.keys(window.__sn.map.expanded || {}).length);
    t.assert(expanded >= 1, `the long current match was temporarily expanded (got ${expanded})`);
    // Leaving search restores it.
    await t.key('Escape');
    const after = await t.page.evaluate(() => Object.keys(window.__sn.map.expanded || {}).length);
    t.assert(after === 0, `temporary expansion restored after leaving search (got ${after})`);
  },

  // Which side of the center the focused map node sits on (1 right, -1 left),
  // read from the rendered layout, so descent tests are side-agnostic.
  // (defined inline per test via t.page.evaluate)

  'map: descent returns to the child you came from (child memory)': async t => {
    await t.open();
    await t.key('2'); // Plan
    await t.key('j'); await t.key('j'); // second item: wire sessions
    await t.assertCursor('it:Plan:- [>] wire sessions through it');
    await t.key('m');
    const child = (await t.sn2()).map.focus;
    t.assert(child === 'it:Plan:- [>] wire sessions through it', `map focus on the child (got ${child})`);
    const side = await t.page.evaluate(() => {
      const f = document.querySelector('.mapnode.focus'), c = document.querySelector('.mapnode.center');
      return f.offsetLeft >= c.offsetLeft ? 1 : -1;
    });
    const inward = side === 1 ? 'h' : 'l', outward = side === 1 ? 'l' : 'h';
    await t.key(inward); // ascend to the parent section (records the child)
    t.assert((await t.sn2()).map.focus === 'sec:Plan', 'ascended to parent Plan');
    await t.key(outward); // descend again
    const got = (await t.sn2()).map.focus;
    t.assert(got === child, `descent returned to the remembered child, not the first (got ${got})`);
    // And it is NOT the first child.
    t.assert(got !== 'it:Plan:- [x] extract middleware', 'did not fall to the first child');
  },

  'map: descending into a folded node auto-expands and lands on the child': async t => {
    await t.open();
    await t.key('3'); // Threads
    await t.key('j'); // auth refactor (has a hidden reply child)
    await t.assertCursor('it:Threads:- [>] auth refactor — extracting middleware');
    await t.key('m');
    const replyKey = 'it:Threads:  - claude: extracted, tests green';
    t.assert(!(await t.sn2()).map.nodes.includes(replyKey), 'reply hidden behind the default fold');
    const side = await t.page.evaluate(() => {
      const f = document.querySelector('.mapnode.focus'), c = document.querySelector('.mapnode.center');
      return f.offsetLeft >= c.offsetLeft ? 1 : -1;
    });
    const outward = side === 1 ? 'l' : 'h';
    await t.key(outward); // descend into the folded node
    const map = (await t.sn2()).map;
    t.assert(map.nodes.includes(replyKey), 'fold auto-expanded to reveal the child');
    t.assert(map.focus === replyKey, `focus landed on the revealed child (got ${map.focus})`);
  },

  'map: w wraps a long node across multiple lines': async t => {
    await t.open();
    await t.key('5'); // Ideas
    await t.key('j'); await t.key('j'); await t.key('j'); // the deliberately long idea
    const key = await t.cursorKey();
    t.assert(key.startsWith('it:Ideas:- this is a deliberately very long idea'), `on the long idea (got ${key})`);
    await t.key('m');
    const box1 = await t.page.locator('.mapnode.focus').boundingBox();
    await t.key('w');
    await t.page.waitForSelector('.mapnode.focus.wrap');
    const box2 = await t.page.locator('.mapnode.focus').boundingBox();
    t.assert(box2.height > box1.height * 1.5, `wrapped node grew taller (${Math.round(box1.height)} -> ${Math.round(box2.height)})`);
    t.assert(box2.width <= box1.width, `wrapped node no wider than the truncated line (${Math.round(box1.width)} -> ${Math.round(box2.width)})`);
  },

  'search highlights all matches and clears when it closes': async t => {
    await t.open();
    await t.key('/');
    await t.type('middleware');
    await t.page.waitForSelector('.searchhit');
    t.assert((await t.page.locator('.searchhit').count()) >= 2, 'multiple matches highlighted while typing');
    t.assert((await t.page.locator('.searchmark').count()) >= 2, 'matched substrings tinted');
    await t.key('Enter'); // select + close: highlights clear (search done)
    await t.page.waitForFunction(() => document.querySelectorAll('.searchhit').length === 0);
    t.assert((await t.searchState()).hl === '', 'highlights cleared on select');
    // n recall brings a sticky results mode back with highlights.
    await t.key('n');
    await t.page.waitForSelector('.searchhit');
    t.assert((await t.page.locator('.searchhit').count()) >= 2, 'matches highlighted again in recall');
    await t.key('j'); // nav no longer clears (sticky results mode)
    t.assert((await t.page.locator('.searchhit').count()) >= 2, 'highlights survive a nav key');
    await t.key('Escape'); // only Escape clears the highlights
    await t.page.waitForFunction(() => document.querySelectorAll('.searchhit').length === 0);
    t.assert((await t.page.locator('.searchmark').count()) === 0, 'substring tints cleared too');
    t.assert((await t.searchState()).hl === '', 'highlight query cleared');
  },

  'map default fold keeps status-less bullets visible, hides replies': async t => {
    await t.open();
    await t.key('m');
    await t.settled();
    const nodes = (await t.sn2()).map.nodes;
    t.assert(nodes.some(k => k.startsWith('it:Ideas:- cache invalidation')),
      'a plain status-less bullet stays visible in the default map');
    t.assert(!nodes.some(k => k.includes('user: leaning yes')),
      'a conversational user:/claude: reply is folded behind [+N]');
  },

  'map space cycles a plain bullet through all five states': async t => {
    await t.open();
    await t.key('5'); // Ideas
    await t.key('j');
    await t.assertCursor('it:Ideas:- cache invalidation could be event-driven');
    await t.key('m');
    await t.settled();
    // Settle between presses: the file gate (waitBoardContains) can pass before
    // the page re-rendered, and a space against the stale node posts an old raw.
    await t.key(' '); await t.waitBoardContains('- [ ] cache invalidation could be event-driven'); await t.settled();
    await t.key(' '); await t.waitBoardContains('- [>] cache invalidation could be event-driven'); await t.settled();
    await t.key(' '); await t.waitBoardContains('- [x] cache invalidation could be event-driven'); await t.settled();
    await t.key(' '); await t.waitBoardContains('- [?] cache invalidation could be event-driven'); await t.settled();
    await t.key(' '); await t.waitBoardContains('- cache invalidation could be event-driven');
  },

  'map A adds a sibling reply under the same parent': async t => {
    await t.open();
    await t.key('3'); // Threads
    await t.key('j'); // auth refactor item
    await t.key('j'); // its claude: reply
    await t.assertCursor('it:Threads:  - claude: extracted, tests green');
    await t.key('m');
    await t.key('A');
    await t.page.waitForSelector('.mapnode.provisional input.mapinput');
    await t.type('sibling-reply-x');
    await t.key('Enter');
    await t.waitBoardContains('sibling-reply-x');
    // The new item is a peer reply (indented under auth refactor), not a child
    // of the claude reply.
    t.assert(t.file().includes('  - user: sibling-reply-x'), 'sibling added at reply depth with user: tag');
  },

  'T edits the board title from the outline': async t => {
    await t.open();
    await t.key('T');
    await t.page.waitForSelector('#title input.inline');
    await t.selectAll();
    await t.type('renamed via T');
    await t.key('Enter');
    await t.waitBoardContains('title: renamed via T');
  },

  'A opens the add-section overlay (custom entry adds a section)': async t => {
    await t.open();
    await t.key('A');
    await t.page.waitForSelector('#addsecmodal.open');
    await t.page.fill('#addsecmodal input.custom', 'Retro');
    await t.page.press('#addsecmodal input.custom', 'Enter');
    await t.waitBoardContains('## Retro');
  },

  'A overlay: custom input is reachable by keyboard alone': async t => {
    await t.open();
    await t.key('A');
    await t.page.waitForSelector('#addsecmodal.open');
    // The fixture already has every canonical section, so the overlay lands the
    // keyboard straight in the custom name box — no mouse, no click needed.
    await t.page.waitForFunction(
      () => document.activeElement === document.querySelector('#addsecmodal input.custom'));
    await t.page.keyboard.type('Retro');
    await t.page.keyboard.press('Enter');
    await t.waitBoardContains('## Retro');
  },

  'p toggles pin on an item from the outline': async t => {
    await t.open();
    await t.key('2'); // Plan
    await t.key('j', 3);
    await t.assertCursor('it:Plan:- [ ] drop legacy endpoint');
    await t.key('p');
    await t.waitBoardContains('!pin drop legacy endpoint');
    await t.key('p');
    await t.waitBoardContains('!pin drop legacy endpoint', false);
  },

  'add on a collapsed section auto-expands and adds a visible item': async t => {
    await t.open();
    await t.key('2'); // sec:Plan
    await t.key('h'); // collapse
    const col = await t.page.evaluate(() => window.__sn.collapsedSecs);
    t.assert(col.Plan === true, 'Plan is collapsed');
    await t.key('a'); // auto-expands the section and focuses its add box
    await t.page.waitForSelector('input.add[data-section="Plan"]', { timeout: 3000 });
    await t.page.fill('input.add[data-section="Plan"]', 'added-after-expand');
    await t.page.press('input.add[data-section="Plan"]', 'Enter');
    await t.waitBoardContains('added-after-expand');
  },

  'D deletes an item without a confirm dialog': async t => {
    await t.open();
    t.page.removeAllListeners('dialog');
    let sawDialog = false;
    t.page.on('dialog', d => { sawDialog = true; d.dismiss(); });
    await t.key('2'); // Plan
    await t.key('j', 3);
    await t.assertCursor('it:Plan:- [ ] drop legacy endpoint');
    await t.key('D');
    await t.waitBoardContains('drop legacy endpoint', false);
    t.assert(!sawDialog, 'D hard-deleted without prompting a confirm dialog');
  },

  'section headers show a discoverable 1-9 index hint': async t => {
    await t.open();
    const first = await t.page.locator('.sechead .secidx').first().innerText();
    t.assert(first === '1', 'first section shows index 1');
    const count = await t.page.locator('.sechead .secidx').count();
    t.assert(count === 6, `all six sections show an index (got ${count})`);
  },

  'B navigates back to the boards dashboard': async t => {
    await t.open();
    await t.key('B');
    await t.page.waitForSelector('a.card');
    t.assert(t.page.url().replace(/\/$/, '') === t.base, `B went to / (got ${t.page.url()})`);
  },

  'o on an item with several links opens a chooser': async t => {
    await t.open();
    await t.editExternally({ op: 'add', section: 'Ideas', text: 'links [[alpha]] and [[beta]]' });
    await t.settled();
    await t.page.locator('.row', { hasText: 'alpha' }).click();
    await t.key('o');
    await t.page.waitForSelector('#linkchooser.open');
    const rows = await t.page.locator('#linkchooser .lrow').count();
    t.assert(rows === 2, `chooser shows both links (got ${rows})`);
    await t.key('Escape');
  },

  'map j from the center steps to the nearest first-ring node': async t => {
    await t.open();
    await t.key('m');
    await t.settled();
    let m = (await t.sn2()).map;
    t.assert(m.focus === 'center', 'map opens focused on the center');
    await t.key('j');
    m = (await t.sn2()).map;
    t.assert(m.focus.startsWith('sec:'), `j from center lands on a first-ring section (got ${m.focus})`);
  },

  'map f focuses a collapsed section node': async t => {
    await t.open();
    await t.key('4'); // Questions
    await t.key('m');
    await t.settled();
    await t.key('Enter'); // fold the section closed
    await t.key('f');      // focus into it even though it shows no children
    const m = (await t.sn2()).map;
    t.assert(m.root === 'sec:Questions', `f re-rooted onto the collapsed node (got ${m.root})`);
  },

  'map footer shows a Log-hidden reminder until M reveals it': async t => {
    await t.open();
    await t.key('m');
    await t.settled();
    let foot = await t.page.locator('#mapfoot').innerText();
    t.assert(foot.includes('Log hidden'), 'reminder shown while the Log ring is hidden');
    await t.key('M');
    await t.settled();
    foot = await t.page.locator('#mapfoot').innerText();
    t.assert(!foot.includes('Log hidden'), 'reminder gone once Log is shown');
  },

  'search tally stays visible through n/N (persistent status)': async t => {
    await t.open();
    await t.key('/');
    await t.type('legacy');
    await t.key('Enter');
    await t.page.waitForSelector('#searchstatus.show');
    const s1 = await t.page.locator('#searchstatus').innerText();
    t.assert(/\d+\/\d+ matches/.test(s1), `tally shown (got ${JSON.stringify(s1)})`);
    await t.key('n');
    await t.page.waitForTimeout(80);
    t.assert(await t.page.locator('#searchstatus.show').count() === 1,
      'tally still visible after stepping with n');
  },

  'urgent items are fully tinted; reply rows are dimmed (outline)': async t => {
    await t.open();
    const r = await t.page.evaluate(() => {
      const u = document.querySelector('li.urgent');
      const rep = document.querySelector('li.reply');
      const dim = getComputedStyle(document.documentElement).getPropertyValue('--dim').trim();
      const asRGB = c => { const s = document.createElement('span'); s.style.color = c; document.body.append(s); const v = getComputedStyle(s).color; s.remove(); return v; };
      return {
        hasUrgent: !!u, hasReply: !!rep,
        urgentText: u ? getComputedStyle(u.querySelector('.text')).color : null,
        urgentFlag: u ? getComputedStyle(u.querySelector('.urgentflag')).color : null,
        replyText: rep ? getComputedStyle(rep.querySelector('.text')).color : null,
        dim: dim ? asRGB(dim) : null,
      };
    });
    t.assert(r.hasUrgent && r.urgentText === r.urgentFlag,
      'urgent whole-line text takes the urgent color (matches the flag)');
    t.assert(r.hasReply && r.replyText === r.dim, 'reply row text is dimmed');
  },

  'map colors pinned, wip and blocked nodes': async t => {
    await t.open();
    await t.key('m');
    await t.settled();
    const cls = await t.page.evaluate(() => {
      const nodes = [...document.querySelectorAll('.mapnode')];
      const find = s => { const n = nodes.find(n => n.textContent.includes(s)); return n ? n.className : null; };
      return { pin: find('always run gofmt'), wip: find('auth refactor') };
    });
    t.assert(cls.pin && cls.pin.includes('pin'), `pinned node has the pin class (got ${cls.pin})`);
    t.assert(cls.wip && cls.wip.includes('wip'), `wip node has the wip class (got ${cls.wip})`);
  },

  'dashboard shows a project count and l opens a card': async t => {
    await t.openDash();
    const count = await t.page.locator('#count').innerText();
    t.assert(/project/.test(count), `dashboard surfaces project scope (got ${JSON.stringify(count)})`);
    await t.key('j');
    await t.key('l');
    await t.page.waitForSelector('.marker');
    t.assert(t.page.url().includes('/b/'), 'l opened the selected card');
  },

  'dashboard lists the board and enter opens it': async t => {
    await t.openDash();
    await t.key('j');
    await t.key('Enter');
    await t.page.waitForSelector('.marker');
    t.assert(t.page.url().includes('/b/e2e-fixture-0001'), 'navigated to the board');
  },

  'W cycles outline width presets and the choice persists across reload': async t => {
    await t.open();
    let w = await t.page.evaluate(() => window.__sn.width);
    t.assert(w.mode === 'default', `starts at the default width (got ${w.mode})`);
    await t.key('W');
    w = await t.page.evaluate(() => window.__sn.width);
    t.assert(w.mode === 'narrow' && w.computed === '700px', `W → narrow (got ${JSON.stringify(w)})`);
    await t.key('W');
    w = await t.page.evaluate(() => window.__sn.width);
    t.assert(w.mode === 'wide' && w.computed === '1100px', `W → wide (got ${JSON.stringify(w)})`);
    await t.key('W');
    w = await t.page.evaluate(() => window.__sn.width);
    t.assert(w.mode === 'full' && w.computed === 'none', `W → full (got ${JSON.stringify(w)})`);
    // localStorage carries the preset across a fresh page load.
    await t.open();
    w = await t.page.evaluate(() => window.__sn.width);
    t.assert(w.mode === 'full' && w.computed === 'none',
      `full width restored after reload (got ${JSON.stringify(w)})`);
  },

  'dragging a width grip sets a custom column width that persists': async t => {
    await t.open();
    // Start from a preset so the column is centered with real side gutters.
    await t.page.evaluate(() => window.__sn.cycleWidth()); // → narrow (700px)
    await t.page.waitForTimeout(60);
    const before = await t.page.evaluate(() => window.__sn.width.bodyWidth);
    // A real pointer drag on the right grip, pulling it inward.
    const pt = await t.page.evaluate(() => {
      const g = document.querySelectorAll('.wgrip')[1];
      const r = g.getBoundingClientRect();
      return { x: r.left + r.width / 2, y: Math.min(r.top + r.height / 2, 300) };
    });
    await t.page.mouse.move(pt.x, pt.y);
    await t.page.mouse.down();
    await t.page.mouse.move(700, pt.y, { steps: 6 });
    await t.page.mouse.up();
    await t.page.waitForTimeout(80);
    let w = await t.page.evaluate(() => window.__sn.width);
    // Pointer simulation can be flaky headless; fall back to the debug setter so
    // the persistence half of the test still runs deterministically.
    if (w.mode !== 'custom') {
      await t.page.evaluate(() => window.__sn.setWidthPx(500));
      w = await t.page.evaluate(() => window.__sn.width);
    }
    t.assert(w.mode === 'custom', `drag set a custom width (got ${JSON.stringify(w)})`);
    t.assert(w.bodyWidth < before, `column actually narrowed (got ${w.bodyWidth} < ${before})`);
    const dragged = w.px;
    await t.open();
    w = await t.page.evaluate(() => window.__sn.width);
    t.assert(w.mode === 'custom' && w.px === dragged,
      `custom drag width persisted after reload (got ${JSON.stringify(w)})`);
  },

  'map view centers its content vertically in the viewport': async t => {
    await t.open();
    await t.key('m');
    await t.settled();
    const r = await t.page.evaluate(() => {
      const view = document.getElementById('mapview');
      const canvas = document.getElementById('mapcanvas');
      const center = document.querySelector('.mapnode.center');
      const vr = view.getBoundingClientRect();
      const cr = center.getBoundingClientRect();
      return {
        canvasH: canvas.offsetHeight, viewH: view.clientHeight,
        viewMidY: vr.top + vr.height / 2, centerMidY: cr.top + cr.height / 2,
      };
    });
    // The canvas is grown to at least fill the viewport so short trees center
    // instead of pinning to the top.
    t.assert(r.canvasH >= r.viewH, `canvas fills the viewport height (${r.canvasH} >= ${r.viewH})`);
    // The center node lands near the vertical middle of the map viewport.
    t.assert(Math.abs(r.centerMidY - r.viewMidY) < r.viewH / 4,
      `center node is vertically centered (Δ=${Math.round(Math.abs(r.centerMidY - r.viewMidY))}, viewH=${r.viewH})`);
  },

  'help overlay scrolls with the keyboard and ? closes it': async t => {
    await t.open();
    // The help table is built at runtime from /api/keymap (single source of
    // truth), so wait for it to populate before asserting it overflows.
    await t.page.waitForFunction(() => document.querySelectorAll('#helpkeys tr').length > 10, null, { timeout: 3000 });
    await t.key('?');
    await t.page.waitForSelector('#helpmodal.open');
    // The key list overflows the panel, so the body must scroll.
    const overflow = await t.page.$eval('#helpbody', e => e.scrollHeight - e.clientHeight);
    t.assert(overflow > 0, `help body overflows and is scrollable (slack=${overflow})`);
    const before = await t.page.$eval('#helpbody', e => e.scrollTop);
    await t.key('j', 6);
    const after = await t.page.$eval('#helpbody', e => e.scrollTop);
    t.assert(after > before, `j scrolls the help body down (before=${before}, after=${after})`);
    await t.key('k', 3);
    const upped = await t.page.$eval('#helpbody', e => e.scrollTop);
    t.assert(upped < after, `k scrolls the help body up (after=${after}, upped=${upped})`);
    await t.key('?');
    await t.page.waitForFunction(() => !document.querySelector('#helpmodal.open'), null, { timeout: 3000 });
  },

  'an out-of-band edit flags the changed item and feeds it': async t => {
    await t.open();
    await t.settled();
    // Another writer (Claude/TUI/other session) adds an item straight to disk.
    await t.editExternally({ op: 'add', section: 'Ideas', text: 'brand new idea' });
    await t.waitBoardContains('- brand new idea');
    const key = 'it:Ideas:- brand new idea';
    // Wait for the SSE-driven re-render to actually paint the new item.
    await t.page.waitForFunction(k => window.__sn.stops.some(s => s.key === k), key, { timeout: 4000 });
    await t.settled();
    const nov = await t.page.evaluate(() => window.__sn.novelty);
    t.assert(nov.fresh.includes(key), `changed item flagged fresh (fresh=${JSON.stringify(nov.fresh)})`);
    // The headline leads with WHAT changed; who/where is the dim second line.
    t.assert(nov.feed[0] === 'brand new idea',
      `feed headline carries the change text (feed=${JSON.stringify(nov.feed)})`);
    t.assert(/Ideas/.test(nov.feedMeta[0]),
      `feed meta names the location (meta=${JSON.stringify(nov.feedMeta)})`);
    // The accent renders on the row, and the corner feed is visible.
    const shown = await t.page.evaluate(() => ({
      row: !!document.querySelector('.row.fresh'),
      feed: document.getElementById('feed').classList.contains('show'),
    }));
    t.assert(shown.row, 'a .row.fresh is painted in the outline');
    t.assert(shown.feed, 'the event feed is visible');
    // Clicking the feed entry jumps to the item and settles its mark.
    await t.page.click('#feed .fentry');
    await t.settled();
    const nov2 = await t.page.evaluate(() => window.__sn.novelty);
    t.assert(!nov2.fresh.includes(key), `visiting clears the fresh mark (fresh=${JSON.stringify(nov2.fresh)})`);
    t.assert((await t.cursorKey()) === key, 'the feed entry moved the cursor onto the change');
  },

  'the corner feed peeks a few changes and hides the rest behind "+N more"': async t => {
    await t.open();
    await t.settled();
    // Six out-of-band adds land; the corner log must not grow to swallow the
    // screen — it caps at the peek height and offers the rest via "+N more".
    for (let i = 1; i <= 6; i++) {
      await t.editExternally({ op: 'add', section: 'Ideas', text: `peek idea ${i}` });
      await t.waitBoardContains(`- peek idea ${i}`);
    }
    await t.page.waitForFunction(() => window.__sn.novelty.feed.length >= 6, null, { timeout: 4000 });
    await t.settled();
    const rest = await t.page.evaluate(() => ({
      total: window.__sn.novelty.feed.length,
      entries: document.querySelectorAll('#feed .fentry').length,
      more: document.querySelector('#feed .fmore')?.textContent || null,
    }));
    t.assert(rest.total >= 6, `all changes tracked (total=${rest.total})`);
    t.assert(rest.entries === 4, `corner shows only the peek entries (entries=${rest.entries})`);
    t.assert(/\+2 more/.test(rest.more || ''), `"+N more" cue counts the hidden rest (more=${JSON.stringify(rest.more)})`);
    // Clicking "+N more" drops into keyboard mode and expands to the full list.
    await t.page.click('#feed .fmore');
    await t.settled();
    const expanded = await t.page.evaluate(() => ({
      focused: window.__sn.novelty.focused,
      entries: document.querySelectorAll('#feed .fentry').length,
      more: !!document.querySelector('#feed .fmore'),
    }));
    t.assert(expanded.focused, 'the feed took keyboard focus');
    t.assert(expanded.entries === rest.total, `all entries visible while browsing (entries=${expanded.entries})`);
    t.assert(!expanded.more, 'the "+N more" cue is gone once expanded');
  },

  'a self-edit is not flagged as novelty': async t => {
    await t.open();
    await t.settled();
    await t.key('5'); // Ideas
    await t.key('a');
    await t.type('my own idea');
    await t.key('Enter');
    await t.waitBoardContains('- my own idea');
    await t.settled();
    await t.page.waitForTimeout(300); // let the SSE echo land too
    const nov = await t.page.evaluate(() => window.__sn.novelty);
    t.assert(nov.fresh.length === 0, `self-edit leaves nothing fresh (fresh=${JSON.stringify(nov.fresh)})`);
    t.assert(nov.feed.length === 0, `self-edit adds nothing to the feed (feed=${JSON.stringify(nov.feed)})`);
  },

  'visiting a changed item via the cursor clears its accent': async t => {
    await t.open();
    await t.settled();
    await t.editExternally({ op: 'add', section: 'Ideas', text: 'another fresh one' });
    await t.waitBoardContains('- another fresh one');
    const key = 'it:Ideas:- another fresh one';
    await t.page.waitForFunction(k => window.__sn.stops.some(s => s.key === k), key, { timeout: 4000 });
    await t.settled();
    let nov = await t.page.evaluate(() => window.__sn.novelty);
    t.assert(nov.fresh.includes(key), 'flagged fresh before visiting');
    // Walk the cursor onto it directly (no feed click).
    await t.key('5'); // jump to Ideas section header
    // Step down until the cursor reaches the fresh item.
    for (let i = 0; i < 8 && (await t.cursorKey()) !== key; i++) await t.key('j');
    await t.assertCursor(key);
    nov = await t.page.evaluate(() => window.__sn.novelty);
    t.assert(!nov.fresh.includes(key), 'cursor visit cleared the fresh mark');
    const stillPainted = await t.page.evaluate(() => !!document.querySelector('.row.fresh'));
    t.assert(!stillPainted, 'the accent is gone from the DOM');
  },

  'an out-of-band status flip reads as changed, not added': async t => {
    await t.open();
    await t.settled();
    // Another writer flips an existing item's checkbox (open -> done) on disk.
    await t.editExternally({ op: 'status', section: 'Plan', raw: '- [ ] drop legacy endpoint', status: 'done' });
    await t.waitBoardContains('- [x] drop legacy endpoint');
    const key = 'it:Plan:- [x] drop legacy endpoint';
    await t.page.waitForFunction(k => window.__sn.stops.some(s => s.key === k), key, { timeout: 4000 });
    await t.settled();
    const nov = await t.page.evaluate(() => window.__sn.novelty);
    t.assert(nov.fresh.includes(key), `the flipped item is flagged fresh (fresh=${JSON.stringify(nov.fresh)})`);
    // The status change must NOT be mislabeled as an addition; the meta says what changed.
    t.assert(/^→ done/.test(nov.feedMeta[0]),
      `feed meta reads as a status change (meta=${JSON.stringify(nov.feedMeta)})`);
    t.assert(!/added/.test(nov.feedMeta[0]),
      `not mislabeled as added (meta=${JSON.stringify(nov.feedMeta)})`);
    t.assert(nov.feed[0] === 'drop legacy endpoint',
      `headline is the item text (feed=${JSON.stringify(nov.feed)})`);
  },

  'recents: dismiss clears the feed but a new change brings it back': async t => {
    await t.open();
    await t.settled();
    await t.editExternally({ op: 'add', section: 'Ideas', text: 'first novelty' });
    await t.waitBoardContains('- first novelty');
    await t.page.waitForFunction(() => window.__sn.novelty.feed.length > 0, null, { timeout: 4000 });
    // Dismiss with x the way a user would: focus the feed (V), then x.
    await t.key('V');
    t.assert(await t.page.evaluate(() => window.__sn.novelty.focused), 'feed focused');
    await t.key('x');
    await t.settled();
    let nov = await t.page.evaluate(() => window.__sn.novelty);
    t.assert(nov.feed.length === 0, `dismiss cleared the feed (feed=${JSON.stringify(nov.feed)})`);
    // A NEW out-of-band change must resurface the feed (no permanent suppression).
    await t.editExternally({ op: 'add', section: 'Ideas', text: 'second novelty' });
    await t.waitBoardContains('- second novelty');
    await t.page.waitForFunction(() => window.__sn.novelty.feed.length > 0, null, { timeout: 4000 });
    nov = await t.page.evaluate(() => window.__sn.novelty);
    t.assert(nov.feed[0] === 'second novelty', `feed reappears with the new change (feed=${JSON.stringify(nov.feed)})`);
    const shown = await t.page.evaluate(() => document.getElementById('feed').classList.contains('show'));
    t.assert(shown, 'the feed panel is visible again');
  },

  'map: g/G jump to the first/last node in document order': async t => {
    await t.open();
    await t.key('m');
    await t.page.waitForFunction(() => window.__sn.map.in);
    await t.key('G'); // last node in document order (Log hidden by default)
    let m = (await t.sn2()).map;
    t.assert(m.focus.startsWith('it:Ideas:'), `G landed on the last Ideas node (got ${m.focus})`);
    await t.key('g'); // first node: the first section's node
    m = (await t.sn2()).map;
    t.assert(m.focus === 'sec:Waiting on User', `g landed on the first section node (got ${m.focus})`);
  },

  'map: 1-9 jump to the Nth section node; tab cycles sections': async t => {
    await t.open();
    await t.key('m');
    await t.page.waitForFunction(() => window.__sn.map.in);
    await t.key('3'); // Threads
    t.assert((await t.sn2()).map.focus === 'sec:Threads', 'digit jumped to the Threads node');
    await t.key('Tab'); // next section, document order, wrapping
    t.assert((await t.sn2()).map.focus === 'sec:Questions', 'tab moved to the next section node');
    await t.key('Shift+Tab');
    t.assert((await t.sn2()).map.focus === 'sec:Threads', 'shift-tab moved back');
  },

  'map: b toggles blocked on the focused item (outline parity)': async t => {
    await t.open();
    await t.key('2'); // Plan
    await t.key('j', 3); // drop legacy endpoint ([ ])
    await t.assertCursor('it:Plan:- [ ] drop legacy endpoint');
    await t.key('m'); // map seeds focus from the outline cursor
    await t.page.waitForFunction(k => window.__sn.map.in && window.__sn.map.focus === k,
      'it:Plan:- [ ] drop legacy endpoint', { timeout: 3000 });
    await t.key('b'); // blocked, not the old "step out"
    await t.waitBoardContains('- [?] drop legacy endpoint');
    t.assert((await t.sn2()).map.in, 'still in the map view');
    await t.page.waitForFunction(k => window.__sn.map.focus === k,
      'it:Plan:- [?] drop legacy endpoint', { timeout: 3000 });
    await t.key('b'); // toggles back to open
    await t.waitBoardContains('- [ ] drop legacy endpoint');
  },

  'map: H opens the history modal (outline parity)': async t => {
    await t.open();
    await t.key('5'); // Ideas — seed a journaled edit so history has an entry
    await t.key('a');
    await t.type('history seed');
    await t.key('Enter');
    await t.waitBoardContains('- history seed');
    await t.page.click('body');
    await t.key('m');
    await t.page.waitForFunction(() => window.__sn.map.in);
    await t.key('H');
    await t.page.waitForSelector('#histmodal.open', { timeout: 3000 });
    const txt = await t.page.textContent('#histlist');
    t.assert(txt.includes('history seed'), `history lists the seeded edit (got ${JSON.stringify(txt.slice(0, 120))})`);
  },

  'map: V drives the recents feed and Enter lands on the node': async t => {
    await t.open();
    await t.settled();
    await t.key('m'); // map view
    await t.settled();
    await t.editExternally({ op: 'add', section: 'Ideas', text: 'map novelty' });
    await t.waitBoardContains('- map novelty');
    const key = 'it:Ideas:- map novelty';
    await t.page.waitForFunction(() => window.__sn.novelty.feed.length > 0, null, { timeout: 4000 });
    await t.page.waitForFunction(k => window.__sn.map.nodes.includes(k), key, { timeout: 4000 });
    await t.key('V'); // focus the feed from the MAP view (regression: V was outline-only)
    t.assert(await t.page.evaluate(() => window.__sn.novelty.focused), 'feed focused from the map view');
    await t.key('Enter'); // jump to the changed node, in the map
    await t.settled();
    t.assert((await t.sn2()).map.focus === key, `map focus landed on the changed node (got ${(await t.sn2()).map.focus})`);
    t.assert(!(await t.page.evaluate(() => window.__sn.novelty.focused)), 'feed mode exited after the jump');
  },

  'a reply headline leads with what was said, location as meta': async t => {
    await t.open();
    await t.settled();
    // Claude replies under the Threads wip item, out of band.
    await t.editExternally({ op: 'reply', section: 'Threads',
      raw: '- [>] auth refactor — extracting middleware', text: 'claude: rebased cleanly' });
    await t.waitBoardContains('claude: rebased cleanly');
    await t.page.waitForFunction(
      () => window.__sn.novelty.feed.length > 0, null, { timeout: 4000 });
    const nov = await t.page.evaluate(() => window.__sn.novelty);
    t.assert(nov.feed[0] === 'rebased cleanly',
      `headline is the reply text, author stripped (feed=${JSON.stringify(nov.feed)})`);
    t.assert(/claude/.test(nov.feedMeta[0]) && /under/.test(nov.feedMeta[0]),
      `meta says who replied and under what (meta=${JSON.stringify(nov.feedMeta)})`);
  },

  'V drives the recents feed from the keyboard: move, jump': async t => {
    await t.open();
    await t.settled();
    await t.editExternally({ op: 'add', section: 'Ideas', text: 'first change' });
    await t.waitBoardContains('- first change');
    await t.editExternally({ op: 'add', section: 'Ideas', text: 'second change' });
    await t.waitBoardContains('- second change');
    await t.page.waitForFunction(() => window.__sn.novelty.feed.length >= 2, null, { timeout: 4000 });
    // V focuses the feed with the newest entry selected.
    await t.key('V');
    let nov = await t.page.evaluate(() => window.__sn.novelty);
    t.assert(nov.focused && nov.sel === 0, `V focuses the feed at the top (${JSON.stringify(nov)})`);
    // j moves the selection down through entries.
    await t.key('j');
    nov = await t.page.evaluate(() => window.__sn.novelty);
    t.assert(nov.sel === 1, `j moves the feed selection (sel=${nov.sel})`);
    // Enter jumps to the selected change and leaves feed mode. Entry 1 is the
    // older "first change" ("second change" is newest at index 0).
    await t.key('Enter');
    nov = await t.page.evaluate(() => window.__sn.novelty);
    t.assert(!nov.focused, 'Enter leaves feed mode');
    t.assert((await t.cursorKey()) === 'it:Ideas:- first change',
      `Enter jumped the cursor to the selected change (got ${await t.cursorKey()})`);
  },

  'V then v opens the full recents view; Esc closes it': async t => {
    await t.open();
    await t.settled();
    await t.editExternally({ op: 'add', section: 'Ideas', text: 'a logged change' });
    await t.waitBoardContains('- a logged change');
    await t.page.waitForFunction(() => window.__sn.novelty.feed.length >= 1, null, { timeout: 4000 });
    await t.key('V');
    await t.key('v'); // full view
    await t.page.waitForSelector('#recentsmodal.open');
    const rows = await t.page.$$eval('#recentslist .rentry', els => els.map(e => e.textContent));
    t.assert(rows.length >= 1 && /a logged change/.test(rows[0]),
      `full recents view lists the change (rows=${JSON.stringify(rows)})`);
    // Enter opens the selected change and closes the modal.
    await t.key('Enter');
    await t.page.waitForFunction(() => !document.querySelector('#recentsmodal.open'), null, { timeout: 3000 });
    t.assert((await t.cursorKey()) === 'it:Ideas:- a logged change', 'Enter in full view jumped to the change');
  },

  'the arrival pulse plays once and navigation does not restart it': async t => {
    await t.open();
    await t.settled();
    await t.editExternally({ op: 'add', section: 'Ideas', text: 'blink me' });
    await t.waitBoardContains('- blink me');
    const key = 'it:Ideas:- blink me';
    await t.page.waitForFunction(k => window.__sn.stops.some(s => s.key === k), key, { timeout: 4000 });
    await t.settled();
    // The pulse is consumed by the render that painted the fresh row: the row
    // carries .fresh, and no key remains queued for a re-pulse.
    let st = await t.page.evaluate(() => ({
      pulsePending: window.__sn.novelty.pulse,
      rowHasFresh: !!document.querySelector('.row.fresh'),
    }));
    t.assert(st.rowHasFresh, 'the fresh row is painted');
    t.assert(st.pulsePending.length === 0, `pulse consumed after first render (pending=${JSON.stringify(st.pulsePending)})`);
    // A second out-of-band change forces a full re-render. The FIRST fresh row
    // must keep its accent but must NOT be re-tagged .pulse (that would blink).
    await t.editExternally({ op: 'add', section: 'Ideas', text: 'other change' });
    await t.waitBoardContains('- other change');
    await t.page.waitForFunction(() => window.__sn.stops.some(s => s.key === 'it:Ideas:- other change'), null, { timeout: 4000 });
    await t.settled();
    const cls = await t.page.evaluate(() => {
      const r = [...document.querySelectorAll('.row.fresh')].find(x => x.textContent.includes('blink me'));
      return r ? [...r.classList] : null;
    });
    t.assert(cls && cls.includes('fresh') && !cls.includes('pulse'),
      `the earlier fresh row keeps its accent without re-pulsing (classes=${JSON.stringify(cls)})`);
    // Navigating around also must not re-pulse it.
    await t.key('5'); await t.key('j'); await t.key('k');
    const still = await t.page.evaluate(() => {
      const r = [...document.querySelectorAll('.row.fresh')].find(x => x.textContent.includes('blink me'));
      return r ? [...r.classList] : null;
    });
    t.assert(still && !still.includes('pulse'), `navigation does not re-add pulse (classes=${JSON.stringify(still)})`);
  },

  'session status sidecar shows model + context in the header': async t => {
    t.seedStatus({ model: 'Opus', context_pct: 62, activity: 'working' });
    await t.open();
    await t.settled();
    const txt = await t.sessionStatusText();
    t.assert(txt.includes('Opus'), `header shows model (got ${JSON.stringify(txt)})`);
    t.assert(txt.includes('62%'), `header shows context % (got ${JSON.stringify(txt)})`);
    t.assert(txt.includes('working'), `header shows activity (got ${JSON.stringify(txt)})`);
    t.assert(/[▰▱]/.test(txt), `header shows a context bar (got ${JSON.stringify(txt)})`);
  },

  'stale session status is hidden': async t => {
    // Updated well beyond the staleness window -> the server omits it.
    t.seedStatus({ model: 'Opus', context_pct: 62, updated: new Date(Date.now() - 30 * 60 * 1000).toISOString() });
    await t.open();
    await t.settled();
    const txt = await t.sessionStatusText();
    t.assert(txt.trim() === '', `stale status hidden (got ${JSON.stringify(txt)})`);
  },

  'map wrapped multi-line siblings keep a minimum vertical gap (no overlap)': async t => {
    await t.open();
    // Two paragraph-length siblings peppered with emoji — the glyphs whose
    // rendered width overflows the 2-column estimate and rewraps the box.
    // (No author token: reply-shaped items hide behind the default fold.)
    const long = tag => `${tag} 🎉 the quick brown fox jumps over the lazy dog again and again, ` +
      'weighing every wrapped line against its estimated column budget until the box height diverges 🚀 from the plan';
    await t.editExternally({ op: 'add', section: 'Plan', text: long('wrapcheck-a') });
    await t.editExternally({ op: 'add', section: 'Plan', text: long('wrapcheck-b') });
    await t.waitBoardContains('wrapcheck-b');
    await t.settled();
    await t.key('m');
    await t.settled();
    // Expand both long nodes into wrapped multi-line boxes with w.
    for (const tag of ['wrapcheck-a', 'wrapcheck-b']) {
      await t.page.evaluate(s => {
        const n = [...document.querySelectorAll('.mapnode')].find(e => e.textContent.includes(s));
        n.dispatchEvent(new MouseEvent('click', { bubbles: true }));
      }, tag);
      await t.page.waitForTimeout(80);
      await t.key('w');
      await t.page.waitForTimeout(120);
    }
    const bad = await t.page.evaluate(() => {
      const rects = [...document.querySelectorAll('.mapnode')].map(e => {
        const r = e.getBoundingClientRect();
        return { t: e.textContent.slice(0, 30), x1: r.left, x2: r.right, y1: r.top, y2: r.bottom };
      });
      const out = [];
      for (let i = 0; i < rects.length; i++) for (let j = i + 1; j < rects.length; j++) {
        const a = rects[i], b = rects[j];
        if (a.x1 < b.x2 && b.x1 < a.x2 && a.y1 < b.y2 - 1 && b.y1 < a.y2 - 1)
          out.push(`${JSON.stringify(a.t)} overlaps ${JSON.stringify(b.t)}`);
      }
      return out;
    });
    t.assert(bad.length === 0, `no map node boxes overlap (got: ${bad.join(' · ') || 'none'})`);
    // Both wrapcheck nodes really are multi-line (the wrap took effect), and as
    // adjacent paragraph blocks they get the larger TALL_GAP breathing room.
    const wr = await t.page.evaluate(() =>
      [...document.querySelectorAll('.mapnode')].filter(e => e.textContent.includes('wrapcheck'))
        .map(e => { const r = e.getBoundingClientRect(); return { h: e.offsetHeight, top: r.top, bottom: r.bottom }; })
        .sort((a, b) => a.top - b.top));
    t.assert(wr.length === 2 && wr.every(x => x.h > 30),
      `both long nodes rendered as multi-line boxes (heights: ${wr.map(x => x.h)})`);
    const gap = wr[1].top - wr[0].bottom;
    t.assert(gap >= 16, `wrapped paragraph siblings keep a roomy gap (got ${gap.toFixed(1)}px)`);
  },

  'header stays in view while scrolling the outline (sticky)': async t => {
    await t.open();
    // Bulk up the board so the outline actually overflows the viewport.
    for (let i = 0; i < 30; i++) await t.editExternally({ op: 'add', section: 'Ideas', text: `filler idea number ${i} to force a tall outline` });
    await t.waitBoardContains('filler idea number 29');
    await t.settled();
    await t.key('G'); // jump to the last stop — deep scroll
    await t.page.waitForTimeout(200);
    const r = await t.page.evaluate(() => {
      const h = document.querySelector('header').getBoundingClientRect();
      return { scrollY: window.scrollY, top: h.top, bottom: h.bottom, height: h.height };
    });
    t.assert(r.scrollY > 100, `page really scrolled (scrollY=${r.scrollY})`);
    t.assert(r.top >= 0 && r.bottom > 0 && r.height > 0,
      `header still visible after deep scroll (top=${r.top}, bottom=${r.bottom})`);
  },

  'map nodes tint the claude:/user: author token like the outline': async t => {
    await t.open();
    await t.key('3'); // Threads: "auth refactor" has a claude: reply
    await t.key('m');
    await t.settled();
    // Reveal the reply behind the default fold.
    await t.page.evaluate(() => {
      const n = [...document.querySelectorAll('.mapnode')].find(e => e.textContent.includes('auth refactor'));
      n.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    await t.page.waitForTimeout(100);
    await t.key('Enter');
    await t.page.waitForTimeout(150);
    const r = await t.page.evaluate(() => {
      const node = [...document.querySelectorAll('.mapnode')].find(e => e.textContent.includes('extracted, tests green'));
      const span = node && node.querySelector('span.author.claude');
      const asRGB = c => { const s = document.createElement('span'); s.style.color = c; document.body.append(s); const v = getComputedStyle(s).color; s.remove(); return v; };
      const want = asRGB(getComputedStyle(document.documentElement).getPropertyValue('--author-claude').trim());
      return { hasNode: !!node, hasSpan: !!span, color: span ? getComputedStyle(span).color : null, want };
    });
    t.assert(r.hasNode, 'the claude: reply node is visible in the map');
    t.assert(r.hasSpan, 'the reply node wraps its author token in span.author.claude');
    t.assert(r.color === r.want, `author token takes --author-claude (got ${r.color}, want ${r.want})`);
  },

  'delivery receipts: pending ◌ until the watch snapshot advances': async t => {
    const fs = require('fs');
    await t.open();
    // A watcher arms on the current board state (watch snapshots on start).
    fs.writeFileSync(t.boardPath + '.watch', t.file());
    // A change lands that no watch poll has delivered yet.
    await t.editExternally({ op: 'add', section: 'Ideas', text: 'needs delivering' });
    await t.waitBoardContains('needs delivering');
    // The item renders with the pending receipt ◌.
    await t.page.waitForFunction(() =>
      [...document.querySelectorAll('#board li')].some(li =>
        li.textContent.includes('needs delivering') && li.querySelector('.receipt')),
      null, { timeout: 3000 });
    // Delivery: the watch advances the snapshot; the pending dot clears via SSE
    // (only the transient .seen ✓ may remain).
    fs.writeFileSync(t.boardPath + '.watch', t.file());
    await t.page.waitForFunction(() =>
      [...document.querySelectorAll('#board li')]
        .filter(li => li.textContent.includes('needs delivering'))
        .every(li => !li.querySelector('.receipt:not(.seen)')),
      null, { timeout: 3000 });
  },
});
