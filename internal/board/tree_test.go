package board

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTreeBoard(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "b.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestFileTreeApplyAssignsIDsAndGet: Apply mutates through the op layer, stamps
// ids, and Get reads the subtree back by id.
func TestFileTreeApplyAssignsIDsAndGet(t *testing.T) {
	path := writeTreeBoard(t, "## Plan\n- [ ] a\n")
	tr := OpenFileTree(path)

	if _, err := tr.Apply(Op{Name: "add", Section: "Plan", Text: "b"}); err != nil {
		t.Fatal(err)
	}
	// Reload to discover the assigned ids.
	b, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	it, _ := b.FindByQuery("- [ ] b")
	if it == nil || it.ID == "" {
		t.Fatalf("added item has no id: %+v", it)
	}

	n, err := tr.Get(it.ID, -1)
	if err != nil {
		t.Fatal(err)
	}
	if n.ID != it.ID || n.Text != "b" || n.Status != StatusOpen {
		t.Fatalf("Get returned wrong node: %+v", n)
	}

	// Root Get returns all sections as children.
	root, err := tr.Get("", -1)
	if err != nil {
		t.Fatal(err)
	}
	if len(root.Children) != 1 || root.Children[0].Text != "Plan" {
		t.Fatalf("root children: %+v", root.Children)
	}
	if len(root.Children[0].Children) != 2 {
		t.Fatalf("expected 2 items under Plan, got %d", len(root.Children[0].Children))
	}

	// Get on an unknown id errors.
	if _, err := tr.Get("zzzz99", -1); err == nil {
		t.Fatal("expected error for unknown id")
	}
}

// TestFileTreeApplyByID: FileTree.Apply resolves an op by node id.
func TestFileTreeApplyByID(t *testing.T) {
	path := writeTreeBoard(t, "## Plan\n- [ ] a ^known1\n")
	tr := OpenFileTree(path)
	res, err := tr.Apply(Op{Name: "status", ID: "known1", Status: StatusDone})
	if err != nil {
		t.Fatal(err)
	}
	if res.ID != "known1" {
		t.Fatalf("result id: %q", res.ID)
	}
	b, _ := Load(path)
	if b.FindByID("known1").Status != StatusDone {
		t.Fatal("status not applied")
	}
}

// TestFileTreeGetDepth: depth limits how many descendant levels Get returns.
func TestFileTreeGetDepth(t *testing.T) {
	path := writeTreeBoard(t, "## Plan\n- [ ] a ^root01\n  - reply ^kid001\n    - deep ^gcd001\n")
	tr := OpenFileTree(path)
	n, err := tr.Get("root01", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(n.Children) != 1 {
		t.Fatalf("depth 1 children: %d", len(n.Children))
	}
	if len(n.Children[0].Children) != 0 {
		t.Fatal("depth 1 should not include grandchildren")
	}
	n0, _ := tr.Get("root01", 0)
	if len(n0.Children) != 0 {
		t.Fatal("depth 0 should have no children")
	}
}

// TestFileTreeWatchSubtree: Watch emits a coarse event only when the watched
// subtree changes, and stays quiet for edits elsewhere.
func TestFileTreeWatchSubtree(t *testing.T) {
	path := writeTreeBoard(t, "## Plan\n- [ ] a ^watch1\n- [ ] b ^other1\n")
	tr := OpenFileTree(path)
	tr.interval = 20 * time.Millisecond

	ch, cancel, err := tr.Watch("watch1")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	// Change a sibling outside the watched subtree: no event.
	if _, err := tr.Apply(Op{Name: "status", ID: "other1", Status: StatusDone}); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-ch:
		t.Fatalf("unexpected event for out-of-subtree change: %+v", ev)
	case <-time.After(150 * time.Millisecond):
	}

	// Change the watched item: event.
	if _, err := tr.Apply(Op{Name: "status", ID: "watch1", Status: StatusDone}); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-ch:
		if ev.NodeID != "watch1" {
			t.Fatalf("event node id: %q", ev.NodeID)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected event for watched-subtree change")
	}

	// cancel is idempotent.
	cancel()
	cancel()
}
