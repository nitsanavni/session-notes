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
