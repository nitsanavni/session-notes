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
