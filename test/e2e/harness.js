// Harness for browser e2e tests of the session-notes web UI.
//
// Owns the whole lifecycle: temp boards dir seeded from fixture-board.md, a
// `session-notes serve` child on a free port, one Chromium page wired for
// diagnostics. Each test gets a FRESH server + board so tests never bleed
// state into each other. On failure it dumps everything you need to debug:
// the step log, page errors, the __sn debug handle (cursor + stops), a
// screenshot, and the board file's content.
//
// Invoked by run.sh; see scenarios.js for the tests themselves.
'use strict';
const { chromium } = require('playwright-core');
const { spawn } = require('child_process');
const fs = require('fs');
const os = require('os');
const path = require('path');
const net = require('net');

const BIN = process.env.SN_BIN || path.join(__dirname, 'session-notes-e2e-bin');
const CHROME = process.env.SN_CHROME || '/opt/pw-browsers/chromium';
const HEADLESS = process.env.SN_HEADED !== '1';
const FIXTURE = path.join(__dirname, 'fixture-board.md');
const BOARD_ID = 'e2e-fixture-0001';
const ARTIFACTS = path.join(__dirname, 'artifacts');

function freePort() {
  return new Promise((resolve, reject) => {
    const srv = net.createServer();
    srv.listen(0, '127.0.0.1', () => {
      const port = srv.address().port;
      srv.close(() => resolve(port));
    });
    srv.on('error', reject);
  });
}

async function waitFor(fn, ms, what) {
  const start = Date.now();
  for (;;) {
    if (await fn()) return;
    if (Date.now() - start > ms) throw new Error(`timeout waiting for ${what}`);
    await new Promise(r => setTimeout(r, 50));
  }
}

class Ctx {
  constructor(name) {
    this.name = name;
    this.steps = [];
    this.pageErrors = [];
    this.consoleErrors = [];
  }

  log(msg) { this.steps.push(msg); }

