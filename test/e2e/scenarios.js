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
    await t.page.keyboard.press('Control+a');
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

  'map: m toggles and selection carries over both ways': async t => {
    await t.open();
    await t.key('3'); // Threads head
    await t.key('j'); // first thread item
    const key = await t.cursorKey();
    await t.key('m');
    let map = (await t.sn2()).map;
    t.assert(map.in, 'map mode entered');
    t.assert(map.focus === key, `map focused the outline cursor (got ${map.focus})`);
    // Move toward the center: the parent. Which physical key that is depends
    // on which side the balancer put Threads on, so try h then l.
    await t.key('h');
    map = (await t.sn2()).map;
    if (map.focus !== 'sec:Threads') { await t.key('l'); map = (await t.sn2()).map; }
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

  'dashboard lists the board and enter opens it': async t => {
    await t.openDash();
    await t.key('j');
    await t.key('Enter');
    await t.page.waitForSelector('.marker');
    t.assert(t.page.url().includes('/b/e2e-fixture-0001'), 'navigated to the board');
  },
});
