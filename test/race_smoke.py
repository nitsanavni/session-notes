#!/usr/bin/env python3
"""End-to-end pty smoke test for the lost-update race between the TUI and an
external writer (Claude).

Reproduces the reported bug: the user opens an inline input in the TUI and types
a new item; while that input is open, an external writer atomically replaces the
board file (adding a line); the user presses enter to commit. Before the fix the
TUI wrote its whole stale in-memory tree and clobbered the external line. With
advisory locking + rebase-before-save, BOTH the external line and the user's new
item must be present in the final file.

Requires pexpect (pip install pexpect). Builds its own binary via `go build`.
Run:  python3 test/race_smoke.py
"""

import os
import subprocess
import sys
import tempfile
import time

import pexpect

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
USER_ITEM = "user typed this during edit"
EXTERNAL_LINE = "- [ ] gamma written by claude"

BOARD = """\
---
session: smoke-test
cwd: /tmp
started: 2026-07-07T00:00:00Z
---

## Threads
- [ ] alpha
- [ ] beta

## Log
- 00:00 start: session started
"""


def go_bin():
    for cand in (
        os.path.expanduser("~/.local/go/bin/go"),
        "go",
    ):
        if cand == "go" or os.path.exists(cand):
            return cand
    return "go"


def build(tmp):
    binpath = os.path.join(tmp, "session-notes")
    env = dict(os.environ)
    env["PATH"] = os.path.expanduser("~/.local/go/bin") + os.pathsep + env.get("PATH", "")
    subprocess.run([go_bin(), "build", "-o", binpath, "."], cwd=REPO, check=True, env=env)
    return binpath


def external_write(path, contents):
    """Atomic replace: write a temp file in the same dir + rename over the board,
    matching the sidecar convention (and what Claude's writer does)."""
    d = os.path.dirname(path)
    fd, tmp = tempfile.mkstemp(dir=d, prefix=".ext-")
    with os.fdopen(fd, "w") as f:
        f.write(contents)
    os.replace(tmp, path)


def main():
    with tempfile.TemporaryDirectory() as tmp:
        binpath = build(tmp)
        board_path = os.path.join(tmp, "board.md")
        with open(board_path, "w") as f:
            f.write(BOARD)

        env = dict(os.environ)
        env["TERM"] = "xterm-256color"
        env["SESSION_NOTES_DIR"] = tmp

        child = pexpect.spawn(
            binpath, ["--board", board_path],
            env=env, dimensions=(30, 100), encoding="utf-8", timeout=10,
        )
        try:
            # Wait until the board's footer has rendered before sending keys.
            child.expect("a add", timeout=8)
            time.sleep(0.5)
            # Open the inline "add item" input on the current (first) section.
            child.send("a")
            # Confirm the input opened: the footer label switches to "add:".
            child.expect("add:", timeout=5)
            # Type the user's new item text.
            child.send(USER_ITEM)
            child.expect("typed this during edit", timeout=5)

            # --- The race: external writer replaces the board while input is open.
            external = BOARD.replace(
                "- [ ] beta\n", "- [ ] beta\n" + EXTERNAL_LINE + "\n"
            )
            external_write(board_path, external)
            time.sleep(0.5)

            # Commit the user's edit (this triggers the locked rebase-save).
            child.send("\r")
            time.sleep(0.8)
            # Quit. Don't hard-depend on a clean EOF — the save already happened
            # on enter; just give it a moment then tear the child down.
            child.send("q")
            try:
                child.expect(pexpect.EOF, timeout=3)
            except pexpect.TIMEOUT:
                pass
        finally:
            if child.isalive():
                child.terminate(force=True)

        with open(board_path) as f:
            final = f.read()

        ok = True
        if USER_ITEM not in final:
            print("FAIL: user's typed item was lost")
            ok = False
        if EXTERNAL_LINE not in final:
            print("FAIL: external (Claude) line was clobbered")
            ok = False

        print("----- final board -----")
        print(final)
        print("-----------------------")
        if ok:
            print("PASS: both the external line and the user's new item survived")
            return 0
        return 1


if __name__ == "__main__":
    sys.exit(main())
