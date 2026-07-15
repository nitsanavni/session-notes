package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestServer spins up the full HTTP mux over a fresh temp-dir store.
func newTestServer(t *testing.T) (*httptest.Server, *Store) {
	t.Helper()
	store, err := newStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := &server{s: store}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/tree", srv.tree)
	mux.HandleFunc("GET /api/context", srv.ctx)
	mux.HandleFunc("POST /api/nodes", srv.createNode)
	mux.HandleFunc("PATCH /api/nodes/{id}", srv.patchNode)
	mux.HandleFunc("DELETE /api/nodes/{id}", srv.deleteNode)
	mux.HandleFunc("POST /api/edges", srv.addEdge)
	mux.HandleFunc("POST /api/reparent", srv.reparent)
	mux.HandleFunc("POST /api/presence", srv.presence)
	mux.HandleFunc("GET /api/events", srv.events)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, store
}

// req issues an HTTP request and returns status + body.
func req(t *testing.T, method, url, body string) (int, string) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	rq, err := http.NewRequest(method, url, r)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(rq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// create posts a node and returns its id (fails the test on non-200).
func create(t *testing.T, base, parent, kind, content string) string {
	t.Helper()
	body := fmt.Sprintf(`{"parent":%q,"kind":%q,"content":%q,"author":"nitsan"}`, parent, kind, content)
	st, resp := req(t, "POST", base+"/api/nodes", body)
	if st != 200 {
		t.Fatalf("create: status %d: %s", st, resp)
	}
	var n Node
	if err := json.Unmarshal([]byte(resp), &n); err != nil {
		t.Fatal(err)
	}
	return n.ID
}

// addNode is a tiny test helper that mirrors the createNode HTTP handler.
func addNode(t *testing.T, s *Store, parent, kind, content, author string) string {
	t.Helper()
	id := newID()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.append(author, "node.add", map[string]string{"id": id, "content": content, "author": author}); err != nil {
		t.Fatal(err)
	}
	if parent != "" {
		rank := s.maxRank(parent) + 1
		if _, err := s.append(author, "edge.add", Edge{Parent: parent, Child: id, Kind: kind, Rank: rank}); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func TestReplayRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s1, err := newStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	root := addNode(t, s1, "", "child", "root", "nitsan")
	child := addNode(t, s1, root, "child", "hello", "nitsan")
	addNode(t, s1, child, "reply", "world", "aria")
	wantNodes, wantEdges := len(s1.nodes), len(s1.edges)
	wantSeq := s1.seq

	// Reopen: state must rebuild identically from the log alone.
	s2, err := newStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(s2.nodes) != wantNodes {
		t.Fatalf("nodes: got %d want %d", len(s2.nodes), wantNodes)
	}
	if len(s2.edges) != wantEdges {
		t.Fatalf("edges: got %d want %d", len(s2.edges), wantEdges)
	}
	if s2.seq != wantSeq {
		t.Fatalf("seq: got %d want %d", s2.seq, wantSeq)
	}
	if s2.nodes[child].Content != "hello" {
		t.Fatalf("child content lost after replay")
	}
}

func TestContextDepthTruncation(t *testing.T) {
	s, err := newStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := addNode(t, s, "", "child", "a", "nitsan")
	b := addNode(t, s, a, "child", "b", "nitsan")
	c := addNode(t, s, b, "child", "c", "nitsan")
	addNode(t, s, c, "child", "d", "nitsan")

	s.mu.Lock()
	defer s.mu.Unlock()

	// depth 1 from a: b is visible, its child c is fog (childCount, no content).
	out1, _ := s.context(a, 1)
	sub1 := reencode(t, out1["subtree"])
	if len(sub1.Children) != 1 || sub1.Children[0].ID != b {
		t.Fatalf("depth1: expected b as only child")
	}
	if !sub1.Children[0].Cut || sub1.Children[0].ChildCount != 1 {
		t.Fatalf("depth1: b should be cut with childCount 1, got %+v", sub1.Children[0])
	}
	// The boundary node keeps its own content; the fog is its hidden children,
	// represented by childCount rather than by emitting c/d.
	if sub1.Children[0].Content != "b" {
		t.Fatalf("depth1: boundary node b should still carry its content")
	}
	if len(sub1.Children[0].Children) != 0 {
		t.Fatalf("depth1: b's descendants must be cut, not emitted")
	}

	// depth 2 from a: b and c visible, d is fog.
	out2, _ := s.context(a, 2)
	sub2 := reencode(t, out2["subtree"])
	cc := sub2.Children[0].Children
	if len(cc) != 1 || cc[0].ID != c || cc[0].Content != "c" {
		t.Fatalf("depth2: c should be visible with content")
	}
	if !cc[0].Cut || cc[0].ChildCount != 1 {
		t.Fatalf("depth2: c should be cut marking d, got %+v", cc[0])
	}

	// path is the full ancestor spine root→a.
	path := out1["path"].([]*Node)
	if len(path) != 1 || path[0].ID != a {
		t.Fatalf("path from a should be [a]")
	}
}

func TestReplyEdgeCreation(t *testing.T) {
	s, err := newStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := addNode(t, s, "", "child", "topic", "nitsan")
	reply := addNode(t, s, root, "reply", "a thought", "aria")

	s.mu.Lock()
	defer s.mu.Unlock()
	kids := s.childrenOf(root)
	if len(kids) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(kids))
	}
	if kids[0].Kind != "reply" || kids[0].Child != reply {
		t.Fatalf("expected reply edge to %s, got %+v", reply, kids[0])
	}
	if s.nodes[reply].Author != "aria" {
		t.Fatalf("reply author should be aria")
	}
}

