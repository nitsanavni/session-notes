#!/usr/bin/env bash
# Browser e2e tests for the session-notes web UI.
#
#   ./test/e2e/run.sh                 # everything
#   SN_ONLY=undo ./test/e2e/run.sh    # tests whose name contains "undo"
#   SN_HEADED=1 ./test/e2e/run.sh     # watch the browser
#
# Needs node and a Chromium (defaults to the Playwright-managed one at
# /opt/pw-browsers/chromium; override with SN_CHROME=/path/to/chrome).
# Failures dump a step log + screenshot + board state into test/e2e/artifacts/.
set -euo pipefail
cd "$(dirname "$0")"

GO="${GO:-$(command -v go || echo "$HOME/.local/go/bin/go")}"
BIN="${SN_BIN:-$PWD/session-notes-e2e-bin}"

echo "building $BIN"
(cd ../.. && "$GO" build -o "$BIN" .)

if [ ! -d node_modules ]; then
  echo "installing playwright-core (first run only)"
  npm install --no-fund --no-audit --loglevel=error
fi

rm -rf artifacts
SN_BIN="$BIN" exec node scenarios.js
