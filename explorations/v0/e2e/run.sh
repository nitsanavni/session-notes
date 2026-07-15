#!/usr/bin/env bash
# Build the v0 server once, then drive it in a real Chromium via playwright-core.
# Each scenario boots its own server on a free port with a fresh temp -data dir.
#
#   ./run.sh                 # all scenarios
#   V0_ONLY=quote ./run.sh   # filter by substring
#   V0_HEADED=1 ./run.sh     # watch the browser
set -euo pipefail
cd "$(dirname "$0")"

GO="${V0_GO:-}"
if [ -z "$GO" ]; then
  if [ -x "$HOME/.local/go/bin/go" ]; then GO="$HOME/.local/go/bin/go"
  elif command -v go >/dev/null 2>&1; then GO="$(command -v go)"
  else echo "go not found (set V0_GO)" >&2; exit 1; fi
fi
export V0_GO="$GO"

if [ ! -d node_modules ]; then
  echo "installing playwright-core..."
  npm install --no-audit --no-fund >/dev/null
fi

# Prebuild the server so each per-test boot is a fast exec, not `go run`.
BIN="$(mktemp -d)/v0-server"
echo "building server -> $BIN"
"$GO" build -o "$BIN" ..
export V0_BIN="$BIN"

node scenarios.js
