#!/usr/bin/env bash
# Regenerate the golden .out fixtures by running the real mm `tree` renderer.
#
# These captured outputs are checked into the repo so the Go golden tests never
# invoke bun (CI has no bun). Run this only when intentionally re-baselining
# against a new mm version.
#
#   MM_DIR=~/code/mm ./gen.sh
#
# Requires bun and a checkout of mm (github.com/nitsanavni/mm).
set -euo pipefail
cd "$(dirname "$0")"
MM_DIR="${MM_DIR:-$HOME/code/mm}"
for md in *.md; do
  out="${md%.md}.out"
  bun "$MM_DIR/src/cli.ts" tree "$md" > "$out"
  echo "wrote $out"
done
