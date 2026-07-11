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
   inbound connections, so the user cannot open this URL directly — publish
   it with an outbound tunnel instead (see "Exposing the board publicly").
3. **Watch** (Monitor/background task):
   `/tmp/sn watch --board <dir>/dev.md --snapshot <snap>` prints a unified
   item diff per external change (add `--once` to exit after the first).
4. **Write** through the CLI only, with self-edit suppression so your own
   writes don't wake the watcher:
   `/tmp/sn edit reply "<query>" "claude: …" --board <dir>/dev.md --refresh-snapshot <snap>`
5. **Catch up** after compaction: `/tmp/sn history --board <dir>/dev.md`.

## Exposing the board publicly (Claude Code on the web sandbox)

Verified 2026-07 from a Claude Code on the web session. The sandbox allows
**no inbound connections** and egress **only on ports 80/443**; all 443
traffic is transparently TLS-intercepted by Anthropic's egress gateway (its
CA is in the system store), and anything inside the TLS session that is not
real HTTP/WebSocket gets dropped. Consequences, so you don't re-litigate
them each session:

- **cloudflared** — dead: edge transport needs port 7844 (QUIC or TCP).
- **SSH tunnels** (pinggy, localhost.run, serveo, even on port 443) — dead:
  raw SSH is not TLS, the gateway kills it at the ClientHello.
- **tunnelmole** — dead: its service listens on port 8083.
- **ngrok** — dead: with `root_cas: host` (v2 config) the MITM cert is
  accepted, but its muxed protocol inside TLS is not HTTP and gets cut.
- **Microsoft dev tunnels** — WORKS: pure WebSocket over 443, built for
  corporate proxies.

Recipe (note `--noproxy '*'` for downloads the agent proxy would 403, and
unsetting the proxy env so devtunnel dials direct):

```
curl -sSL --noproxy '*' -o /tmp/devtunnel https://aka.ms/TunnelsCliDownload/linux-x64
chmod +x /tmp/devtunnel
env -u HTTPS_PROXY -u HTTP_PROXY -u https_proxy -u http_proxy \
  /tmp/devtunnel user login -g -d     # device code — user completes on their phone
env -u HTTPS_PROXY -u HTTP_PROXY -u https_proxy -u http_proxy \
  /tmp/devtunnel host -p 7080 --allow-anonymous
```

Prints `https://<id>-7080.<region>.devtunnels.ms` — hand that to the user
(first visit shows a one-tap interstitial). Caveats to state: anonymous
access means anyone with the URL can read/write the board, and both tunnel
and login live only as long as the session's container.

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
