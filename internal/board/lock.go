package board

import (
	"os"

	"golang.org/x/sys/unix"
)

// LockPath returns the sidecar lock file path for a board: "<board>.lock".
//
// The lock is a separate zero-byte file, never the board itself, because the
// board is replaced by atomic rename (a new inode each save) — an flock on the
// board inode would not survive the rename and would not exclude a writer that
// races between open and rename. A stable sidecar inode does.
func LockPath(boardPath string) string { return boardPath + ".lock" }

// WithLock runs fn while holding an exclusive advisory flock on the board's
// sidecar lock file (LockPath(path)). It is the single serialization point for
// all board writers — the TUI's Save/SaveRebasing and the hooks — and the same
// convention any external writer (Claude) must follow: flock "<board>.lock"
// around its whole read → modify → atomic-rename.
//
// The lock is released (and the fd closed) when fn returns. WithLock must not be
// nested for the same path within one process: flock associates the lock with
// the open file description, so a second WithLock on a fresh fd would block on
// the first and deadlock. Callers that need read+merge+write under one lock do
// the whole thing inside a single WithLock (see SaveRebasing) rather than
// wrapping another lock-taking call.
func WithLock(path string, fn func() error) error {
	f, err := os.OpenFile(LockPath(path), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		return err
	}
	// Best-effort unlock; closing the fd also drops the lock, so a failure here
	// is not fatal.
	defer func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }()
	return fn()
}
