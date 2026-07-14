package cloud

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nitsanavni/session-notes/internal/board"
)

// threadsID returns the node id assigned to the "Threads" section of board b1.
func threadsID(t *testing.T, s *Store) string {
	t.Helper()
	content, _, _ := s.Get("b1")
	sec := board.Parse(content).Section("Threads")
	if sec == nil || sec.ID == "" {
		t.Fatal("no Threads section id")
	}
	return sec.ID
}

func TestGrantsCRUD(t *testing.T) {
	s := newStore(t)
	_ = s.CreateBoard("b1", seed)

	if _, ok := s.GrantFor("alice", "b1"); ok {
		t.Fatal("unexpected grant before add")
	}
	if err := s.AddGrant("alice", "b1", "", "read"); err != nil {
		t.Fatal(err)
	}
	g, ok := s.GrantFor("alice", "b1")
	if !ok || g.Perm != "read" {
		t.Fatalf("grant = %+v ok=%v", g, ok)
	}
	// Upsert: re-grant tightens/broadens in place, one row per (subject,board).
	if err := s.AddGrant("alice", "b1", "n1", "write"); err != nil {
		t.Fatal(err)
	}
	g, _ = s.GrantFor("alice", "b1")
	if g.Perm != "write" || g.Root != "n1" {
		t.Fatalf("after upsert grant = %+v", g)
	}
	list, _ := s.ListGrants("b1")
	if len(list) != 1 {
		t.Fatalf("expected 1 grant, got %d", len(list))
	}
	if err := s.AddGrant("alice", "b1", "", "bogus"); err == nil {
		t.Fatal("expected unknown-perm error")
	}
	if err := s.RemoveGrant("alice", "b1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.GrantFor("alice", "b1"); ok {
		t.Fatal("grant survived revoke")
	}
}

func TestTokenIdentity(t *testing.T) {
	s := newStore(t)
	admin, _ := s.CreateToken("boss") // admin bootstrap
	scoped, _ := s.CreateTokenAs("agent", "agent", false)

	if id, ok := s.TokenIdentity(admin); !ok || !id.Admin || id.Subject != "boss" {
		t.Fatalf("admin identity = %+v ok=%v", id, ok)
	}
	if id, ok := s.TokenIdentity(scoped); !ok || id.Admin || id.Subject != "agent" {
		t.Fatalf("scoped identity = %+v ok=%v", id, ok)
	}
	if _, ok := s.TokenIdentity("garbage"); ok {
		t.Fatal("garbage token validated")
	}
	if sub, ok := s.SubjectForTokenName("agent"); !ok || sub != "agent" {
		t.Fatalf("SubjectForTokenName = %q ok=%v", sub, ok)
	}
}

