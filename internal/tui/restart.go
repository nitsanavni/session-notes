package tui

import (
	"os"
	"syscall"
	"time"
)

// This file implements in-place restart when the running executable is replaced
// on disk — the common "I just shipped a new binary but the open TUI still runs
// the old code" pain. On the periodic status tick the TUI re-stats its own
// executable; when the size or mtime changes it flags itself stale and shows an
// unobtrusive footer notice ("binary updated — U restarts"). Pressing U quits
// bubbletea cleanly and then syscall.Exec re-executes the new binary in place,
// preserving the board path (same argv) and the current view (SN_RESUME_VIEW).

// binStatFn / exePathFn are indirections so tests can inject a fake executable
// path and stat result without touching the real filesystem.
var (
	binStatFn = os.Stat
	exePathFn = os.Executable
)

// binState is the identity of the running executable on disk: its size and
// mtime. ok is false when the path could not be resolved or stat'd, which the
// caller treats as "don't know" and never as "changed" (so a transient missing
// file — e.g. mid-replace — does not nag).
type binState struct {
	size  int64
	mtime time.Time
	ok    bool
}

// statBinary resolves and stats the running executable. A failure at either
// step yields ok=false (with the resolved path when we got that far).
func statBinary() (path string, s binState) {
	path, err := exePathFn()
	if err != nil {
		return "", binState{}
	}
	fi, err := binStatFn(path)
	if err != nil {
		return path, binState{}
	}
	return path, binState{size: fi.Size(), mtime: fi.ModTime(), ok: true}
}

// baselineBinary records the executable's identity at startup. If we couldn't
// baseline it (ok=false), change detection stays disabled and the TUI never
// nags — guarding the "os.Executable fails / path deleted" cases in the sketch.
func (m *model) baselineBinary() {
	m.exePath, m.exeBase = statBinary()
}

// checkBinary re-stats the executable and flags the model stale when it differs
// from the startup baseline. No baseline (couldn't stat at startup) or a current
// stat failure (file deleted and not yet replaced) is ignored — we only flip to
// stale on a real, readable change.
func (m *model) checkBinary() {
	if m.binaryStale || !m.exeBase.ok || m.exePath == "" {
		return
	}
	_, cur := statBinary()
	if !cur.ok {
		return
	}
	if cur.size != m.exeBase.size || !cur.mtime.Equal(m.exeBase.mtime) {
		m.binaryStale = true
	}
}

// reexecEnv is the environment variable carrying the pre-restart view (outline
// or map) so the fresh process resumes where the user was.
const reexecEnv = "SN_RESUME_VIEW"

// reexec replaces the current process image with a fresh exec of the (updated)
// binary, preserving the original argv (board path, flags) and passing the
// current view through reexecEnv. It only returns on failure — on success the
// process is replaced. bubbletea must already have released the terminal (we
// call this after p.Run() returns).
func (m *model) reexec() error {
	path, err := exePathFn()
	if err != nil {
		return err
	}
	view := "outline"
	if m.mapView {
		view = "map"
	}
	env := append(os.Environ(), reexecEnv+"="+view)
	return syscall.Exec(path, os.Args, env)
}

// binaryNotice is the persistent footer line shown once the running binary has
// been replaced on disk, prompting the in-place restart. Empty until stale.
func (m *model) binaryNotice() string {
	if !m.binaryStale {
		return ""
	}
	return styleUrgent.Render("⟳ binary updated — U restarts")
}

// resumeView applies a SN_RESUME_VIEW hint left by a prior in-place restart,
// switching straight into the map view when that's where the user was. Called
// once after the board opens.
func (m *model) resumeView() {
	if os.Getenv(reexecEnv) == "map" {
		m.enterMap()
	}
	// Consume it so a later plain restart (or a child process) doesn't inherit it.
	_ = os.Unsetenv(reexecEnv)
}
