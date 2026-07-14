package cloud

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nitsanavni/session-notes/internal/board"
)

func testServer(t *testing.T, insecure bool) (*Store, *httptest.Server) {
	t.Helper()
	s := newStore(t)
	_ = s.CreateBoard("b1", seed)
	ts := httptest.NewServer(NewServer(s, insecure).Handler())
	t.Cleanup(ts.Close)
	return s, ts
}

func TestAuth401WithoutToken(t *testing.T) {
	s, ts := testServer(t, false)
	tok, _ := s.CreateToken("t")

	resp, err := http.Get(ts.URL + "/api/board/b1/raw")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: status=%d want 401", resp.StatusCode)
	}

	req, _ := http.NewRequest("GET", ts.URL+"/api/board/b1/raw", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("with token: status=%d want 200", resp.StatusCode)
	}
}

func TestRemoteTreeEditRoundTrip(t *testing.T) {
	s, ts := testServer(t, false)
	tok, _ := s.CreateToken("t")
	tree := NewRemoteTree(ts.URL, "b1", tok)

	// Get whole board.
	n, err := tree.Get("", -1)
	if err != nil {
		t.Fatal(err)
	}
	if len(n.Children) == 0 {
		t.Fatal("empty board node")
	}

	// Apply an add and verify it landed server-side.
	if _, err := tree.Apply(board.Op{Name: "add", Section: "Threads", Text: "remote task"}); err != nil {
		t.Fatal(err)
	}
	content, _, _ := s.Get("b1")
	if !strings.Contains(content, "remote task") {
		t.Fatalf("edit not persisted:\n%s", content)
	}
}

func TestRemoteTreeCarveOutRefusedEndToEnd(t *testing.T) {
	s, ts := testServer(t, false)
	tok, _ := s.CreateToken("t")
	tree := NewRemoteTree(ts.URL, "b1", tok)

	// Two sibling subtrees.
	_, _ = tree.Apply(board.Op{Name: "add", Section: "Threads", Text: "sib-a"})
	_, _ = tree.Apply(board.Op{Name: "add", Section: "Threads", Text: "sib-b"})
	content, _, _ := s.Get("b1")
	b := board.Parse(content)
	th := b.Section("Threads")
	var aID, bID string
	for _, it := range th.Items {
		if strings.Contains(it.DisplayText(), "sib-a") {
			aID = it.ID
		}
		if strings.Contains(it.DisplayText(), "sib-b") {
			bID = it.ID
		}
	}
	if aID == "" || bID == "" {
		t.Fatal("could not find sibling ids")
	}
	// Rooted at sib-a, editing sib-b must be refused (server-side scope).
	_, err := tree.Apply(board.Op{Name: "status", ID: bID, Root: aID, Status: board.StatusDone})
	if err == nil {
		t.Fatal("expected carve-out refusal end-to-end")
	}
}

func TestRemoteWatchScoped(t *testing.T) {
	s, ts := testServer(t, false)
	tok, _ := s.CreateToken("t")
	tree := NewRemoteTree(ts.URL, "b1", tok)

	// Build two sibling subtrees, then watch only one.
	_, _ = tree.Apply(board.Op{Name: "add", Section: "Threads", Text: "watched"})
	_, _ = tree.Apply(board.Op{Name: "add", Section: "Threads", Text: "other"})
	content, _, _ := s.Get("b1")
	b := board.Parse(content)
	th := b.Section("Threads")
	var watchedID, otherID string
	for _, it := range th.Items {
		if strings.Contains(it.DisplayText(), "watched") {
			watchedID = it.ID
		}
		if strings.Contains(it.DisplayText(), "other") {
			otherID = it.ID
		}
	}

	ch, cancel, err := tree.Watch(watchedID)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	// Edit the OTHER subtree: no event should arrive.
	_, _ = tree.Apply(board.Op{Name: "reply", ID: otherID, Text: "noise"})
	select {
	case <-ch:
		t.Fatal("received event for out-of-scope edit")
	case <-time.After(400 * time.Millisecond):
	}

	// Edit the WATCHED subtree: event should arrive.
	_, _ = tree.Apply(board.Op{Name: "reply", ID: watchedID, Text: "signal"})
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("no event for in-scope edit")
	}
}

