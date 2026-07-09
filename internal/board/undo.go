package board

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

// The undo journal gives a board ONE undo timeline shared by every client
// that writes through the locked edit path — the web UI, the edit CLI (so
// Claude can `session-notes edit undo`), and any future adopter. It lives in
// a sidecar next to the board (like <board>.feedback.jsonl) and is only ever
// read or written inside the board's advisory lock, so entries can never
// interleave with a concurrent edit.
//
// Undo applies only when the board still reads exactly the state the
// journaled edit left behind: an intervening write from a client that does
// not journal (today: the TUI, the hooks, a human in $EDITOR) surfaces as
// ErrUndoConflict instead of being silently destroyed. This is the same
// refuse-don't-clobber rule the web UI shipped with, promoted to the shared
// layer.

// UndoEntry is one journaled edit: the full board content on both sides.
// Boards are small (a screenful of markdown), so whole-content snapshots buy
// perfect fidelity for the cost of a few KB.
type UndoEntry struct {
	Time   string `json:"ts"`
	Author string `json:"author"` // "web", "claude", ...
	Before string `json:"before"`
	After  string `json:"after"`
}

// undoJournal is the sidecar's whole content.
type undoJournal struct {
	Undo []UndoEntry `json:"undo"`
	Redo []UndoEntry `json:"redo"`
}

// undoLimit bounds the undo stack, mirroring the TUI's in-memory depth.
const undoLimit = 100

var (
	// ErrUndoEmpty means there is nothing to undo (or redo).
	ErrUndoEmpty = errors.New("nothing to undo")
	// ErrUndoConflict means the board changed since the journaled edit — some
	// writer that does not journal (TUI, $EDITOR, a hook) wrote in between —
	// so applying the entry would destroy that write.
	ErrUndoConflict = errors.New("the board changed since the last journaled edit; undo/redo refused")
)

// UndoPath returns the journal sidecar path for a board.
func UndoPath(boardPath string) string { return boardPath + ".undo.json" }

func loadJournal(path string) *undoJournal {
	j := &undoJournal{}
	data, err := os.ReadFile(UndoPath(path))
	if err != nil {
		return j
	}
	// A corrupt journal degrades to an empty one — undo history is a
	// convenience, never worth failing a board edit over.
	_ = json.Unmarshal(data, j)
	return j
}

func saveJournal(path string, j *undoJournal) error {
	data, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return writeAtomicRaw(UndoPath(path), string(data))
}

// EditUnderLockJournaled is EditUnderLock plus journaling: a successful,
// content-changing transform appends an undo entry (authored for future
// display) and clears the redo line. Journal failures are swallowed — a
// board write must never fail because its history couldn't be recorded.
func EditUnderLockJournaled(path, snapshot, author string, transform func(content string) (string, error)) error {
	return EditUnderLock(path, snapshot, func(content string) (string, error) {
		out, err := transform(content)
		if err != nil {
			return "", err
		}
		if out != content {
			j := loadJournal(path)
			j.Undo = append(j.Undo, UndoEntry{
				Time: time.Now().Format(time.RFC3339), Author: author,
				Before: content, After: out,
			})
			if len(j.Undo) > undoLimit {
				j.Undo = j.Undo[len(j.Undo)-undoLimit:]
			}
			j.Redo = nil
			_ = saveJournal(path, j)
		}
		return out, nil
	})
}

// Undo reverts the most recent journaled edit, moving it to the redo stack.
func Undo(path string) error {
	return timeTravel(path, func(j *undoJournal) (*[]UndoEntry, *[]UndoEntry) { return &j.Undo, &j.Redo },
		func(e UndoEntry) (expect, restore string) { return e.After, e.Before })
}

// Redo re-applies the most recently undone edit, moving it back to undo.
func Redo(path string) error {
	return timeTravel(path, func(j *undoJournal) (*[]UndoEntry, *[]UndoEntry) { return &j.Redo, &j.Undo },
		func(e UndoEntry) (expect, restore string) { return e.Before, e.After })
}

// timeTravel pops the top of `from`, verifies the board still reads `expect`,
// writes `restore`, and lands the entry on `to` — all inside the board lock,
// so undo and redo are the same walk in opposite directions.
func timeTravel(path string, pick func(*undoJournal) (from, to *[]UndoEntry), sides func(UndoEntry) (expect, restore string)) error {
	return WithLock(path, func() error {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		j := loadJournal(path)
		from, to := pick(j)
		if len(*from) == 0 {
			return ErrUndoEmpty
		}
		entry := (*from)[len(*from)-1]
		expect, restore := sides(entry)
		if string(data) != expect {
			return ErrUndoConflict
		}
		if err := writeAtomicRaw(path, restore); err != nil {
			return err
		}
		*from = (*from)[:len(*from)-1]
		*to = append(*to, entry)
		if err := saveJournal(path, j); err != nil {
			return fmt.Errorf("board restored but journal not saved: %w", err)
		}
		return nil
	})
}

// UndoDepths reports how many entries each stack holds (for UI affordances).
// Reads without the lock: the journal is written atomically, so a read is
// torn-free, and a stale count only mis-lights a button.
func UndoDepths(path string) (undo, redo int) {
	j := loadJournal(path)
	return len(j.Undo), len(j.Redo)
}