// reencode round-trips a CtxNode through JSON so omitempty is honored exactly
// as the API would emit it.
func reencode(t *testing.T, v any) CtxNode {
	t.Helper()
	b, _ := json.Marshal(v)
	var cn CtxNode
	if err := json.Unmarshal(b, &cn); err != nil {
		t.Fatal(err)
	}
	return cn
}

// ---- (1) reparent cycles ----

func TestReparentCycles(t *testing.T) {
	ts, store := newTestServer(t)
	a := create(t, ts.URL, "", "child", "a")
	b := create(t, ts.URL, a, "child", "b")
	c := create(t, ts.URL, b, "child", "c")

	// self-parent rejected
	if st, _ := req(t, "POST", ts.URL+"/api/reparent", fmt.Sprintf(`{"child":%q,"parent":%q}`, a, a)); st != 400 {
		t.Fatalf("self-parent: want 400 got %d", st)
	}
	// a under its descendant c -> cycle rejected
	if st, _ := req(t, "POST", ts.URL+"/api/reparent", fmt.Sprintf(`{"child":%q,"parent":%q}`, a, c)); st != 400 {
		t.Fatalf("descendant cycle: want 400 got %d", st)
	}
	// a under direct child b -> cycle rejected
	if st, _ := req(t, "POST", ts.URL+"/api/reparent", fmt.Sprintf(`{"child":%q,"parent":%q}`, a, b)); st != 400 {
		t.Fatalf("direct-child cycle: want 400 got %d", st)
	}
	// legit move: c under a
	if st, _ := req(t, "POST", ts.URL+"/api/reparent", fmt.Sprintf(`{"child":%q,"parent":%q}`, c, a)); st != 204 {
		t.Fatalf("valid reparent: want 204 got %d", st)
	}
	// tree stays acyclic: a walkable without looping forever
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.wouldCycle(a, a) != true {
		t.Fatal("sanity: wouldCycle(a,a) should be true")
	}
}

func TestReparentUnknownParent(t *testing.T) {
	ts, _ := newTestServer(t)
	a := create(t, ts.URL, "", "child", "a")
	if st, _ := req(t, "POST", ts.URL+"/api/reparent", fmt.Sprintf(`{"child":%q,"parent":"nope99"}`, a)); st != 400 {
		t.Fatalf("unknown parent: want 400 got %d", st)
	}
	// reparent to root ("") is allowed (outdent to top)
	if st, _ := req(t, "POST", ts.URL+"/api/reparent", fmt.Sprintf(`{"child":%q,"parent":""}`, a)); st != 204 {
		t.Fatalf("reparent to root: want 204 got %d", st)
	}
}

