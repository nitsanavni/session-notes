package tui

// history is a bounded undo/redo stack of full board-file snapshots (each a
// rendered-markdown string). Snapshots are taken before each mutating action;
// undo restores the previous snapshot and redo re-applies it. Because boards are
// tiny, storing whole-file contents is the simple, obviously-correct approach.
type history struct {
	undo  []string
	redo  []string
	limit int
}

// newHistory returns a history bounded to at most limit undo (and limit redo)
// entries. A limit below 1 is clamped to 1.
func newHistory(limit int) *history {
	if limit < 1 {
		limit = 1
	}
	return &history{limit: limit}
}

// snapshot records cur as a restorable state taken just before a local mutation,
// and truncates the redo stack — standard behavior: a fresh edit invalidates any
// redo future.
func (h *history) snapshot(cur string) {
	h.undo = pushBounded(h.undo, cur, h.limit)
	h.redo = h.redo[:0]
}

// record pushes cur onto the undo stack without clearing the redo stack. It marks
// an external-edit boundary (an fsnotify reload of someone else's write) so undo
// steps back through that write instead of silently clobbering it, while leaving
// any pending redo future intact.
func (h *history) record(cur string) {
	h.undo = pushBounded(h.undo, cur, h.limit)
}

// undoTo returns the state to restore when undoing from cur, moving cur onto the
// redo stack. ok is false when there is nothing to undo.
func (h *history) undoTo(cur string) (prev string, ok bool) {
	if len(h.undo) == 0 {
		return "", false
	}
	prev = h.undo[len(h.undo)-1]
	h.undo = h.undo[:len(h.undo)-1]
	h.redo = pushBounded(h.redo, cur, h.limit)
	return prev, true
}

// redoTo returns the state to restore when redoing from cur, moving cur back onto
// the undo stack. ok is false when there is nothing to redo.
func (h *history) redoTo(cur string) (next string, ok bool) {
	if len(h.redo) == 0 {
		return "", false
	}
	next = h.redo[len(h.redo)-1]
	h.redo = h.redo[:len(h.redo)-1]
	h.undo = pushBounded(h.undo, cur, h.limit)
	return next, true
}

// pushBounded appends v to s, dropping the oldest entries so the result never
// exceeds limit.
func pushBounded(s []string, v string, limit int) []string {
	s = append(s, v)
	if len(s) > limit {
		s = append(s[:0], s[len(s)-limit:]...)
	}
	return s
}
