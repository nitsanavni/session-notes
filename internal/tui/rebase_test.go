package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nitsanavni/session-notes/internal/board"
)

// hasItem reports whether the section contains a top-level item with the given
// display text.
func hasItem(b *board.Board, section, text string) bool {
	s := b.Section(section)
	if s == nil {
		return false
	}
	for _, it := range s.Items {
		if it.IsItem() && it.DisplayText() == text {
			return true
		}
	}
	return false
}

func TestApplyOpEditReapplied(t *testing.T) {
	fresh := board.Parse("## Threads\n- [ ] alpha\n- [ ] beta from claude\n")
	msg := applyOp(fresh, pendingOp{typ: opEdit, section: "Threads", rawLine: "- [ ] alpha", payload: "alpha edited"})
	if !hasItem(fresh, "Threads", "alpha edited") {
		t.Errorf("edit not applied: %q", fresh.Render())
	}
	if !strings.Contains(fresh.Render(), "beta from claude") {
		t.Error("external item lost")
	}
	if !strings.Contains(msg, "reapplied") {
		t.Errorf("msg = %q", msg)
	}
}

// The edited item was deleted externally: the user's text must survive as a new
// item rather than being silently dropped.
func TestApplyOpEditTargetGoneKeepsUserText(t *testing.T) {
	fresh := board.Parse("## Threads\n- [ ] beta from claude\n")
	msg := applyOp(fresh, pendingOp{typ: opEdit, section: "Threads", rawLine: "- [ ] alpha", payload: "alpha edited"})
	if !hasItem(fresh, "Threads", "alpha edited") {
		t.Errorf("user text lost when target vanished: %q", fresh.Render())
	}
	if !strings.Contains(msg, "kept your text") {
		t.Errorf("msg = %q", msg)
	}
}

func TestApplyOpReplyTargetGoneKeepsUserText(t *testing.T) {
	fresh := board.Parse("## Threads\n- [ ] something else\n")
	applyOp(fresh, pendingOp{typ: opReply, section: "Threads", rawLine: "- [ ] alpha", payload: "my reply"})
	if !hasItem(fresh, "Threads", "user: my reply") {
		t.Errorf("reply text lost when parent vanished: %q", fresh.Render())
	}
}

func TestApplyOpAddAndLog(t *testing.T) {
	fresh := board.Parse("## Threads\n- [ ] t1\n\n## Log\n- 10:00 start: x\n")
	applyOp(fresh, pendingOp{typ: opAdd, section: "Threads", payload: "new item"})
	applyOp(fresh, pendingOp{typ: opLog, payload: "did a thing"})
	if !hasItem(fresh, "Threads", "new item") {
		t.Errorf("add lost: %q", fresh.Render())
	}
	if !strings.Contains(fresh.Render(), "user: did a thing") {
		t.Errorf("log lost: %q", fresh.Render())
	}
}

// TestSaveWithRebaseMergesExternalEdit drives the model path end to end: an
// external write lands between load and save; the user's edit and the external
// line must both be in the final file, and undo history records the external
// state.
func TestSaveWithRebaseMergesExternalEdit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "b.md")
	src := "## Threads\n- [ ] alpha\n- [ ] beta\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	m := newModel()
	if err := m.openBoard(path); err != nil {
		t.Fatal(err)
	}
	// User starts editing "alpha"; capture identity as handleInputKey would.
	target := m.board.Section("Threads").Items[0]
	op := pendingOp{typ: opEdit, section: "Threads", rawLine: target.Raw(), payload: "alpha edited"}
	m.snapshot()
	target.Text = "alpha edited"

	// External writer appends a line while the input was open.
	external := "## Threads\n- [ ] alpha\n- [ ] beta\n- [ ] gamma from claude\n"
	if err := os.WriteFile(path, []byte(external), 0o644); err != nil {
		t.Fatal(err)
	}

	m.saveWithRebase(op)

	final, _ := os.ReadFile(path)
	got := string(final)
	if !strings.Contains(got, "- [ ] alpha edited") {
		t.Errorf("user edit lost: %q", got)
	}
	if !strings.Contains(got, "- [ ] gamma from claude") {
		t.Errorf("external line clobbered: %q", got)
	}
	// lastDisk must equal what we wrote (no phantom rebase next save).
	if m.lastDisk != got {
		t.Errorf("lastDisk out of sync with disk")
	}
	// Undo steps back through the external write first.
	if prev, ok := m.hist.undoTo(m.currentContent()); !ok {
		t.Fatal("expected undo entry recording external state")
	} else if !strings.Contains(prev, "gamma from claude") || strings.Contains(prev, "alpha edited") {
		t.Errorf("first undo should reveal external write without our edit: %q", prev)
	}
}

// countOccur counts non-overlapping occurrences of sub in s.
func countOccur(s, sub string) int { return strings.Count(s, sub) }