// TestRemoteWatchIgnoreAuthor verifies server-side self-edit suppression: an
// edit authored by the ignored name delivers NO event; an edit by another author
// fires.
func TestRemoteWatchIgnoreAuthor(t *testing.T) {
	s, ts := testServer(t, false)
	// On an authed server the author is the token subject, not client-supplied,
	// so each principal gets its own token: scout watches ignoring itself.
	scoutTok, _ := s.CreateToken("scout")
	otherTok, _ := s.CreateToken("other")
	scout := NewRemoteTree(ts.URL, "b1", scoutTok)
	other := NewRemoteTree(ts.URL, "b1", otherTok)

	ch, cancel, err := scout.WatchFiltered("", "scout")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	// Let the SSE handler subscribe before we write.
	time.Sleep(100 * time.Millisecond)

	// Own edit (author forced to token subject "scout"): suppressed, no wakeup.
	if _, err := scout.Apply(board.Op{Name: "add", Section: "Threads", Text: "mine"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ch:
		t.Fatal("own edit should be suppressed by --ignore-author")
	case <-time.After(500 * time.Millisecond):
	}

	// Another principal's edit: fires.
	if _, err := other.Apply(board.Op{Name: "add", Section: "Threads", Text: "theirs"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("edit by another author should fire an event")
	}
}

// TestForcedAuthorOverridesSpoof proves F1: on an authed server a client-supplied
// author is ignored and forced to the token subject, so an attacker cannot spoof
// author=<victim> to (a) slip past the victim's ignore-author self-suppression or
// (b) forge journal/audit attribution.
func TestForcedAuthorOverridesSpoof(t *testing.T) {
	s, ts := testServer(t, false)
	victimTok, _ := s.CreateToken("victim")
	attackerTok, _ := s.CreateToken("attacker")
	victim := NewRemoteTree(ts.URL, "b1", victimTok)
	attacker := NewRemoteTree(ts.URL, "b1", attackerTok)

	// Victim watches, ignoring its own edits.
	ch, cancel, err := victim.WatchFiltered("", "victim")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	time.Sleep(100 * time.Millisecond)

	// Attacker edits but claims author="victim" hoping to be suppressed.
	if _, err := attacker.Apply(board.Op{Name: "add", Section: "Threads", Text: "sneaky", Author: "victim"}); err != nil {
		t.Fatal(err)
	}
	// Suppression must NOT be gamed: the victim's watch still fires.
	var ev board.Event
	select {
	case ev = <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("spoofed author must not suppress the victim's watch")
	}
	// And the event attributes the change to the attacker's real subject.
	if ev.Author != "attacker" {
		t.Fatalf("event author=%q want attacker (spoof leaked or misattributed)", ev.Author)
	}

	// The journal records the attacker's real subject, never the spoofed name.
	authorsJournal, err := s.AuthorsSince("b1", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range authorsJournal {
		if a != "attacker" {
			t.Fatalf("journal author=%q want attacker (spoof leaked)", a)
		}
	}
	if len(authorsJournal) == 0 {
		t.Fatal("journal recorded no op")
	}
}

func TestConflict409OverHTTP(t *testing.T) {
	s, ts := testServer(t, true) // insecure: focus on conflict semantics
	_ = s
	base := 0
	req := editReq{Op: "add", Section: "Threads", Text: "x", Base: &base}
	post := func(r editReq) int {
		return postEdit(t, ts.URL, "b1", r)
	}
	// current version is 1, base 0 is stale -> 409
	if code := post(req); code != http.StatusConflict {
		t.Fatalf("stale base: status=%d want 409", code)
	}
	good := 1
	req.Base = &good
	if code := post(req); code != http.StatusOK {
		t.Fatalf("fresh base: status=%d want 200", code)
	}
}