  async start() {
    this.dir = fs.mkdtempSync(path.join(os.tmpdir(), 'sn-e2e-'));
    this.boardsDir = path.join(this.dir, 'boards');
    fs.mkdirSync(this.boardsDir, { recursive: true });
    this.boardPath = path.join(this.boardsDir, BOARD_ID + '.md');
    fs.copyFileSync(FIXTURE, this.boardPath);

    this.port = await freePort();
    this.server = spawn(BIN, ['serve', '--addr', `127.0.0.1:${this.port}`], {
      env: {
        ...process.env,
        SESSION_NOTES_DIR: this.boardsDir,
        SESSION_NOTES_PROJECTS_DIR: path.join(this.dir, 'projects'),
      },
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    this.serverLog = '';
    this.server.stdout.on('data', d => (this.serverLog += d));
    this.server.stderr.on('data', d => (this.serverLog += d));
    this.base = `http://127.0.0.1:${this.port}`;
    await waitFor(async () => {
      try { return (await fetch(`${this.base}/api/boards`)).ok; } catch { return false; }
    }, 5000, 'server to come up');

    this.browser = await chromium.launch({
      executablePath: CHROME,
      headless: HEADLESS,
      args: ['--no-sandbox', '--no-proxy-server'],
    });
    this.page = await this.browser.newPage({ viewport: { width: 900, height: 900 } });
    this.page.on('pageerror', e => this.pageErrors.push(e.message));
    this.page.on('console', m => { if (m.type() === 'error') this.consoleErrors.push(m.text()); });
    // Dialogs (confirm() on deletes) auto-accept unless a test overrides.
    this.page.on('dialog', d => d.accept());
  }

  // ---- driving ----
  async open(id = BOARD_ID) {
    this.log(`open /b/${id}`);
    await this.page.goto(`${this.base}/b/${id}`);
    await this.page.waitForSelector('.marker');
    await this.settled();
  }

  async openDash() {
    this.log('open /');
    await this.page.goto(this.base + '/');
    await this.page.waitForSelector('a.card');
  }

  async key(k, times = 1) {
    for (let i = 0; i < times; i++) {
      this.log(`key ${k}`);
      await this.page.keyboard.press(k);
      await this.page.waitForTimeout(60);
    }
  }

  async type(text) {
    this.log(`type ${JSON.stringify(text)}`);
    await this.page.keyboard.type(text);
  }

  // selectAll issues the platform's "select all" chord for the focused input.
  // On macOS Chrome, Control+a is the emacs line-start binding (not select-all),
  // so it would leave the pre-filled text in place — use Meta+a there.
  async selectAll() {
    const chord = process.platform === 'darwin' ? 'Meta+a' : 'Control+a';
    this.log(`selectAll (${chord})`);
    await this.page.keyboard.press(chord);
  }

  // settled waits until the page has no deferred render pending and the last
  // fetch cycle finished — cheap: poll the __sn handle.
  async settled() {
    await this.page.waitForFunction(() => window.__sn && window.__sn.board !== null);
    await this.page.waitForTimeout(120);
  }

  // seedStatus writes a fresh session-status sidecar for the fixture board, the
  // way the statusline/lifecycle hooks would. `updated` defaults to now so the
  // UI treats it as fresh.
  seedStatus(obj) {
    const stateDir = path.join(this.boardsDir, '.state');
    fs.mkdirSync(stateDir, { recursive: true });
    const rec = { context_pct: -1, updated: new Date().toISOString(), ...obj };
    fs.writeFileSync(path.join(stateDir, BOARD_ID + '.status'), JSON.stringify(rec) + '\n');
  }

  // sessionStatusText returns the visible header status segment.
  async sessionStatusText() {
    return this.page.evaluate(() => document.getElementById('sessionstatus').textContent);
  }

  // sn exposes the page's debug handle.
  async sn() { return this.page.evaluate(() => ({
    cursorKey: window.__sn.cursorKey,
    inputsOpen: window.__sn.inputsOpen,
    stops: window.__sn.stops,
    collapsedItems: window.__sn.collapsedItems,
  })); }

  // sn2 includes the map state as well.
  async sn2() { return this.page.evaluate(() => ({
    cursorKey: window.__sn.cursorKey,
    inputsOpen: window.__sn.inputsOpen,
    map: window.__sn.map,
  })); }

  async cursorKey() { return (await this.sn()).cursorKey; }

  // searchState exposes the incremental-search debug handle.
  async searchState() { return this.page.evaluate(() => window.__sn.search); }

  // waitBoard polls until the board FILE contains (or stops containing) a string.
  async waitBoardContains(str, should = true) {
    this.log(`wait board ${should ? 'contains' : 'lacks'} ${JSON.stringify(str)}`);
    await waitFor(() => this.file().includes(str) === should, 4000,
      `board to ${should ? 'contain' : 'lack'} ${JSON.stringify(str)}`);
  }

  file() { return fs.readFileSync(this.boardPath, 'utf8'); }

  // editExternally mimics Claude/TUI writing the board while the page is open.
  editExternally(op) {
    this.log(`external edit ${JSON.stringify(op)}`);
    return fetch(`${this.base}/api/board/${BOARD_ID}/edit`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(op),
    });
  }

  // ---- asserting ----
  assert(cond, what) {
    this.log(`assert ${what}: ${cond ? 'ok' : 'FAIL'}`);
    if (!cond) throw new Error(`assertion failed: ${what}`);
  }

  async assertCursor(key) {
    const got = await this.cursorKey();
    this.assert(got === key, `cursor is ${JSON.stringify(key)} (got ${JSON.stringify(got)})`);
  }

  // ---- teardown / diagnostics ----
  async dumpFailure(err) {
    fs.mkdirSync(ARTIFACTS, { recursive: true });
    const slug = this.name.replace(/\W+/g, '-');
    const lines = [
      `test: ${this.name}`,
      `error: ${err.stack || err}`,
      '',
      '--- steps ---', ...this.steps,
      '', '--- page errors ---', ...this.pageErrors,
      '', '--- console errors ---', ...this.consoleErrors,
      '', '--- server log ---', this.serverLog,
      '', '--- __sn ---', JSON.stringify(await this.sn().catch(() => 'unavailable'), null, 2),
      '', '--- board file ---', this.file(),
    ];
    fs.writeFileSync(path.join(ARTIFACTS, slug + '.txt'), lines.join('\n'));
    await this.page.screenshot({ path: path.join(ARTIFACTS, slug + '.png') }).catch(() => {});
    return path.join(ARTIFACTS, slug + '.txt');
  }

  async stop() {
    if (this.browser) await this.browser.close().catch(() => {});
    if (this.server) this.server.kill();
    if (this.dir) fs.rmSync(this.dir, { recursive: true, force: true });
  }
}

// run executes named tests sequentially, each in a fresh Ctx.
async function run(tests) {
  const only = process.env.SN_ONLY;
  let failed = 0;
  for (const [name, fn] of Object.entries(tests)) {
    if (only && !name.includes(only)) continue;
    const ctx = new Ctx(name);
    const t0 = Date.now();
    try {
      await ctx.start();
      await fn(ctx);
      console.log(`ok   ${name} (${Date.now() - t0}ms)`);
    } catch (err) {
      failed++;
      const dump = await ctx.dumpFailure(err).catch(() => '(dump failed)');
      console.log(`FAIL ${name}: ${err.message}\n     details: ${dump}`);
    } finally {
      await ctx.stop();
    }
  }
  process.exit(failed ? 1 : 0);
}

module.exports = { run, BOARD_ID };