// ---- (2) input validation ----

func TestCreateValidation(t *testing.T) {
	ts, _ := newTestServer(t)
	// unknown parent -> 404
	if st, _ := req(t, "POST", ts.URL+"/api/nodes", `{"parent":"ghost1","content":"x"}`); st != 404 {
		t.Fatalf("unknown parent: want 404 got %d", st)
	}
	// unknown edge kind -> 400
	if st, _ := req(t, "POST", ts.URL+"/api/nodes", `{"kind":"bogus","content":"x"}`); st != 400 {
		t.Fatalf("bad kind: want 400 got %d", st)
	}
	// malformed JSON -> 400, no panic
	if st, _ := req(t, "POST", ts.URL+"/api/nodes", `{not json`); st != 400 {
		t.Fatalf("malformed: want 400 got %d", st)
	}
	// empty content allowed (UI creates empty then edits)
	if st, _ := req(t, "POST", ts.URL+"/api/nodes", `{"content":""}`); st != 200 {
		t.Fatalf("empty content: want 200 got %d", st)
	}
	// oversized content -> 413
	big := strings.Repeat("x", maxContentBytes+1)
	if st, _ := req(t, "POST", ts.URL+"/api/nodes", fmt.Sprintf(`{"content":%q}`, big)); st != 413 {
		t.Fatalf("oversize: want 413 got %d", st)
	}
	// at the cap is fine
	ok := strings.Repeat("x", maxContentBytes)
	if st, _ := req(t, "POST", ts.URL+"/api/nodes", fmt.Sprintf(`{"content":%q}`, ok)); st != 200 {
		t.Fatalf("at-cap: want 200 got %d", st)
	}
}

func TestPatchValidation(t *testing.T) {
	ts, _ := newTestServer(t)
	a := create(t, ts.URL, "", "child", "a")
	// unknown node -> 404
	if st, _ := req(t, "PATCH", ts.URL+"/api/nodes/ghost", `{"content":"x"}`); st != 404 {
		t.Fatalf("patch unknown: want 404 got %d", st)
	}
	// malformed -> 400
	if st, _ := req(t, "PATCH", ts.URL+"/api/nodes/"+a, `{`); st != 400 {
		t.Fatalf("patch malformed: want 400 got %d", st)
	}
	// oversize -> 413
	big := strings.Repeat("x", maxContentBytes+1)
	if st, _ := req(t, "PATCH", ts.URL+"/api/nodes/"+a, fmt.Sprintf(`{"content":%q}`, big)); st != 413 {
		t.Fatalf("patch oversize: want 413 got %d", st)
	}
}

func TestEdgeValidation(t *testing.T) {
	ts, _ := newTestServer(t)
	a := create(t, ts.URL, "", "child", "a")
	b := create(t, ts.URL, "", "child", "b")
	// unknown child -> 400
	if st, _ := req(t, "POST", ts.URL+"/api/edges", `{"parent":"","child":"ghost","kind":"quote"}`); st != 400 {
		t.Fatalf("edge unknown child: want 400 got %d", st)
	}
	// unknown parent -> 400
	if st, _ := req(t, "POST", ts.URL+"/api/edges", fmt.Sprintf(`{"parent":"ghost","child":%q,"kind":"quote"}`, a)); st != 400 {
		t.Fatalf("edge unknown parent: want 400 got %d", st)
	}
	// unknown kind -> 400
	if st, _ := req(t, "POST", ts.URL+"/api/edges", fmt.Sprintf(`{"parent":%q,"child":%q,"kind":"bogus"}`, a, b)); st != 400 {
		t.Fatalf("edge bad kind: want 400 got %d", st)
	}
	// non-finite rank -> 400 (overflow decodes to +Inf / decode error, both 400)
	if st, _ := req(t, "POST", ts.URL+"/api/edges", fmt.Sprintf(`{"parent":%q,"child":%q,"kind":"quote","rank":1e999}`, a, b)); st != 400 {
		t.Fatalf("edge inf rank: want 400 got %d", st)
	}
	// valid quote edge -> 200
	if st, _ := req(t, "POST", ts.URL+"/api/edges", fmt.Sprintf(`{"parent":%q,"child":%q,"kind":"quote"}`, a, b)); st != 200 {
		t.Fatalf("valid edge: want 200 got %d", st)
	}
}

