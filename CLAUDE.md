# session-notes

Go TUI + Claude Code hooks for shared per-session markdown boards. See README.md
for usage and SPEC.md for behavior details.

## Workflow

- Commit and push early and often. When a coherent unit of work is done, commit
  it and push to origin — don't wait to be asked. Unrelated changes go in
  separate commits.

## Checks

Run before committing (CI enforces all three):

```
gofmt -l .        # must print nothing
go vet ./...
go test ./...
```

Go lives at `~/.local/go/bin` (not on PATH by default).

## Web UI e2e tests

`./test/e2e/run.sh` drives the web UI (`session-notes serve`) in a real
Chromium: builds the binary, seeds a fixture board in a temp dir, starts a
fresh server per test, and asserts through the page's `window.__sn` debug
handle and the board file itself. Run it after changing anything under
`internal/web/`. `SN_ONLY=<substring>` filters tests; failures dump a step
log + screenshot + board state into `test/e2e/artifacts/`. Needs node and a
Chromium (`SN_CHROME` overrides the default `/opt/pw-browsers/chromium`).
