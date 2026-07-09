---
session: e2e-fixture-0001
cwd: /tmp/e2e-project
started: 2026-07-09T10:00:00Z
title: e2e fixture
---

## Waiting on User
- [ ] review the middleware PR @user

## Plan
- [x] extract middleware
- [>] wire sessions through it
- [ ] drop legacy endpoint

## Threads
- [>] auth refactor — extracting middleware
  - claude: extracted, tests green
- [x] fix flaky test

## Questions
- [ ] !! drop the legacy endpoint? @user
  - user: leaning yes
- [ ] !pin always run gofmt before committing

## Ideas
- cache invalidation could be event-driven
- see [[design-notes]] for the full sketch
- this is a deliberately very long idea that should overflow the truncation width so the wrap toggle and the map node truncation both have a visible, testable effect on the layout

## Log
- 10:00 start: session started in /tmp/e2e-project
