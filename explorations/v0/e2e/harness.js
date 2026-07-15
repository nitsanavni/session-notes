// Harness for browser e2e tests of the v0 outliner prototype.
//
// Each test gets a FRESH server (own free port, own empty temp -data dir, so the
// seed tree is regenerated) and a fresh Chromium page. On failure it dumps the
// step log, page/console errors, the __v0 debug handle, a screenshot, and the
// events.jsonl log into ./artifacts.
//
// Invoked by run.sh; see scenarios.js for the tests. Reuses the *ideas* of
// ../../test/e2e/harness.js, not its code (different app, different API).
'use strict';
const { chromium } = require('playwright-core');
const { spawn } = require('child_process');
const fs = require('fs');
const os = require('os');
const path = require('path');
const net = require('net');

// V0_BIN: prebuilt server binary (run.sh builds it). Falls back to `go run`.
const BIN = process.env.V0_BIN || '';
const GO = process.env.V0_GO || 'go';
const SRC = path.resolve(__dirname, '..');
const CHROME = process.env.V0_CHROME || defaultChrome();
const HEADLESS = process.env.V0_HEADED !== '1';
const ARTIFACTS = path.join(__dirname, 'artifacts');

function defaultChrome() {
  const home = os.homedir();
  const cands = [
    path.join(home, 'Library/Caches/ms-playwright/chromium-1232/chrome-mac-x64/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing'),
    path.join(home, 'Library/Caches/ms-playwright/chromium-1232/chrome-mac/Chromium.app/Contents/MacOS/Chromium'),
    '/opt/pw-browsers/chromium',
    '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  ];
  return cands.find(p => fs.existsSync(p)) || cands[0];
}

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
    this.dir = fs.mkdtempSync(path.join(os.tmpdir(), 'v0-e2e-'));
    this.dataDir = path.join(this.dir, 'data');
    this.port = await freePort();

    const [cmd, args] = BIN
      ? [BIN, ['-port', String(this.port), '-data', this.dataDir]]
      : [GO, ['run', SRC, '-port', String(this.port), '-data', this.dataDir]];
    this.server = spawn(cmd, args, { cwd: SRC, stdio: ['ignore', 'pipe', 'pipe'] });
    this.serverLog = '';
    this.server.stdout.on('data', d => (this.serverLog += d));
    this.server.stderr.on('data', d => (this.serverLog += d));
    this.base = `http://127.0.0.1:${this.port}`;
    await waitFor(async () => {
      try { return (await fetch(`${this.base}/api/tree`)).ok; } catch { return false; }
    }, 20000, 'server to come up');

    this.browser = await chromium.launch({
      executablePath: CHROME,
      headless: HEADLESS,
      args: ['--no-sandbox', '--no-proxy-server'],
    });
    this.page = await this.browser.newPage({ viewport: { width: 900, height: 900 } });
    this.page.on('pageerror', e => this.pageErrors.push(e.message));
    this.page.on('console', m => { if (m.type() === 'error') this.consoleErrors.push(m.text()); });
  }

  // ---- driving ----
  async open() {
    this.log('open /');
    await this.page.goto(this.base + '/');
    await this.page.waitForFunction(() => window.__v0 && window.__v0.ready === true, null, { timeout: 8000 });
    await this.settled();
  }

  async key(k, times = 1) {
    for (let i = 0; i < times; i++) {
      this.log(`key ${k}`);
      await this.page.keyboard.press(k);
      await this.page.waitForTimeout(70);
    }
  }

  async type(text) {
    this.log(`type ${JSON.stringify(text)}`);
    await this.page.keyboard.type(text);
    await this.page.waitForTimeout(30);
  }

  // settled: let any deferred (debounced) render + fetch cycle land.
  async settled() { await this.page.waitForTimeout(150); }

  // v0 exposes the page debug handle fields.
  v0() {
    return this.page.evaluate(() => ({
      cursor: window.__v0.cursor,
      focus: window.__v0.focus,
      editing: window.__v0.editing,
      rendered: window.__v0.rendered,
      quotes: window.__v0.quotes,
      visible: window.__v0.visible,
      nodeCount: window.__v0.nodeCount,
    }));
  }
  async cursor() { return this.page.evaluate(() => window.__v0.cursor); }
  async focus() { return this.page.evaluate(() => window.__v0.focus); }

  // ---- API helpers ----
  async tree() { return (await fetch(`${this.base}/api/tree`)).json(); }
  async ctx(node, depth) {
    const q = new URLSearchParams({ node });
    if (depth != null) q.set('depth', String(depth));
    return (await fetch(`${this.base}/api/context?${q}`)).json();
  }
  async apiCreate(body) {
    return (await fetch(`${this.base}/api/nodes`, { method: 'POST', body: JSON.stringify(body) })).json();
  }
  async apiPresence(actor, node) {
    return fetch(`${this.base}/api/presence`, { method: 'POST', body: JSON.stringify({ actor, node }) });
  }

  // childrenOf: edges of a given parent id from a tree snapshot.
  static childEdges(tree, parent) { return tree.edges.filter(e => e.parent === parent); }

  // ---- asserting ----
  assert(cond, what) {
    this.log(`assert ${what}: ${cond ? 'ok' : 'FAIL'}`);
    if (!cond) throw new Error(`assertion failed: ${what}`);
  }

  // ---- teardown / diagnostics ----
  async dumpFailure(err) {
    fs.mkdirSync(ARTIFACTS, { recursive: true });
    const slug = this.name.replace(/\W+/g, '-');
    let log = '';
    try { log = fs.readFileSync(path.join(this.dataDir, 'events.jsonl'), 'utf8'); } catch {}
    const handle = await this.v0().catch(() => 'unavailable');
    const lines = [
      `test: ${this.name}`,
      `error: ${err.stack || err}`,
      '', '--- steps ---', ...this.steps,
      '', '--- page errors ---', ...this.pageErrors,
      '', '--- console errors ---', ...this.consoleErrors,
      '', '--- server log ---', this.serverLog,
      '', '--- __v0 ---', JSON.stringify(handle, null, 2),
      '', '--- events.jsonl ---', log,
    ];
    fs.writeFileSync(path.join(ARTIFACTS, slug + '.txt'), lines.join('\n'));
    if (this.page) await this.page.screenshot({ path: path.join(ARTIFACTS, slug + '.png') }).catch(() => {});
    return path.join(ARTIFACTS, slug + '.txt');
  }

  async stop() {
    if (this.browser) await this.browser.close().catch(() => {});
    if (this.server) this.server.kill();
    if (this.dir) fs.rmSync(this.dir, { recursive: true, force: true });
  }
}

async function run(tests) {
  const only = process.env.V0_ONLY;
  let failed = 0, ran = 0;
  for (const [name, fn] of Object.entries(tests)) {
    if (only && !name.includes(only)) continue;
    ran++;
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
  console.log(`\n${ran - failed}/${ran} passed`);
  process.exit(failed ? 1 : 0);
}

module.exports = { run };
