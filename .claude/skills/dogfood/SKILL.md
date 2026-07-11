---
name: dogfood
description: Serve, watch, and edit a live session-notes board from inside a dev session — use when asked to dogfood, demo, babysit a board, or work the board protocol in a headless/remote environment (Claude Code on the web, CI, containers).
---

# Dogfood a live board

The binary documents its own protocol — those docs are the source of truth,
not this file. Build once and read the relevant topics before improvising:

```
go build -o /tmp/sn .          # Go lives at ~/.local/go/bin
/tmp/sn docs headless          # bootstrap in envs without tmux/hooks (this one)
/tmp/sn docs cli               # the locked write CLI (edit/history/pins)
/tmp/sn docs monitor           # watch loop + self-edit suppression
/tmp/sn docs protocol          # board conventions (sections, replies, markers)
```

## Recipe (headless dev session)

1. **Bootstrap** — no hooks run here, so no board exists:
   `/tmp/sn init --board <dir>/dev.md --title "…"`
2. **Serve** (background task):
   `SESSION_NOTES_DIR=<dir> /tmp/sn serve` → dashboard on
   http://127.0.0.1:7080, the board at `/b/dev`. Remote sandboxes accept no
   inbound connections — a human cannot open this URL; say so instead of
   handing it out, and suggest they run `serve` on their own machine.
3. **Watch** (Monitor/background task):
   `/tmp/sn watch --board <dir>/dev.md --snapshot <snap>` prints a unified
   item diff per external change (add `--once` to exit after the first).
4. **Write** through the CLI only, with self-edit suppression so your own
   writes don't wake the watcher:
   `/tmp/sn edit reply "<query>" "claude: …" --board <dir>/dev.md --refresh-snapshot <snap>`
5. **Catch up** after compaction: `/tmp/sn history --board <dir>/dev.md`.

## Driving the web UI headlessly

Playwright is vendored for the e2e suite — reuse it instead of installing:

```js
import { chromium } from './test/e2e/node_modules/playwright-core/index.mjs';
const browser = await chromium.launch({
  executablePath: '/opt/pw-browsers/chromium',
  headless: true,
  args: ['--no-sandbox', '--no-proxy-server'],   // the session proxy breaks localhost otherwise
});
// phone/touch emulation, for the mobile UI paths:
const ctx = await browser.newContext({
  viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true,
});
```

Assert through the page's `window.__sn` debug handle (see `test/e2e/`), and
after changing anything under `internal/web/`, run the real suite:
`./test/e2e/run.sh` (per CLAUDE.md).
