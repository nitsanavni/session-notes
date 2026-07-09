package board

import (
	"errors"
	"strings"
	"testing"
)

const opsBoard = `## Plan
- [ ] build the widget
- [ ] paint the widget

## Questions
- [ ] ship it? @user
  - user: leaning yes

## Log
`

func TestApplyAddressing(t *testing.T) {
	b := Parse(opsBoard)

	// Query addressing notes multiple matches and uses the first.
	note, err := Apply(b, Op{Name: "status", Query: "widget", Status: StatusDone})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(note, "2 items match") {
		t.Errorf("note: %q", note)
	}
	if !strings.Contains(b.Render(), "- [x] build the widget") {
		t.Error("first match not updated")
	}

	// Raw addressing with normalized-text fallback across cosmetic drift.
	if _, err := Apply(b, Op{Name: "urgent", Section: "Plan", Raw: "- [ ] build the widget", Urgent: true}); err != nil {
		t.Fatalf("normalized fallback: %v", err)
	}
	if !strings.Contains(b.Render(), "- [x] !! build the widget") {
		t.Error("urgent not set via stale raw")
	}

	// Missing target is ErrOpNotFound.
	if _, err := Apply(b, Op{Name: "delete", Query: "no such thing"}); !errors.Is(err, ErrOpNotFound) {
		t.Errorf("want ErrOpNotFound, got %v", err)
	}
}

func TestApplySemantics(t *testing.T) {
	b := Parse(opsBoard)

	// add honors a leading status marker.
	if _, err := Apply(b, Op{Name: "add", Section: "Plan", Text: "[>] ship the widget"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.Render(), "- [>] ship the widget") {
		t.Errorf("leading status not honored:\n%s", b.Render())
	}

	// reply on a reply stays flat; fork nests.
	if _, err := Apply(b, Op{Name: "reply", Query: "leaning yes", Text: "claude: shipping"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.Render(), "\n  - claude: shipping\n") {
		t.Errorf("reply not flat:\n%s", b.Render())
	}
	if _, err := Apply(b, Op{Name: "fork", Query: "leaning yes", Text: "claude: aside"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.Render(), "\n    - claude: aside\n") {
		t.Errorf("fork not nested:\n%s", b.Render())
	}

	// urgent is set-not-toggle: applying the same state twice is a no-op.
	before := b.Render()
	if _, err := Apply(b, Op{Name: "urgent", Query: "ship it?", Urgent: false}); err != nil {
		t.Fatal(err)
	}
	if b.Render() != before {
		t.Error("urgent false on non-urgent item changed the board")
	}

	// pin is set-not-toggle, like urgent: set it, idempotent re-set is a no-op,
	// then clear it.
	if _, err := Apply(b, Op{Name: "pin", Query: "ship it?", Pinned: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.Render(), "!pin ship it?") {
		t.Errorf("pin not set:\n%s", b.Render())
	}
	pinned := b.Render()
	if _, err := Apply(b, Op{Name: "pin", Query: "ship it?", Pinned: true}); err != nil {
		t.Fatal(err)
	}
	if b.Render() != pinned {
		t.Error("re-setting pin to the same state changed the board")
	}
	if _, err := Apply(b, Op{Name: "pin", Query: "ship it?", Pinned: false}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.Render(), "!pin") {
		t.Errorf("pin not cleared:\n%s", b.Render())
	}

	// Guarded and malformed ops are ErrOpInvalid.
	if _, err := Apply(b, Op{Name: "archive-section", Section: "Log"}); !errors.Is(err, ErrOpInvalid) {
		t.Errorf("archiving Log: want ErrOpInvalid, got %v", err)
	}
	if _, err := Apply(b, Op{Name: "frobnicate"}); !errors.Is(err, ErrOpInvalid) {
		t.Errorf("unknown op: want ErrOpInvalid, got %v", err)
	}
}
