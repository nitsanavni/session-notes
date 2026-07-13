package cloud

import (
	"errors"
	"strings"
	"testing"

	"github.com/nitsanavni/session-notes/internal/board"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

const seed = "---\ntitle: T\n---\n\n## Threads\n\n- [ ] first task\n\n## Log\n"

func TestCreateAndGetRoundTrip(t *testing.T) {
	s := newStore(t)
	if err := s.CreateBoard("b1", seed); err != nil {
		t.Fatal(err)
	}
	content, version, err := s.Get("b1")
	if err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("version=%d want 1", version)
	}
	// EnsureIDs assigned node anchors on the way in.
	if !strings.Contains(content, "first task ^") {
		t.Fatalf("expected id anchor, got:\n%s", content)
	}
	if s.BoardExists("nope") {
		t.Fatal("nonexistent board reported existing")
	}
}

func TestApplyBumpsVersion(t *testing.T) {
	s := newStore(t)
	_ = s.CreateBoard("b1", seed)
	res, err := s.ApplyOp("b1", board.Op{Name: "add", Section: "Threads", Text: "second"}, -1, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if res.Version != 2 {
		t.Fatalf("version=%d want 2", res.Version)
	}
	content, version, _ := s.Get("b1")
	if version != 2 || !strings.Contains(content, "second") {
		t.Fatalf("apply not persisted: v=%d\n%s", version, content)
	}
	hist, _ := s.History("b1", 0, 0)
	if len(hist) != 1 || hist[0].Author != "tester" {
		t.Fatalf("history=%+v", hist)
	}
}

func TestApplyConflictOnStaleBase(t *testing.T) {
	s := newStore(t)
	_ = s.CreateBoard("b1", seed)
	// base 1 matches -> ok, version becomes 2.
	if _, err := s.ApplyOp("b1", board.Op{Name: "add", Section: "Threads", Text: "a"}, 1, "x"); err != nil {
		t.Fatal(err)
	}
	// base 1 is now stale (current is 2) -> conflict.
	_, err := s.ApplyOp("b1", board.Op{Name: "add", Section: "Threads", Text: "b"}, 1, "x")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
}

func TestApplyRootCarveOutRefusal(t *testing.T) {
	s := newStore(t)
	_ = s.CreateBoard("b1", seed)
	content, _, _ := s.Get("b1")
	b := board.Parse(content)
	// two sibling subtrees under Threads
	threads := b.Section("Threads")
	var firstID string
	for _, it := range threads.Items {
		if strings.Contains(it.DisplayText(), "first task") {
			firstID = it.ID
		}
	}
	if firstID == "" {
		t.Fatal("no id on first item")
	}
	// add a second sibling so we have an out-of-scope target
	_, _ = s.ApplyOp("b1", board.Op{Name: "add", Section: "Threads", Text: "sibling"}, -1, "x")
	content, _, _ = s.Get("b1")
	b = board.Parse(content)
	threads = b.Section("Threads")
	var siblingID string
	for _, it := range threads.Items {
		if strings.Contains(it.DisplayText(), "sibling") {
			siblingID = it.ID
		}
	}
	// An op rooted at firstID addressing siblingID must be refused.
	_, err := s.ApplyOp("b1", board.Op{Name: "status", ID: siblingID, Root: firstID, Status: board.StatusDone}, -1, "x")
	if err == nil {
		t.Fatal("expected out-of-scope refusal")
	}
}

func TestSetContentCreatesAndVersions(t *testing.T) {
	s := newStore(t)
	v, err := s.SetContent("new1", seed, "push")
	if err != nil {
		t.Fatal(err)
	}
	if v != 1 {
		t.Fatalf("v=%d want 1", v)
	}
	v, err = s.SetContent("new1", seed+"\n- [ ] more\n", "push")
	if err != nil {
		t.Fatal(err)
	}
	if v != 2 {
		t.Fatalf("v=%d want 2", v)
	}
}

func TestTokens(t *testing.T) {
	s := newStore(t)
	if s.HasTokens() {
		t.Fatal("fresh store should have no tokens")
	}
	tok, err := s.CreateToken("agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if !s.HasTokens() {
		t.Fatal("HasTokens false after create")
	}
	if !s.ValidToken(tok) {
		t.Fatal("minted token not valid")
	}
	if s.ValidToken("deadbeef") {
		t.Fatal("bogus token accepted")
	}
	if s.ValidToken("") {
		t.Fatal("empty token accepted")
	}
}