// The user cycled alpha [ ]->[>]; meanwhile Claude flipped it to [x] on disk.
// The exact-raw key misses, the fuzzy text key hits, and the RESULTING status is
// set absolutely — not a relative re-cycle — so a double-apply is a no-op.
func TestApplyOpCycleFuzzyAbsolute(t *testing.T) {
	fresh := board.Parse("## Threads\n- [x] alpha\n")
	op := pendingOp{typ: opCycle, section: "Threads", rawLine: "- [ ] alpha", newStatus: board.StatusInProgress}
	applyOp(fresh, op)
	applyOp(fresh, op) // idempotent under double-apply
	it := fresh.Section("Threads").Items[0]
	if it.Status != board.StatusInProgress {
		t.Errorf("status = %v, want InProgress (absolute set, not re-cycled)", it.Status)
	}
	if countOccur(fresh.Render(), "alpha") != 1 {
		t.Errorf("item duplicated: %q", fresh.Render())
	}
	// Target genuinely gone -> no-op, no resurrection.
	gone := board.Parse("## Threads\n- [ ] beta\n")
	applyOp(gone, op)
	if strings.Contains(gone.Render(), "alpha") {
		t.Errorf("cycle resurrected a gone item: %q", gone.Render())
	}
}

// Urgent replays the resulting flag absolutely and hits through marker drift.
func TestApplyOpUrgentFuzzyAbsolute(t *testing.T) {
	fresh := board.Parse("## Threads\n- [x] alpha\n") // Claude flipped the marker
	op := pendingOp{typ: opUrgent, section: "Threads", rawLine: "- [ ] alpha", newUrgent: true}
	applyOp(fresh, op)
	applyOp(fresh, op)
	if !strings.Contains(fresh.Render(), "!! alpha") {
		t.Errorf("urgency not set: %q", fresh.Render())
	}
	if countOccur(fresh.Render(), "alpha") != 1 {
		t.Errorf("item duplicated: %q", fresh.Render())
	}
}

// Pin replays the resulting flag absolutely and hits through marker drift.
func TestApplyOpPinFuzzyAbsolute(t *testing.T) {
	fresh := board.Parse("## Threads\n- [x] alpha\n") // Claude flipped the marker
	op := pendingOp{typ: opPin, section: "Threads", rawLine: "- [ ] alpha", newPinned: true}
	applyOp(fresh, op)
	applyOp(fresh, op)
	if !strings.Contains(fresh.Render(), "!pin alpha") {
		t.Errorf("pin not set: %q", fresh.Render())
	}
	if countOccur(fresh.Render(), "alpha") != 1 {
		t.Errorf("item duplicated: %q", fresh.Render())
	}
	// Unpin replays absolutely too: the marker comes back off.
	off := pendingOp{typ: opPin, section: "Threads", rawLine: "- [x] !pin alpha", newPinned: false}
	applyOp(fresh, off)
	if strings.Contains(fresh.Render(), "!pin") {
		t.Errorf("pin not cleared: %q", fresh.Render())
	}
	// Target genuinely gone -> no-op, no resurrection.
	gone := board.Parse("## Threads\n- [ ] beta\n")
	applyOp(gone, op)
	if strings.Contains(gone.Render(), "alpha") {
		t.Errorf("pin resurrected a gone item: %q", gone.Render())
	}
}

// Archive acts when the target is present (exact or single-fuzzy) and is a clean
// no-op when it is gone — no resurrection, no spurious Archive section.
func TestApplyOpArchiveItem(t *testing.T) {
	present := board.Parse("## Threads\n- [x] alpha\n- [ ] beta\n") // marker drift on alpha
	applyOp(present, pendingOp{typ: opArchiveItem, section: "Threads", rawLine: "- [ ] alpha"})
	if hasItem(present, "Threads", "alpha") {
		t.Errorf("alpha not archived out of Threads: %q", present.Render())
	}
	if present.Section("Archive") == nil || !strings.Contains(present.Render(), "alpha") {
		t.Errorf("archived content lost: %q", present.Render())
	}

	gone := board.Parse("## Threads\n- [ ] beta from claude\n")
	applyOp(gone, pendingOp{typ: opArchiveItem, section: "Threads", rawLine: "- [ ] alpha"})
	if strings.Contains(gone.Render(), "alpha") {
		t.Errorf("archive resurrected a gone item: %q", gone.Render())
	}
	if gone.Section("Archive") != nil {
		t.Errorf("spurious Archive section created: %q", gone.Render())
	}
}

