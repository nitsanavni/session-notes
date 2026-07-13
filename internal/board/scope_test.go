package board

import (
	"errors"
	"strings"
	"testing"
)

// scoped board: two sibling subtrees under a section, each with a child.
func scopedBoard(t *testing.T) *Board {
	t.Helper()
	b := Parse("# Board\n\n## Plan\n\n- [ ] alpha ^aaaaaa\n  - [ ] alpha-child ^aaaach\n- [ ] beta ^bbbbbb\n  - [ ] beta-child ^bbbbch\n")
	return b
}

func TestScopeRefusesSiblingID(t *testing.T) {
	b := scopedBoard(t)
	// Editing beta while rooted at alpha must be refused.
	_, err := Apply(b, Op{Name: "edit", ID: "bbbbbb", Root: "aaaaaa", Text: "hacked"})
	if !errors.Is(err, ErrOpInvalid) {
		t.Fatalf("want ErrOpInvalid for out-of-scope id, got %v", err)
	}
	if it := b.FindByID("bbbbbb"); it.Text != "beta" {
		t.Fatalf("beta was mutated across the carve-out: %q", it.Text)
	}
}

func TestScopeAllowsInScopeID(t *testing.T) {
	b := scopedBoard(t)
	if _, err := Apply(b, Op{Name: "edit", ID: "aaaach", Root: "aaaaaa", Text: "renamed"}); err != nil {
		t.Fatalf("in-scope edit failed: %v", err)
	}
	if it := b.FindByID("aaaach"); it.Text != "renamed" {
		t.Fatalf("in-scope edit did not apply: %q", it.Text)
	}
}

func TestScopeQueryCannotReachSibling(t *testing.T) {
	b := scopedBoard(t)
	_, err := Apply(b, Op{Name: "edit", Query: "beta", Root: "aaaaaa", Text: "x"})
	if !errors.Is(err, ErrOpNotFound) {
		t.Fatalf("want ErrOpNotFound (sibling invisible to scoped query), got %v", err)
	}
}

func TestScopeAddDefaultsToRootNode(t *testing.T) {
	b := scopedBoard(t)
	if _, err := Apply(b, Op{Name: "add", Root: "aaaaaa", Text: "new child"}); err != nil {
		t.Fatalf("scoped add failed: %v", err)
	}
	root := b.FindByID("aaaaaa")
	found := false
	for _, c := range root.Children {
		if c.Text == "new child" {
			found = true
		}
	}
	if !found {
		t.Fatalf("add --root did not append a child to the root node")
	}
}

func TestScopeRefusesWholeBoardOps(t *testing.T) {
	b := scopedBoard(t)
	if _, err := Apply(b, Op{Name: "title", Root: "aaaaaa", Text: "nope"}); !errors.Is(err, ErrOpInvalid) {
		t.Fatalf("want ErrOpInvalid for title under item root, got %v", err)
	}
	if _, err := Apply(b, Op{Name: "add-section", Root: "aaaaaa", Text: "Sneaky"}); !errors.Is(err, ErrOpInvalid) {
		t.Fatalf("want ErrOpInvalid for add-section under root, got %v", err)
	}
}

func TestScopeUnknownRoot(t *testing.T) {
	b := scopedBoard(t)
	if _, err := Apply(b, Op{Name: "edit", ID: "aaaaaa", Root: "zzzzzz", Text: "x"}); !errors.Is(err, ErrOpNotFound) {
		t.Fatalf("want ErrOpNotFound for missing root, got %v", err)
	}
}

func TestSectionRootScope(t *testing.T) {
	b := Parse("# B\n\n## Plan ^planid\n\n- [ ] a ^aaaaaa\n\n## Other ^othrid\n\n- [ ] o ^oooooo\n")
	// alpha is in Plan; editing it rooted at the Plan section works.
	if _, err := Apply(b, Op{Name: "edit", ID: "aaaaaa", Root: "planid", Text: "a2"}); err != nil {
		t.Fatalf("in-section edit failed: %v", err)
	}
	// editing an item in Other while rooted at Plan is refused.
	if _, err := Apply(b, Op{Name: "edit", ID: "oooooo", Root: "planid", Text: "x"}); !errors.Is(err, ErrOpInvalid) {
		t.Fatalf("want ErrOpInvalid for cross-section edit, got %v", err)
	}
}

func TestInScopeHelper(t *testing.T) {
	b := scopedBoard(t)
	alpha := b.FindByID("aaaaaa")
	child := b.FindByID("aaaach")
	beta := b.FindByID("bbbbbb")
	if !b.InScope("aaaaaa", child) {
		t.Error("child should be in alpha scope")
	}
	if !b.InScope("aaaaaa", alpha) {
		t.Error("root itself should be in scope")
	}
	if b.InScope("aaaaaa", beta) {
		t.Error("sibling should not be in alpha scope")
	}
	if !b.InScope("", beta) {
		t.Error("empty root should scope whole board")
	}
}

func TestErrOutsideScopeMessage(t *testing.T) {
	err := errOutsideScope("abc123")
	if !strings.Contains(err.Error(), "abc123") {
		t.Errorf("scope error should name the root: %v", err)
	}
}