// ---- (3) delete semantics ----

func TestDeleteSubtreeAndDangling(t *testing.T) {
	ts, store := newTestServer(t)
	root := create(t, ts.URL, "", "child", "root")
	mid := create(t, ts.URL, root, "child", "mid")
	leaf := create(t, ts.URL, mid, "child", "leaf")
	other := create(t, ts.URL, "", "child", "other")
	// other quotes leaf
	if st, _ := req(t, "POST", ts.URL+"/api/edges", fmt.Sprintf(`{"parent":%q,"child":%q,"kind":"quote"}`, other, leaf)); st != 200 {
		t.Fatal("quote edge setup failed")
	}
	// delete root -> mid, leaf gone
	if st, _ := req(t, "DELETE", ts.URL+"/api/nodes/"+root, ""); st != 204 {
		t.Fatalf("delete root: want 204 got %d", st)
	}
	store.mu.Lock()
	for _, id := range []string{root, mid, leaf} {
		if store.nodes[id] != nil {
			t.Fatalf("node %s should be gone after subtree delete", id)
		}
	}
	// the quote edge that pointed AT leaf must be gone too (no dangling edge)
	for _, e := range store.edges {
		if e.Child == leaf || e.Parent == leaf || e.Child == mid || e.Parent == root {
			t.Fatalf("dangling edge survived: %+v", e)
		}
	}
	store.mu.Unlock()
	// double-delete -> 404, no panic
	if st, _ := req(t, "DELETE", ts.URL+"/api/nodes/"+root, ""); st != 404 {
		t.Fatalf("double delete: want 404 got %d", st)
	}
}

// ---- (4) event-log robustness ----

func TestReplayTruncatedLastLine(t *testing.T) {
	dir := t.TempDir()
	s, err := newStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	id := addNode(t, s, "", "child", "keep", "nitsan")
	s.f.Close()
	// append a truncated (invalid JSON) final line, as a crash mid-write would
	f, _ := os.OpenFile(dir+"/events.jsonl", os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(`{"seq":999,"kind":"node.add","payl`)
	f.Close()
	// boot must succeed, skipping the bad line
	s2, err := newStore(dir)
	if err != nil {
		t.Fatalf("boot with truncated line failed: %v", err)
	}
	if s2.nodes[id] == nil {
		t.Fatal("valid node lost after truncated-line recovery")
	}
}

func TestReplayUnknownKindAndEmpty(t *testing.T) {
	dir := t.TempDir()
	// empty file boots fine (and seeds)
	if _, err := newStore(dir); err != nil {
		t.Fatalf("empty-file boot: %v", err)
	}
	// unknown event kind is a no-op, not fatal
	dir2 := t.TempDir()
	os.WriteFile(dir2+"/events.jsonl", []byte(`{"seq":1,"kind":"weird.thing","payload":{}}`+"\n"), 0o644)
	s, err := newStore(dir2)
	if err != nil {
		t.Fatalf("unknown-kind boot: %v", err)
	}
	if s.seq != 1 {
		t.Fatalf("seq should track unknown-kind events, got %d", s.seq)
	}
}

func TestReplayLargeLogFast(t *testing.T) {
	dir := t.TempDir()
	s, err := newStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	root := addNode(t, s, "", "child", "root", "nitsan")
	for i := 0; i < 10000; i++ {
		addNode(t, s, root, "child", fmt.Sprintf("n%d", i), "nitsan")
	}
	s.f.Close()
	start := time.Now()
	s2, err := newStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("10k-event replay too slow: %v", d)
	}
	if len(s2.nodes) < 10000 {
		t.Fatalf("expected >=10000 nodes replayed, got %d", len(s2.nodes))
	}
}

// ---- (5) concurrency ----

