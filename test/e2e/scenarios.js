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
    await t.type('shipping it');
    await t.key('Enter');
    // Flat: sibling of "leaning yes", i.e. 2-space indent under the question.
    await t.waitBoardContains('\n  - user: shipping it\n');
    await t.key('F');
    await t.type('forked aside');
    await t.key('Enter');
    // Fork target is the cursor item (the "leaning yes" reply): nests to 4.
    await t.waitBoardContains('\n    - user: forked aside\n');
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
    t.assert(val === 'user: mid-typing', `input intact (got ${JSON.stringify(val)})`);
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
    t.assert(box === 'user: doomed reply', `text parked in add box (got ${JSON.stringify(box)})`);
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

  'E raw editor conflict keeps modal and text': async t => {
    await t.open();
    await t.key('E');
    await t.page.waitForSelector('#rawmodal.open');
    await t.editExternally({ op: 'log', text: 'race', author: 'claude' });
    await t.page.waitForTimeout(200);
    const ta = t.page.locator('#rawtext');
    await ta.fill((await ta.inputValue()) + '- my precious edit\n');
    await t.page.click('#rawsave');
    await t.page.waitForTimeout(500);
    t.assert(!!(await t.page.$('#rawmodal.open')), 'modal stayed open on conflict');
    t.assert((await ta.inputValue()).includes('my precious edit'), 'text kept on conflict');
    t.assert(!t.file().includes('my precious edit'), 'stale save did not clobber');
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

  'map: f focuses a subtree, b steps out': async t => {
    await t.open();
    await t.key('3'); // Threads head
    await t.key('m');
    await t.key('f'); // re-root on Threads
    let map = (await t.sn2()).map;
    t.assert(map.root === 'sec:Threads', `re-rooted (got ${map.root})`);
    t.assert(map.focus.startsWith('it:Threads:'), 'focus on first child');
    const crumb = await t.page.textContent('#mapcrumb');
    t.assert(crumb.includes('Threads'), 'breadcrumb shows path');
    await t.key('b');
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
    await t.key('a'); // add child (fork)
    await t.page.waitForSelector('#mapprompt.open');
    await t.type('from the map');
    await t.key('Enter');
    await t.waitBoardContains('  - user: from the map');
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
    await t.key('l'); // expand
    t.assert(!('Plan' in (await collapsed())), 'Plan expanded again');
    t.assert((await t.sn()).stops.some(s => s.key === itemStop && s.visible), 'items back');
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

  'add-mode: arrow up/down exits the add box and navigates': async t => {
    await t.open();
    await t.key('2'); // Plan head
    await t.key('a'); // open add box
    await t.page.waitForFunction(() => document.activeElement && document.activeElement.classList.contains('add'));
    await t.type('half-typed'); // discarded on arrow
    await t.page.keyboard.press('ArrowDown');
    await t.page.waitForTimeout(80);
    const active = await t.page.evaluate(() => document.activeElement.tagName);
    t.assert(active !== 'INPUT', `add box left on ArrowDown (active=${active})`);
    // Discarded text was not committed to the board.
    t.assert(!t.file().includes('half-typed'), 'unsent add text discarded');
    // Cursor moved onto the first Plan item.
    const key = await t.cursorKey();
    t.assert(key === 'it:Plan:- [x] extract middleware', `navigated to first item (got ${key})`);
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
    // b steps out of focus mode back to the whole board.
    await t.key('b');
    map = (await t.sn2()).map;
    t.assert(map.root === 'center', `b stepped out (got ${map.root})`);
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
    // `/` opens the prompt; typing jumps the cursor live to the first match.
    await t.key('/');
    let s = await t.searchState();
    t.assert(s.promptOpen, 'search prompt open');
    await t.type('legacy');
    await t.assertCursor('it:Plan:- [ ] drop legacy endpoint'); // first match, document order
    // Esc cancels and restores the pre-search cursor.
    await t.key('Escape');
    await t.assertCursor(saved);
    s = await t.searchState();
    t.assert(!s.active && !s.promptOpen, `search inactive after esc (got ${JSON.stringify(s)})`);
    // Re-run, confirm with Enter, then step with n/N (wrapping).
    await t.key('/');
    await t.type('legacy');
    await t.key('Enter');
    s = await t.searchState();
    t.assert(s.active && s.matches === 2 && s.idx === 0, `2 matches confirmed (got ${JSON.stringify(s)})`);
    await t.assertCursor('it:Plan:- [ ] drop legacy endpoint');
    await t.key('n'); // forward to the second match
    await t.assertCursor('it:Questions:- [ ] !! drop the legacy endpoint? @user');
    await t.key('n'); // wraps back to the first
    await t.assertCursor('it:Plan:- [ ] drop legacy endpoint');
    await t.key('N'); // previous wraps to the last
    await t.assertCursor('it:Questions:- [ ] !! drop the legacy endpoint? @user');
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
    map = (await t.sn2()).map;
    t.assert(map.focus === replyKey, `map focus jumped to the match (got ${map.focus})`);
    t.assert(map.nodes.includes(replyKey), 'match revealed through the fold');
    await t.key('Enter');
    const s = await t.searchState();
    t.assert(s.active && s.matches === 1, `confirmed one match (got ${JSON.stringify(s)})`);
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
    await t.key('Enter'); // confirm: highlights persist while n/N steps
    await t.page.waitForSelector('.searchhit');
    t.assert((await t.page.locator('.searchhit').count()) >= 2, 'matches stay highlighted after confirm');
    await t.key('j'); // a non-search action clears the highlights
    await t.page.waitForFunction(() => document.querySelectorAll('.searchhit').length === 0);
    t.assert((await t.page.locator('.searchmark').count()) === 0, 'substring tints cleared too');
    t.assert((await t.searchState()).hl === '', 'highlight query cleared');
  },

  'dashboard lists the board and enter opens it': async t => {
    await t.openDash();
    await t.key('j');
    await t.key('Enter');
    await t.page.waitForSelector('.marker');
    t.assert(t.page.url().includes('/b/e2e-fixture-0001'), 'navigated to the board');
  },
});
