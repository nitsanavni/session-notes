package board

import (
	"strings"
	"testing"
)

// TestAnchorParseStrip verifies a trailing " ^id" is parsed into Item.ID /
// Section.ID and stripped from the display text, while a caret elsewhere on the
// line stays plain text.
func TestAnchorParseStrip(t *testing.T) {
	src := "## Threads ^s1a2b3\n- [ ] ship the thing ^k3v9q2\n- [x] !! urgent one ^abc123\n- has ^caret in middle\n"
	b := Parse(src)
	sec := b.Sections[0]
	if sec.Title != "Threads" || sec.ID != "s1a2b3" {
		t.Fatalf("section: title=%q id=%q", sec.Title, sec.ID)
	}
	it0 := sec.Items[0]
	if it0.Text != "ship the thing" || it0.ID != "k3v9q2" || it0.Status != StatusOpen {
		t.Fatalf("it0: text=%q id=%q status=%v", it0.Text, it0.ID, it0.Status)
	}
	it1 := sec.Items[1]
	if it1.Text != "urgent one" || it1.ID != "abc123" || !it1.Urgent || it1.Status != StatusDone {
		t.Fatalf("it1: text=%q id=%q urgent=%v", it1.Text, it1.ID, it1.Urgent)
	}
	it2 := sec.Items[2]
	if it2.ID != "" || it2.Text != "has ^caret in middle" {
		t.Fatalf("mid-line caret should be plain text: id=%q text=%q", it2.ID, it2.Text)
	}
}

// TestAnchorRoundTrip: a board with anchors round-trips byte-identically, and a
// board without anchors is unchanged by Parse/Render (no churn until a save).
func TestAnchorRoundTrip(t *testing.T) {
	withAnchors := "## Threads ^s1a2b3\n- [ ] a ^k3v9q2\n  - user: hi ^m1n2o3\n- [x] b ^p4q5r6\n"
	if got := Parse(withAnchors).Render(); got != withAnchors {
		t.Fatalf("anchored round-trip:\nwant %q\ngot  %q", withAnchors, got)
	}
	noAnchors := "## Threads\n- [ ] a\n  - user: hi\n- [x] b\n"
	if got := Parse(noAnchors).Render(); got != noAnchors {
		t.Fatalf("plain round-trip changed bytes:\nwant %q\ngot  %q", noAnchors, got)
	}
}

// TestEnsureIDsAssignsAll: EnsureIDs stamps every section and recognized item
// lacking an id, leaves existing ids untouched, and skips continuation lines.
func TestEnsureIDsAssignsAll(t *testing.T) {
	src := "## Plan\n- [ ] keep ^fixed1\n- [ ] fresh\n  - reply\n  not a bullet\n"
	b := Parse(src)
	if !b.EnsureIDs() {
		t.Fatal("EnsureIDs reported no change")
	}
	sec := b.Sections[0]
	if sec.ID == "" {
		t.Fatal("section got no id")
	}
	if sec.Items[0].ID != "fixed1" {
		t.Fatalf("existing id changed: %q", sec.Items[0].ID)
	}
	fresh := sec.Items[1]
	if fresh.ID == "" {
		t.Fatal("fresh item got no id")
	}
	if fresh.Children[0].ID == "" {
		t.Fatal("reply got no id")
	}
	// The "not a bullet" continuation line is a second child; it must stay id-less.
	cont := fresh.Children[1]
	if cont.IsItem() || cont.ID != "" {
		t.Fatalf("continuation got id: parsed=%v id=%q", cont.IsItem(), cont.ID)
	}
	// Idempotent: a second call assigns nothing.
	if b.EnsureIDs() {
		t.Fatal("EnsureIDs should be idempotent")
	}
	// All ids are unique and 6 base36 chars.
	seen := map[string]bool{}
	for _, id := range []string{sec.ID, fresh.ID, fresh.Children[0].ID} {
		if len(id) != idLen {
			t.Fatalf("id %q not %d chars", id, idLen)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}

// TestGenIDCollision: genID never returns an id already in the used set.
func TestGenIDCollision(t *testing.T) {
	used := map[string]bool{}
	for i := 0; i < 2000; i++ {
		id := genID(used)
		if strings.ContainsFunc(id, func(r rune) bool { return !strings.ContainsRune(idAlphabet, r) }) {
			t.Fatalf("id %q has non-base36 chars", id)
		}
	}
	if len(used) != 2000 {
		t.Fatalf("expected 2000 unique ids, got %d", len(used))
	}
}

// TestEnsureIDsIdempotentRender: after the first id-assigning render, re-parsing
// and re-rendering is a fixpoint (stable bytes).
func TestEnsureIDsIdempotentRender(t *testing.T) {
	b := Parse("## Plan\n- [ ] a\n- [ ] !! b\n")
	b.EnsureIDs()
	once := b.Render()
	twice := Parse(once).Render()
	if once != twice {
		t.Fatalf("render not a fixpoint:\n%q\n%q", once, twice)
	}
	if !strings.Contains(once, " ^") {
		t.Fatalf("expected anchors in rendered output:\n%s", once)
	}
}

// TestFindByIDAndApply: an op addressed by ID resolves to the right item.
func TestApplyByID(t *testing.T) {
	b := Parse("## Plan\n- [ ] target ^tgt001\n- [ ] other ^oth002\n")
	if _, err := Apply(b, Op{Name: "status", ID: "tgt001", Status: StatusDone}); err != nil {
		t.Fatal(err)
	}
	it := b.FindByID("tgt001")
	if it == nil || it.Status != StatusDone {
		t.Fatalf("status not applied by id: %+v", it)
	}
	if b.FindByID("oth002").Status != StatusOpen {
		t.Fatal("wrong item mutated")
	}
	// Missing id is a not-found error.
	if _, err := Apply(b, Op{Name: "status", ID: "nope99", Status: StatusDone}); err == nil {
		t.Fatal("expected not-found for unknown id")
	}
	// ID addressing wins over Section+Raw when both are set.
	if _, err := Apply(b, Op{Name: "edit", ID: "oth002", Section: "Plan", Raw: "- [ ] target ^tgt001", Text: "renamed"}); err != nil {
		t.Fatal(err)
	}
	if b.FindByID("oth002").Text != "renamed" {
		t.Fatal("id addressing did not win over raw")
	}
}