func TestConcurrentCreate(t *testing.T) {
	ts, store := newTestServer(t)
	const G, N = 20, 25
	var wg sync.WaitGroup
	ids := make(chan string, G*N)
	for g := 0; g < G; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < N; i++ {
				ids <- create(t, ts.URL, "", "child", "c")
			}
		}()
	}
	wg.Wait()
	close(ids)
	seen := map[string]bool{}
	for id := range ids {
		seen[id] = true
	}
	if len(seen) != G*N {
		t.Fatalf("expected %d unique ids, got %d", G*N, len(seen))
	}
	store.mu.Lock()
	for id := range seen {
		if store.nodes[id] == nil {
			t.Fatalf("node %s missing from store", id)
		}
	}
	store.mu.Unlock()
	// every persisted JSONL line must parse and seqs strictly increase
	b, _ := os.ReadFile(store.path)
	last := 0
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("corrupt JSONL line: %q: %v", line, err)
		}
		if e.Seq <= last {
			t.Fatalf("seq not strictly monotonic: %d after %d", e.Seq, last)
		}
		last = e.Seq
	}
}

func TestConcurrentReparentDelete(t *testing.T) {
	ts, _ := newTestServer(t)
	root := create(t, ts.URL, "", "child", "root")
	var kids []string
	for i := 0; i < 30; i++ {
		kids = append(kids, create(t, ts.URL, root, "child", fmt.Sprintf("k%d", i)))
	}
	var wg sync.WaitGroup
	for _, k := range kids {
		wg.Add(2)
		go func(id string) {
			defer wg.Done()
			req(t, "POST", ts.URL+"/api/reparent", fmt.Sprintf(`{"child":%q,"parent":%q}`, id, root))
		}(k)
		go func(id string) { defer wg.Done(); req(t, "DELETE", ts.URL+"/api/nodes/"+id, "") }(k)
	}
	// hammer delete of the root concurrently too
	wg.Add(1)
	go func() { defer wg.Done(); req(t, "DELETE", ts.URL+"/api/nodes/"+root, "") }()
	wg.Wait()
	// server still responsive & consistent (no panic, tree query returns)
	if st, _ := req(t, "GET", ts.URL+"/api/tree", ""); st != 200 {
		t.Fatalf("tree after churn: want 200 got %d", st)
	}
}

// ---- (6) SSE correctness ----

func readSSE(t *testing.T, body io.Reader, want int, deadline time.Duration) []Event {
	t.Helper()
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	var out []Event
	done := make(chan struct{})
	go func() {
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var e Event
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &e) == nil {
				out = append(out, e)
				if len(out) >= want {
					close(done)
					return
				}
			}
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(deadline):
	}
	return out
}

func TestSSESinceReplayNoGapNoDup(t *testing.T) {
	ts, store := newTestServer(t)
	// prime a few events
	create(t, ts.URL, "", "child", "seed")
	store.mu.Lock()
	base := store.seq
	store.mu.Unlock()

	resp, err := http.Get(fmt.Sprintf("%s/api/events?since=%d", ts.URL, base))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	const M = 40
	go func() {
		for i := 0; i < M; i++ {
			create(t, ts.URL, "", "child", fmt.Sprintf("live%d", i))
		}
	}()
	evs := readSSE(t, resp.Body, M, 5*time.Second)
	if len(evs) < M {
		t.Fatalf("expected >=%d events, got %d", M, len(evs))
	}
	// strictly increasing, all > base, contiguous handoff (no gap, no dup)
	prev := base
	for _, e := range evs[:M] {
		if e.Seq <= base {
			t.Fatalf("event seq %d not after since=%d", e.Seq, base)
		}
		if e.Seq != prev+1 {
			t.Fatalf("gap or dup: seq %d after %d", e.Seq, prev)
		}
		prev = e.Seq
	}
}

func TestSSENoGoroutineLeak(t *testing.T) {
	ts, _ := newTestServer(t)
	create(t, ts.URL, "", "child", "x")
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	before := runtime.NumGoroutine()
	for i := 0; i < 50; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		rq, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/events?since=0", nil)
		resp, err := http.DefaultClient.Do(rq)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		buf := make([]byte, 64)
		resp.Body.Read(buf) // read the ": connected" preamble
		cancel()
		resp.Body.Close()
	}
	// let server-side handlers unwind
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		runtime.GC()
		if runtime.NumGoroutine() <= before+5 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("goroutine leak: before=%d after=%d", before, runtime.NumGoroutine())
}