// get issues an authenticated request and returns the status code.
func authGet(t *testing.T, url, token string) int {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// TestPermMatrix drives the none/read/write-scoped/admin identities against the
// GET/POST/SSE surfaces and asserts the enforcement grid.
func TestPermMatrix(t *testing.T) {
	s, ts := testServer(t, false)
	root := threadsID(t, s)

	admin, _ := s.CreateToken("boss")
	noneTok, _ := s.CreateTokenAs("nobody", "nobody", false)
	readTok, _ := s.CreateTokenAs("reader", "reader", false)
	_ = s.AddGrant("reader", "b1", "", "read")
	writeTok, _ := s.CreateTokenAs("writer", "writer", false)
	_ = s.AddGrant("writer", "b1", root, "write") // subtree-scoped write

	board := ts.URL + "/api/board/b1"

	// GET board.
	cases := []struct {
		name, tok string
		want      int
	}{
		{"none", noneTok, http.StatusNotFound},
		{"read", readTok, http.StatusOK},
		{"write", writeTok, http.StatusOK},
		{"admin", admin, http.StatusOK},
	}
	for _, c := range cases {
		if got := authGet(t, board, c.tok); got != c.want {
			t.Errorf("GET board %s: got %d want %d", c.name, got, c.want)
		}
	}

	// POST edit.
	post := func(tok string, r editReq) int {
		return postEditTok(t, ts.URL, "b1", r, tok)
	}
	add := editReq{Op: "add", Section: "Threads", Text: "x"}
	if got := post(noneTok, add); got != http.StatusNotFound {
		t.Errorf("POST none: got %d want 404", got)
	}
	if got := post(readTok, add); got != http.StatusForbidden {
		t.Errorf("POST read-only: got %d want 403", got)
	}
	if got := post(writeTok, add); got != http.StatusOK {
		t.Errorf("POST write-scoped: got %d want 200", got)
	}
	if got := post(admin, add); got != http.StatusOK {
		t.Errorf("POST admin: got %d want 200", got)
	}

	// SSE events.
	if got := authGet(t, board+"/events", noneTok); got != http.StatusNotFound {
		t.Errorf("SSE none: got %d want 404", got)
	}
	if got := authGet(t, board+"/events", readTok); got != http.StatusOK {
		t.Errorf("SSE read: got %d want 200", got)
	}
}

// postEditTok posts an edit as a specific bearer token.
func postEditTok(t *testing.T, base, boardID string, r editReq, token string) int {
	t.Helper()
	buf, _ := json.Marshal(r)
	req, _ := http.NewRequest("POST", base+"/api/board/"+boardID+"/edit", bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// TestScopedTokenSeesOnlySubtree verifies a subtree-scoped token's board view is
// carved to its granted node, and that writing outside the granted root is
// refused 403 while writing inside it lands.
func TestScopedTokenSeesOnlySubtree(t *testing.T) {
	s, ts := testServer(t, false)

	// Two sibling parents in Threads via an admin client.
	admin, _ := s.CreateToken("boss")
	ac := NewRemoteTree(ts.URL, "b1", admin)
	_, _ = ac.Apply(board.Op{Name: "add", Section: "Threads", Text: "parent-A"})
	_, _ = ac.Apply(board.Op{Name: "add", Section: "Threads", Text: "parent-B"})
	content, _, _ := s.Get("b1")
	th := board.Parse(content).Section("Threads")
	var aID, bID string
	for _, it := range th.Items {
		if strings.Contains(it.DisplayText(), "parent-A") {
			aID = it.ID
		}
		if strings.Contains(it.DisplayText(), "parent-B") {
			bID = it.ID
		}
	}

	// Grant a scoped token rooted at parent-A.
	scoped, _ := s.CreateTokenAs("agentA", "agentA", false)
	_ = s.AddGrant("agentA", "b1", aID, "write")
	tree := NewRemoteTree(ts.URL, "b1", scoped)

	// Get returns only the granted subtree: parent-B is invisible.
	n, err := tree.Get("", -1)
	if err != nil {
		t.Fatal(err)
	}
	blob := fmt.Sprintf("%+v", n)
	if strings.Contains(blob, "parent-B") {
		t.Fatalf("scoped view leaked sibling: %s", blob)
	}

	// Write inside the granted subtree lands.
	if _, err := tree.Apply(board.Op{Name: "reply", ID: aID, Text: "child-in-scope"}); err != nil {
		t.Fatalf("in-scope write refused: %v", err)
	}
	content, _, _ = s.Get("b1")
	if !strings.Contains(content, "child-in-scope") {
		t.Fatal("in-scope write not persisted")
	}

	// Write addressing the sibling (explicit Root outside grant) is refused 403.
	if got := postEditTok(t, ts.URL, "b1", editReq{Op: "reply", ID: bID, Root: bID, Text: "sneaky"}, scoped); got != http.StatusForbidden {
		t.Fatalf("cross-root write: got %d want 403", got)
	}
	// Even without an explicit Root, an id addressing the sibling is confined by
	// the forced grant root and refused (400 out-of-scope).
	if got := postEditTok(t, ts.URL, "b1", editReq{Op: "reply", ID: bID, Text: "sneaky2"}, scoped); got != http.StatusBadRequest {
		t.Fatalf("forced-root escape: got %d want 400", got)
	}
	content, _, _ = s.Get("b1")
	if strings.Contains(content, "sneaky") {
		t.Fatal("out-of-scope write leaked into board")
	}
}

// TestScopedSSE checks that a subtree-scoped token's event stream fires only for
// its own subtree, regardless of the ?node the client requests.
func TestScopedSSE(t *testing.T) {
	s, ts := testServer(t, false)
	admin, _ := s.CreateToken("boss")
	ac := NewRemoteTree(ts.URL, "b1", admin)
	_, _ = ac.Apply(board.Op{Name: "add", Section: "Threads", Text: "watched"})
	_, _ = ac.Apply(board.Op{Name: "add", Section: "Threads", Text: "other"})
	content, _, _ := s.Get("b1")
	th := board.Parse(content).Section("Threads")
	var watchedID, otherID string
	for _, it := range th.Items {
		if strings.Contains(it.DisplayText(), "watched") {
			watchedID = it.ID
		}
		if strings.Contains(it.DisplayText(), "other") {
			otherID = it.ID
		}
	}
	scoped, _ := s.CreateTokenAs("w", "w", false)
	_ = s.AddGrant("w", "b1", watchedID, "read")

	tree := NewRemoteTree(ts.URL, "b1", scoped)
	ch, cancel, err := tree.Watch("") // client asks for whole board; server forces scope
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	// Edit the OTHER subtree (as admin): scoped watcher must stay silent.
	_, _ = ac.Apply(board.Op{Name: "reply", ID: otherID, Text: "noise"})
	select {
	case <-ch:
		t.Fatal("scoped watcher fired for out-of-scope edit")
	case <-time.After(400 * time.Millisecond):
	}
	// Edit the watched subtree: event should arrive.
	_, _ = ac.Apply(board.Op{Name: "reply", ID: watchedID, Text: "signal"})
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("no event for in-scope edit")
	}
}

// TestScopedSSEAuthorsNoSiblingLeak proves F2: a subtree-scoped stream's author
// payload is restricted to ops that touched the granted subtree. A concurrent
// edit in a sibling subtree (which board-wide AuthorsSince would surface) must
// never appear in the scoped authors, and an in-subtree edit is attributed to
// its real author.
func TestScopedSSEAuthorsNoSiblingLeak(t *testing.T) {
	s, ts := testServer(t, false)
	admin, _ := s.CreateToken("boss")
	ac := NewRemoteTree(ts.URL, "b1", admin)
	_, _ = ac.Apply(board.Op{Name: "add", Section: "Threads", Text: "parent-A"})
	_, _ = ac.Apply(board.Op{Name: "add", Section: "Threads", Text: "parent-B"})
	content, verBefore, _ := s.Get("b1")
	th := board.Parse(content).Section("Threads")
	var aID, bID string
	for _, it := range th.Items {
		if strings.Contains(it.DisplayText(), "parent-A") {
			aID = it.ID
		}
		if strings.Contains(it.DisplayText(), "parent-B") {
			bID = it.ID
		}
	}

	// Two distinct principals: alice edits subtree A (watched), bob edits sibling
	// B. Authors are forced to the token subjects.
	aliceTok, _ := s.CreateToken("alice")
	bobTok, _ := s.CreateToken("bob")
	alice := NewRemoteTree(ts.URL, "b1", aliceTok)
	bob := NewRemoteTree(ts.URL, "b1", bobTok)
	if _, err := alice.Apply(board.Op{Name: "reply", ID: aID, Text: "in-A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := bob.Apply(board.Op{Name: "reply", ID: bID, Text: "in-B"}); err != nil {
		t.Fatal(err)
	}

	// Board-wide, both alice and bob authored ops since verBefore — the leak the
	// old scoped stream would have surfaced.
	all, err := s.AuthorsSince("b1", verBefore)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(all, "alice") || !contains(all, "bob") {
		t.Fatalf("precondition: board-wide authors %v want both alice and bob", all)
	}

	// Scoped to subtree A: only alice, never bob.
	cur, _, _ := s.Get("b1")
	scoped, err := s.AuthorsSinceTouching("b1", verBefore, subtreeLineSet(content, cur, aID))
	if err != nil {
		t.Fatal(err)
	}
	if !contains(scoped, "alice") {
		t.Fatalf("scoped authors %v missing in-subtree author alice", scoped)
	}
	if contains(scoped, "bob") {
		t.Fatalf("scoped authors %v leaked sibling-subtree author bob", scoped)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// TestScopedSSEEndToEndAuthor drives the SSE stream: a scoped watcher gets the
// in-subtree change attributed to the real editor.
func TestScopedSSEEndToEndAuthor(t *testing.T) {
	s, ts := testServer(t, false)
	admin, _ := s.CreateToken("boss")
	ac := NewRemoteTree(ts.URL, "b1", admin)
	_, _ = ac.Apply(board.Op{Name: "add", Section: "Threads", Text: "watched"})
	content, _, _ := s.Get("b1")
	th := board.Parse(content).Section("Threads")
	var watchedID string
	for _, it := range th.Items {
		if strings.Contains(it.DisplayText(), "watched") {
			watchedID = it.ID
		}
	}
	editorTok, _ := s.CreateToken("editor")
	editor := NewRemoteTree(ts.URL, "b1", editorTok)

	scopedTok, _ := s.CreateTokenAs("w", "w", false)
	_ = s.AddGrant("w", "b1", watchedID, "read")
	watcher := NewRemoteTree(ts.URL, "b1", scopedTok)
	ch, cancel, err := watcher.Watch("")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	time.Sleep(100 * time.Millisecond)

	if _, err := editor.Apply(board.Op{Name: "reply", ID: watchedID, Text: "signal"}); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-ch:
		if ev.Author != "editor" {
			t.Fatalf("in-subtree event author=%q want editor", ev.Author)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event for in-scope edit")
	}
}

// TestConcurrentSameNodeRace has two remote writers hammer the SAME node. With
// base+retry, last-writer-wins but no write is silently dropped and no 5xx.
func TestConcurrentSameNodeRace(t *testing.T) {
	s, ts := testServer(t, false)
	tok, _ := s.CreateToken("boss")
	seedC := NewRemoteTree(ts.URL, "b1", tok)
	_, _ = seedC.Apply(board.Op{Name: "add", Section: "Threads", Text: "hot"})
	content, _, _ := s.Get("b1")
	th := board.Parse(content).Section("Threads")
	var hotID string
	for _, it := range th.Items {
		if strings.Contains(it.DisplayText(), "hot") {
			hotID = it.ID
		}
	}

	const n = 20
	var wg sync.WaitGroup
	wg.Add(2)
	writer := func(prefix string) {
		defer wg.Done()
		c := NewRemoteTree(ts.URL, "b1", tok)
		for i := 0; i < n; i++ {
			if _, err := c.Apply(board.Op{Name: "reply", ID: hotID, Text: fmt.Sprintf("%s-%d", prefix, i)}); err != nil {
				t.Errorf("%s-%d: %v", prefix, i, err)
				return
			}
		}
	}
	go writer("x")
	go writer("y")
	wg.Wait()

	content, _, _ = s.Get("b1")
	for i := 0; i < n; i++ {
		if !strings.Contains(content, fmt.Sprintf("x-%d", i)) {
			t.Errorf("dropped x-%d", i)
		}
		if !strings.Contains(content, fmt.Sprintf("y-%d", i)) {
			t.Errorf("dropped y-%d", i)
		}
	}
}

// TestCLIGrantRoundTrip exercises the HTTP grant-management surface the CLI
// drives: mint a scoped token via --new-token, use it, list, then revoke.
func TestCLIGrantRoundTrip(t *testing.T) {
	s, ts := testServer(t, false)
	admin, _ := s.CreateToken("boss")
	root := threadsID(t, s)

	res, err := AddGrant(ts.URL, admin, "b1", "", "", "agent", root, "write")
	if err != nil {
		t.Fatal(err)
	}
	if res.Token == "" || res.Subject != "agent" || res.Perm != "write" {
		t.Fatalf("grant result = %+v", res)
	}
	// The minted token can write within its subtree.
	tree := NewRemoteTree(ts.URL, "b1", res.Token)
	if _, err := tree.Apply(board.Op{Name: "add", Section: "Threads", Text: "by-agent"}); err != nil {
		t.Fatalf("minted token write: %v", err)
	}

	grants, err := ListGrants(ts.URL, admin, "b1")
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 1 || grants[0].Subject != "agent" || grants[0].Root != root {
		t.Fatalf("grants = %+v", grants)
	}

	// A non-admin cannot list grants.
	if _, err := ListGrants(ts.URL, res.Token, "b1"); err == nil {
		t.Fatal("non-admin listed grants")
	}

	if err := RevokeGrant(ts.URL, admin, "b1", "agent", ""); err != nil {
		t.Fatal(err)
	}
	if got := authGet(t, ts.URL+"/api/board/b1", res.Token); got != http.StatusNotFound {
		t.Fatalf("after revoke GET: got %d want 404", got)
	}
}
