package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nitsanavni/session-notes/internal/board"
)

// TestCarveOutConcurrentSiblings proves two writers scoped to sibling subtrees
// via edit --root can edit concurrently without stepping on each other, and
// that neither can reach across its carve-out boundary. This is the M2
// permission story: hand a sub-agent one node and it cannot touch the rest.
func TestCarveOutConcurrentSiblings(t *testing.T) {
	dir := t.TempDir()
	boardPath := filepath.Join(dir, "board.md")
	content := "# board\n\n## Plan\n\n- [ ] alpha ^aaaaaa\n  - [ ] a1 ^aaaa11\n- [ ] beta ^bbbbbb\n  - [ ] b1 ^bbbb11\n"
	if err := os.WriteFile(boardPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	// Writer A owns the alpha subtree, writer B owns beta. Each does several
	// scoped edits concurrently under the shared board lock.
	edit := func(root string, ops [][2]string) {
		defer wg.Done()
		for _, op := range ops {
			editApplyOp(boardPath, "", root, board.Op{Name: "add", Text: op[1]})
		}
	}
	go edit("aaaaaa", [][2]string{{"a", "x1"}, {"a", "x2"}, {"a", "x3"}})
	go edit("bbbbbb", [][2]string{{"b", "y1"}, {"b", "y2"}, {"b", "y3"}})
	wg.Wait()

	got, err := os.ReadFile(boardPath)
	if err != nil {
		t.Fatal(err)
	}
	b := board.Parse(string(got))
	alpha := b.FindByID("aaaaaa")
	beta := b.FindByID("bbbbbb")
	// Each writer's three children landed under its own root, none escaped.
	if len(alpha.Children) != 4 { // a1 + x1,x2,x3
		t.Errorf("alpha should have 4 children (a1+3), got %d", len(alpha.Children))
	}
	if len(beta.Children) != 4 { // b1 + y1,y2,y3
		t.Errorf("beta should have 4 children (b1+3), got %d", len(beta.Children))
	}
	for _, c := range alpha.Children {
		if strings.HasPrefix(c.Text, "y") {
			t.Errorf("beta's child escaped into alpha: %q", c.Text)
		}
	}

	// The boundary is hard: alpha's writer cannot touch beta by id.
	rc := editApplyOp(boardPath, "", "aaaaaa", board.Op{Name: "edit", ID: "bbbbbb", Text: "pwned"})
	if rc == 0 {
		t.Error("cross-carve-out edit should have failed with nonzero exit")
	}
	after, _ := os.ReadFile(boardPath)
	if strings.Contains(string(after), "pwned") {
		t.Error("cross-carve-out edit mutated the board")
	}
}