func TestSSESlowConsumerDoesNotBlock(t *testing.T) {
	ts, store := newTestServer(t)
	// A subscriber that never drains: register a raw channel like the handler does.
	slow := make(chan Event, 1)
	store.mu.Lock()
	store.subs[slow] = struct{}{}
	store.mu.Unlock()
	defer func() {
		store.mu.Lock()
		delete(store.subs, slow)
		store.mu.Unlock()
	}()
	// Writers must not block even though `slow` fills up and is never read.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			create(t, ts.URL, "", "child", "x")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("writers blocked by a slow/stuck consumer")
	}
}

// ---- (7) context endpoint ----

func TestContextEndpoint(t *testing.T) {
	ts, _ := newTestServer(t)
	a := create(t, ts.URL, "", "child", "a")
	b := create(t, ts.URL, a, "child", "b")
	c := create(t, ts.URL, b, "child", "c")
	_ = create(t, ts.URL, c, "child", "d")

	// nonexistent node -> 404
	if st, _ := req(t, "GET", ts.URL+"/api/context?node=ghost&depth=2", ""); st != 404 {
		t.Fatalf("ctx ghost: want 404 got %d", st)
	}
	// empty node -> 404 (no synthetic board root)
	if st, _ := req(t, "GET", ts.URL+"/api/context?node=&depth=2", ""); st != 404 {
		t.Fatalf("ctx empty node: want 404 got %d", st)
	}
	// depth=0: node itself, children cut with childCount
	st, body := req(t, "GET", ts.URL+"/api/context?node="+b+"&depth=0", "")
	if st != 200 {
		t.Fatalf("ctx depth0: want 200 got %d", st)
	}
	var d0 struct {
		Path    []Node  `json:"path"`
		Subtree CtxNode `json:"subtree"`
	}
	json.Unmarshal([]byte(body), &d0)
	if !d0.Subtree.Cut || d0.Subtree.ChildCount != 1 {
		t.Fatalf("depth0 subtree should be cut w/ childCount 1: %+v", d0.Subtree)
	}
	// path is the full ancestor spine root..b = [a,b]
	if len(d0.Path) != 2 || d0.Path[0].ID != a || d0.Path[1].ID != b {
		t.Fatalf("path from b should be [a,b], got %+v", d0.Path)
	}
	// huge depth: entire chain visible, deepest leaf not cut
	_, body2 := req(t, "GET", ts.URL+"/api/context?node="+a+"&depth=99", "")
	var d2 struct {
		Subtree CtxNode `json:"subtree"`
	}
	json.Unmarshal([]byte(body2), &d2)
	// a -> b -> c -> d, none cut
	cur := d2.Subtree
	depth := 0
	for len(cur.Children) == 1 {
		cur = cur.Children[0]
		depth++
	}
	if depth != 3 || cur.Cut {
		t.Fatalf("huge depth should reveal full chain uncut, depth=%d cut=%v", depth, cur.Cut)
	}
}

// ---- (8) presence ----

func TestPresenceUnknownNodeAndNotPersisted(t *testing.T) {
	ts, store := newTestServer(t)
	sizeBefore := logSize(t, store.path)
	logLenBefore := len(store.log)
	// unknown node accepted (presence is advisory/transient) -> 204
	if st, _ := req(t, "POST", ts.URL+"/api/presence", `{"actor":"aria","node":"ghost"}`); st != 204 {
		t.Fatalf("presence unknown node: want 204 got %d", st)
	}
	// spam presence: must not grow events.jsonl nor the in-memory history
	for i := 0; i < 500; i++ {
		req(t, "POST", ts.URL+"/api/presence", fmt.Sprintf(`{"actor":"aria","node":"n%d"}`, i))
	}
	if s := logSize(t, store.path); s != sizeBefore {
		t.Fatalf("presence must not touch events.jsonl: %d -> %d", sizeBefore, s)
	}
	store.mu.Lock()
	if len(store.log) != logLenBefore {
		t.Fatalf("presence must not grow in-memory log: %d -> %d", logLenBefore, len(store.log))
	}
	store.mu.Unlock()
}

func logSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatal(err)
	}
	return fi.Size()
}