// Delete is EXACT-RAW only: a marker-drifted (fuzzy-matchable) or reworded line
// an external writer touched is never hard-deleted.
func TestApplyOpDeleteItemExactOnly(t *testing.T) {
	drift := board.Parse("## Threads\n- [x] alpha\n") // fuzzy WOULD match, delete must not
	applyOp(drift, pendingOp{typ: opDeleteItem, section: "Threads", rawLine: "- [ ] alpha"})
	if !strings.Contains(drift.Render(), "alpha") {
		t.Errorf("exact-only delete removed a drifted line: %q", drift.Render())
	}

	reworded := board.Parse("## Threads\n- [ ] alpha reworded by claude\n")
	applyOp(reworded, pendingOp{typ: opDeleteItem, section: "Threads", rawLine: "- [ ] alpha"})
	if !strings.Contains(reworded.Render(), "alpha reworded by claude") {
		t.Errorf("exact-only delete removed a reworded line: %q", reworded.Render())
	}

	exact := board.Parse("## Threads\n- [ ] alpha\n- [ ] beta\n")
	applyOp(exact, pendingOp{typ: opDeleteItem, section: "Threads", rawLine: "- [ ] alpha"})
	if strings.Contains(exact.Render(), "alpha") {
		t.Errorf("exact delete did not remove target: %q", exact.Render())
	}
	if !strings.Contains(exact.Render(), "beta") {
		t.Errorf("exact delete took a bystander: %q", exact.Render())
	}
}

// Add-section is idempotent (existing titles not duplicated) and applies every
// title in a multi-title op.
func TestApplyOpAddSection(t *testing.T) {
	fresh := board.Parse("## Plan\n- [ ] x\n\n## Threads\n- [ ] y\n")
	applyOp(fresh, pendingOp{typ: opAddSection, sections: []string{"Threads", "Questions"}})
	if countOccur(fresh.Render(), "## Threads") != 1 {
		t.Errorf("existing section duplicated: %q", fresh.Render())
	}
	if fresh.Section("Questions") == nil {
		t.Errorf("new section not added: %q", fresh.Render())
	}
	// Idempotent under double-apply.
	applyOp(fresh, pendingOp{typ: opAddSection, sections: []string{"Questions"}})
	if countOccur(fresh.Render(), "## Questions") != 1 {
		t.Errorf("section duplicated on replay: %q", fresh.Render())
	}
}

// Archive / delete of a whole section: acted on when present, no-op when gone.
func TestApplyOpSectionOps(t *testing.T) {
	b := board.Parse("## Threads\n- [ ] a\n\n## Ideas\n- foo\n\n## Plan\n- [ ] p\n")
	applyOp(b, pendingOp{typ: opArchiveSection, section: "Ideas"})
	if b.Section("Ideas") != nil {
		t.Errorf("section not archived: %q", b.Render())
	}
	if b.Section("Archive") == nil || !strings.Contains(b.Render(), "foo") {
		t.Errorf("archived section content lost: %q", b.Render())
	}
	// Gone section: no-op, no panic.
	applyOp(b, pendingOp{typ: opArchiveSection, section: "Ideas"})

	applyOp(b, pendingOp{typ: opDeleteSection, section: "Plan"})
	if b.Section("Plan") != nil {
		t.Errorf("section not deleted: %q", b.Render())
	}
	if strings.Contains(b.Render(), "- [ ] p") {
		t.Errorf("deleted section content survived: %q", b.Render())
	}
	// Gone section: no-op, no panic.
	applyOp(b, pendingOp{typ: opDeleteSection, section: "Plan"})
}

// End-to-end through the model: an external writer flips alpha's marker between
// load and save; the user's cycle result is set absolutely on the rebased tree.
func TestSaveWithRebaseCycleAbsolute(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "b.md")
	if err := os.WriteFile(path, []byte("## Threads\n- [ ] alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := newModel()
	if err := m.openBoard(path); err != nil {
		t.Fatal(err)
	}
	it := m.board.Section("Threads").Items[0]
	op := pendingOp{typ: opCycle, section: "Threads", rawLine: it.Raw()}
	m.snapshot()
	it.CycleStatus() // [ ] -> [>]
	op.newStatus = it.Status

	// External writer flips alpha to done while the gesture was in flight.
	if err := os.WriteFile(path, []byte("## Threads\n- [x] alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.saveWithRebase(op)

	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "- [>] alpha") {
		t.Errorf("cycle not absolute-set on rebase (want [>]): %q", string(got))
	}
	if m.lastDisk != string(got) {
		t.Errorf("lastDisk out of sync with disk")
	}
}

// End-to-end: a stale delete whose target Claude reworded on disk is a no-op —
// the reworded line survives (no accidental hard delete).
func TestSaveWithRebaseDeleteExactOnlyNoOp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "b.md")
	if err := os.WriteFile(path, []byte("## Threads\n- [ ] alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := newModel()
	if err := m.openBoard(path); err != nil {
		t.Fatal(err)
	}
	it := m.board.Section("Threads").Items[0]
	op := pendingOp{typ: opDeleteItem, section: "Threads", rawLine: it.Raw()}
	m.snapshot()
	m.board.Remove(it)

	// Claude reworded alpha on disk before we save.
	if err := os.WriteFile(path, []byte("## Threads\n- [ ] alpha reworded by claude\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.saveWithRebase(op)

	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "alpha reworded by claude") {
		t.Errorf("stale delete clobbered a reworded line: %q", string(got))
	}
}
