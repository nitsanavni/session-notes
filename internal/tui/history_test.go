package tui

import "testing"

func TestHistoryUndoRedoRoundTrip(t *testing.T) {
	h := newHistory(100)

	// Two mutations: A -> B -> C. Snapshot captures the state *before* each.
	h.snapshot("A") // about to leave A for B
	h.snapshot("B") // about to leave B for C
	cur := "C"

	prev, ok := h.undoTo(cur)
	if !ok || prev != "B" {
		t.Fatalf("undo 1 = %q,%v want B,true", prev, ok)
	}
	cur = prev
	prev, ok = h.undoTo(cur)
	if !ok || prev != "A" {
		t.Fatalf("undo 2 = %q,%v want A,true", prev, ok)
	}
	cur = prev
	if _, ok := h.undoTo(cur); ok {
		t.Fatalf("undo 3 should be empty")
	}

	// Redo back up to C.
	next, ok := h.redoTo(cur)
	if !ok || next != "B" {
		t.Fatalf("redo 1 = %q,%v want B,true", next, ok)
	}
	cur = next
	next, ok = h.redoTo(cur)
	if !ok || next != "C" {
		t.Fatalf("redo 2 = %q,%v want C,true", next, ok)
	}
	cur = next
	if _, ok := h.redoTo(cur); ok {
		t.Fatalf("redo 3 should be empty")
	}
}

func TestHistorySnapshotTruncatesRedo(t *testing.T) {
	h := newHistory(100)
	h.snapshot("A")
	cur := "B"
	prev, _ := h.undoTo(cur) // back to A, redo now holds B
	cur = prev
	if len(h.redo) != 1 {
		t.Fatalf("redo len = %d want 1", len(h.redo))
	}
	// A fresh mutation from A must discard the redo future.
	h.snapshot("A")
	if len(h.redo) != 0 {
		t.Fatalf("snapshot did not truncate redo: len = %d", len(h.redo))
	}
	if _, ok := h.redoTo("X"); ok {
		t.Fatalf("redo should be empty after truncation")
	}
}

func TestHistoryBound(t *testing.T) {
	h := newHistory(3)
	for _, s := range []string{"s0", "s1", "s2", "s3", "s4"} {
		h.snapshot(s)
	}
	if len(h.undo) != 3 {
		t.Fatalf("undo len = %d want 3 (bounded)", len(h.undo))
	}
	// Only the three most recent survive: s2, s3, s4 (s4 is top).
	want := []string{"s4", "s3", "s2"}
	cur := "live"
	for i, w := range want {
		prev, ok := h.undoTo(cur)
		if !ok || prev != w {
			t.Fatalf("undo %d = %q,%v want %q", i, prev, ok, w)
		}
		cur = prev
	}
	if _, ok := h.undoTo(cur); ok {
		t.Fatalf("undo past bound should be empty (s0,s1 were dropped)")
	}
}

func TestHistoryRecordKeepsRedo(t *testing.T) {
	h := newHistory(100)
	h.snapshot("A")
	prev, _ := h.undoTo("B") // back to A; redo holds B
	cur := prev
	// External edit boundary: record the state we're leaving, without wiping redo.
	h.record(cur) // pushes A onto undo
	incoming := "E"
	if len(h.redo) != 1 {
		t.Fatalf("record cleared redo: len = %d want 1", len(h.redo))
	}
	// Undo after the external edit steps back to our pre-external state (A).
	prev, ok := h.undoTo(incoming)
	if !ok || prev != "A" {
		t.Fatalf("undo after record = %q,%v want A,true", prev, ok)
	}
}

func TestHistoryEmpty(t *testing.T) {
	h := newHistory(100)
	if _, ok := h.undoTo("x"); ok {
		t.Fatalf("empty undo should report not-ok")
	}
	if _, ok := h.redoTo("x"); ok {
		t.Fatalf("empty redo should report not-ok")
	}
}
