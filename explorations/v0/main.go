// Command v0 is a from-scratch prototype of a notes+agents outliner built on
// four primitives: Node, typed Edge, position-as-context (fog of war), and
// authorship. Storage is an append-only JSONL event log replayed on boot; the
// only sync mechanism the UI uses is an SSE event stream.
//
//	go run ./explorations/v0 -port 4800 -data <dir>
package main

import (
	"crypto/rand"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed ui.html
var ui embed.FS

// ---- primitives ----

// Node is one block of content authored by a user or an agent.
type Node struct {
	ID        string `json:"id"`
	Content   string `json:"content"`
	Author    string `json:"author"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// Edge is a typed, ranked relationship between two nodes. Kind is one of
// "child", "reply", or "quote".
type Edge struct {
	Parent string  `json:"parent"`
	Child  string  `json:"child"`
	Kind   string  `json:"kind"`
	Rank   float64 `json:"rank"`
}

// Event is one appended record in the log. Payload is kind-specific JSON.
type Event struct {
	Seq     int             `json:"seq"`
	TS      string          `json:"ts"`
	Actor   string          `json:"actor"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

// ---- store ----

// Store holds in-memory state rebuilt by replaying the event log. Every
// mutation appends to the log first, then applies to memory.
type Store struct {
	mu   sync.Mutex
	path string
	f    *os.File

	seq   int
	nodes map[string]*Node
	edges []Edge

	subs map[chan Event]struct{}
	log  []Event // full history, for since= replay
}

func newStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := dir + "/events.jsonl"
	s := &Store{
		path:  path,
		nodes: map[string]*Node{},
		subs:  map[chan Event]struct{}{},
	}
	// Replay existing log.
	if b, err := os.ReadFile(path); err == nil {
		for i, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var e Event
			if err := json.Unmarshal([]byte(line), &e); err != nil {
				// A crash mid-write leaves a truncated final line; a bit-flip can
				// corrupt any line. Skip the bad record with a warning rather than
				// refusing to boot — the rest of the log is still authoritative.
				log.Printf("replay: skipping malformed line %d in %s: %v", i+1, path, err)
				continue
			}
			s.apply(e)
			// presence.move is transient and never persisted (see append); if an
			// old log still carries some, replay them as no-ops but keep them out
			// of the in-memory history so since= replay stays log-consistent.
			if e.Kind != "presence.move" {
				s.log = append(s.log, e)
			}
			if e.Seq > s.seq {
				s.seq = e.Seq
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	s.f = f
	if len(s.log) == 0 {
		s.seed()
	}
	return s, nil
}

// apply mutates in-memory state from one event. It is pure state-transition;
// it never writes to disk.
func (s *Store) apply(e Event) {
	switch e.Kind {
	case "node.add":
		var p struct{ ID, Content, Author string }
		_ = json.Unmarshal(e.Payload, &p)
		s.nodes[p.ID] = &Node{ID: p.ID, Content: p.Content, Author: p.Author, CreatedAt: e.TS, UpdatedAt: e.TS}
	case "node.edit":
		var p struct{ ID, Content string }
		_ = json.Unmarshal(e.Payload, &p)
		if n := s.nodes[p.ID]; n != nil {
			n.Content = p.Content
			n.UpdatedAt = e.TS
		}
	case "node.del":
		var p struct{ ID string }
		_ = json.Unmarshal(e.Payload, &p)
		s.deleteSubtree(p.ID)
	case "edge.add":
		var ed Edge
		_ = json.Unmarshal(e.Payload, &ed)
		s.edges = append(s.edges, ed)
	case "edge.move":
		// Re-parent: drop the child's existing structural (child/reply) edges,
		// then attach one fresh edge. Quote edges are pointers, left untouched.
		var ed Edge
		_ = json.Unmarshal(e.Payload, &ed)
		kept := s.edges[:0]
		for _, x := range s.edges {
			if x.Child == ed.Child && x.Kind != "quote" {
				continue
			}
			kept = append(kept, x)
		}
		s.edges = kept
		s.edges = append(s.edges, ed)
	case "presence.move":
		// transient; carried on the stream only
	}
}

// deleteSubtree removes a node, its descendants, and all touching edges.
func (s *Store) deleteSubtree(id string) {
	doomed := map[string]bool{id: true}
	for grew := true; grew; {
		grew = false
		for _, e := range s.edges {
			if doomed[e.Parent] && !doomed[e.Child] {
				doomed[e.Child] = true
				grew = true
			}
		}
	}
	for d := range doomed {
		delete(s.nodes, d)
	}
	kept := s.edges[:0]
	for _, e := range s.edges {
		if !doomed[e.Parent] && !doomed[e.Child] {
			kept = append(kept, e)
		}
	}
	s.edges = kept
}

// append writes one event to the log, applies it, and fans it out to
// subscribers. Callers must hold s.mu.
func (s *Store) append(actor, kind string, payload any) (Event, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return Event{}, err
	}
	s.seq++
	e := Event{Seq: s.seq, TS: now(), Actor: actor, Kind: kind, Payload: raw}
	// presence.move is transient: broadcast to live subscribers only, never
	// written to events.jsonl or the in-memory history. Cursor moves are a
	// high-frequency stream and persisting them would bloat the log without
	// value (position is not part of durable state).
	if kind != "presence.move" {
		line, _ := json.Marshal(e)
		if s.f != nil {
			if _, err := s.f.Write(append(line, '\n')); err != nil {
				s.seq--
				return Event{}, err
			}
		}
		s.log = append(s.log, e)
	}
	s.apply(e)
	for ch := range s.subs {
		select {
		case ch <- e:
		default:
		}
	}
	return e, nil
}

func (s *Store) seed() {
	root, _ := s.append("system", "node.add", map[string]string{"id": newID(), "content": "Welcome — a tree you and agents inhabit together", "author": "system"})
	var rootID string
	_ = json.Unmarshal(root.Payload, &struct {
		ID *string `json:"id"`
	}{&rootID})
	kids := []string{
		"j / k or ↑ ↓ move the cursor; Enter zooms in, Esc zooms out",
		"i edits a node, o adds a sibling, Tab / Shift-Tab indent",
		"r replies (a threaded child); y yanks, p pastes as a quote",
		"Everything streams live over SSE — open a second tab and watch",
	}
	for i, c := range kids {
		add, _ := s.append("system", "node.add", map[string]string{"id": newID(), "content": c, "author": "system"})
		var cid string
		_ = json.Unmarshal(add.Payload, &struct {
			ID *string `json:"id"`
		}{&cid})
		_, _ = s.append("system", "edge.add", Edge{Parent: rootID, Child: cid, Kind: "child", Rank: float64(i + 1)})
	}
	agent, _ := s.append("aria", "node.add", map[string]string{"id": newID(), "content": "I'm an agent living at this position — my notes render in blue.", "author": "aria"})
	var aid string
	_ = json.Unmarshal(agent.Payload, &struct {
		ID *string `json:"id"`
	}{&aid})
	_, _ = s.append("aria", "edge.add", Edge{Parent: rootID, Child: aid, Kind: "reply", Rank: 5})
}

// ---- queries (caller holds s.mu) ----

func (s *Store) childrenOf(id string) []Edge {
	var out []Edge
	for _, e := range s.edges {
		if e.Parent == id {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Rank < out[j].Rank })
	return out
}

func (s *Store) parentOf(id string) (Edge, bool) {
	for _, e := range s.edges {
		if e.Child == id {
			return e, true
		}
	}
	return Edge{}, false
}

func (s *Store) maxRank(parent string) float64 {
	m := 0.0
	for _, e := range s.edges {
		if e.Parent == parent && e.Rank > m {
			m = e.Rank
		}
	}
	return m
}

// maxContentBytes caps node content. The UI never sends anything close; the cap
// exists to reject accidental or hostile multi-megabyte payloads (→ 413).
const maxContentBytes = 64 * 1024

// validEdgeKind reports whether k is one of the three known edge kinds.
func validEdgeKind(k string) bool {
	switch k {
	case "child", "reply", "quote":
		return true
	}
	return false
}

// wouldCycle reports whether attaching child under parent would create a cycle,
// i.e. parent is child itself or a descendant of child. It follows structural
// (non-quote) edges only, since quote edges are pointers, not containment.
// Caller holds s.mu.
func (s *Store) wouldCycle(child, parent string) bool {
	if parent == "" {
		return false
	}
	if parent == child {
		return true
	}
	desc := map[string]bool{child: true}
	for grew := true; grew; {
		grew = false
		for _, e := range s.edges {
			if e.Kind == "quote" {
				continue
			}
			if desc[e.Parent] && !desc[e.Child] {
				desc[e.Child] = true
				grew = true
			}
		}
	}
	return desc[parent]
}

// ---- context primitive (fog of war) ----

// CtxNode is a subtree node in a context response. When the subtree is cut at
// the depth limit, Content/Author are empty and ChildCount marks the fog.
type CtxNode struct {
	ID         string    `json:"id"`
	Content    string    `json:"content,omitempty"`
	Author     string    `json:"author,omitempty"`
	Kind       string    `json:"kind,omitempty"`
	Children   []CtxNode `json:"children,omitempty"`
	ChildCount int       `json:"childCount,omitempty"`
	Cut        bool      `json:"cut,omitempty"`
}

func (s *Store) context(id string, depth int) (map[string]any, bool) {
	if s.nodes[id] == nil {
		return nil, false
	}
	// Ancestor path root→id.
	var path []*Node
	for cur := id; ; {
		path = append([]*Node{s.nodes[cur]}, path...)
		e, ok := s.parentOf(cur)
		if !ok {
			break
		}
		cur = e.Parent
	}
	var build func(nodeID, kind string, d int) CtxNode
	build = func(nodeID, kind string, d int) CtxNode {
		n := s.nodes[nodeID]
		kids := s.childrenOf(nodeID)
		cn := CtxNode{ID: nodeID, Content: n.Content, Author: n.Author, Kind: kind}
		if d <= 0 {
			if len(kids) > 0 {
				cn.ChildCount = len(kids)
				cn.Cut = true
			}
			return cn
		}
		for _, e := range kids {
			cn.Children = append(cn.Children, build(e.Child, e.Kind, d-1))
		}
		return cn
	}
	subtree := build(id, "", depth)
	return map[string]any{"path": path, "subtree": subtree}, true
}

// ---- http ----

type server struct{ s *Store }

func (srv *server) tree(w http.ResponseWriter, r *http.Request) {
	srv.s.mu.Lock()
	defer srv.s.mu.Unlock()
	nodes := make([]*Node, 0, len(srv.s.nodes))
	for _, n := range srv.s.nodes {
		nodes = append(nodes, n)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].CreatedAt < nodes[j].CreatedAt })
	writeJSON(w, map[string]any{"nodes": nodes, "edges": srv.s.edges})
}

func (srv *server) ctx(w http.ResponseWriter, r *http.Request) {
	depth := 2
	if d := r.URL.Query().Get("depth"); d != "" {
		fmt.Sscanf(d, "%d", &depth)
	}
	srv.s.mu.Lock()
	defer srv.s.mu.Unlock()
	out, ok := srv.s.context(r.URL.Query().Get("node"), depth)
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, out)
}

func (srv *server) createNode(w http.ResponseWriter, r *http.Request) {
	var in struct{ Parent, Kind, Content, Author string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if in.Author == "" {
		in.Author = "nitsan"
	}
	if in.Kind == "" {
		in.Kind = "child"
	}
	if !validEdgeKind(in.Kind) {
		http.Error(w, "unknown edge kind", 400)
		return
	}
	if len(in.Content) > maxContentBytes {
		http.Error(w, "content too large", 413)
		return
	}
	id := newID()
	srv.s.mu.Lock()
	defer srv.s.mu.Unlock()
	// A non-root node must attach to an existing parent, else it becomes an
	// invisible orphan (no incoming-edge-free root, no visible parent).
	if in.Parent != "" && srv.s.nodes[in.Parent] == nil {
		http.Error(w, "unknown parent", 404)
		return
	}
	if _, err := srv.s.append(in.Author, "node.add", map[string]string{"id": id, "content": in.Content, "author": in.Author}); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if in.Parent != "" || in.Kind != "child" {
		rank := srv.s.maxRank(in.Parent) + 1
		if _, err := srv.s.append(in.Author, "edge.add", Edge{Parent: in.Parent, Child: id, Kind: in.Kind, Rank: rank}); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}
	writeJSON(w, srv.s.nodes[id])
}

func (srv *server) patchNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in struct{ Content string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if len(in.Content) > maxContentBytes {
		http.Error(w, "content too large", 413)
		return
	}
	srv.s.mu.Lock()
	defer srv.s.mu.Unlock()
	if srv.s.nodes[id] == nil {
		http.NotFound(w, r)
		return
	}
	if _, err := srv.s.append(srv.s.nodes[id].Author, "node.edit", map[string]string{"id": id, "content": in.Content}); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, srv.s.nodes[id])
}

func (srv *server) deleteNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	srv.s.mu.Lock()
	defer srv.s.mu.Unlock()
	if srv.s.nodes[id] == nil {
		http.NotFound(w, r)
		return
	}
	if _, err := srv.s.append("nitsan", "node.del", map[string]string{"id": id}); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(204)
}

// addEdge attaches an arbitrary typed edge between two existing nodes. Used by
// the UI for quote pointers (kind "quote") to an already-existing target.
func (srv *server) addEdge(w http.ResponseWriter, r *http.Request) {
	var in Edge
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if in.Kind == "" {
		in.Kind = "quote"
	}
	if !validEdgeKind(in.Kind) {
		http.Error(w, "unknown edge kind", 400)
		return
	}
	if math.IsNaN(in.Rank) || math.IsInf(in.Rank, 0) {
		http.Error(w, "invalid rank", 400)
		return
	}
	srv.s.mu.Lock()
	defer srv.s.mu.Unlock()
	if srv.s.nodes[in.Child] == nil {
		http.Error(w, "unknown child", 400)
		return
	}
	if in.Parent != "" && srv.s.nodes[in.Parent] == nil {
		http.Error(w, "unknown parent", 400)
		return
	}
	if in.Rank == 0 {
		in.Rank = srv.s.maxRank(in.Parent) + 1
	}
	if _, err := srv.s.append("nitsan", "edge.add", in); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, in)
}

// reparent moves a node under a new parent via an edge.move event.
func (srv *server) reparent(w http.ResponseWriter, r *http.Request) {
	var in struct{ Child, Parent, Kind string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if in.Kind == "" {
		in.Kind = "child"
	}
	if !validEdgeKind(in.Kind) {
		http.Error(w, "unknown edge kind", 400)
		return
	}
	srv.s.mu.Lock()
	defer srv.s.mu.Unlock()
	if srv.s.nodes[in.Child] == nil {
		http.Error(w, "unknown child", 400)
		return
	}
	if in.Parent != "" && srv.s.nodes[in.Parent] == nil {
		http.Error(w, "unknown parent", 400)
		return
	}
	// Reject cycles: making a node a child of itself or of one of its own
	// descendants would corrupt the tree (childrenOf recursion loops forever and
	// clients hang).
	if srv.s.wouldCycle(in.Child, in.Parent) {
		http.Error(w, "reparent would create a cycle", 400)
		return
	}
	rank := srv.s.maxRank(in.Parent) + 1
	if _, err := srv.s.append("nitsan", "edge.move", Edge{Parent: in.Parent, Child: in.Child, Kind: in.Kind, Rank: rank}); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(204)
}

func (srv *server) presence(w http.ResponseWriter, r *http.Request) {
	var in struct{ Actor, Node string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	srv.s.mu.Lock()
	defer srv.s.mu.Unlock()
	_, _ = srv.s.append(in.Actor, "presence.move", map[string]string{"actor": in.Actor, "node": in.Node})
	w.WriteHeader(204)
}

func (srv *server) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	since := 0
	if s := r.URL.Query().Get("since"); s != "" {
		fmt.Sscanf(s, "%d", &since)
	}

	ch := make(chan Event, 256)
	srv.s.mu.Lock()
	// Replay history strictly after `since`, then subscribe — under the lock so
	// no live event is missed or duplicated across the handoff.
	var backlog []Event
	for _, e := range srv.s.log {
		if e.Seq > since {
			backlog = append(backlog, e)
		}
	}
	srv.s.subs[ch] = struct{}{}
	srv.s.mu.Unlock()

	defer func() {
		srv.s.mu.Lock()
		delete(srv.s.subs, ch)
		srv.s.mu.Unlock()
	}()

	send := func(e Event) bool {
		b, _ := json.Marshal(e)
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return false
		}
		fl.Flush()
		return true
	}
	fmt.Fprint(w, ": connected\n\n")
	fl.Flush()
	for _, e := range backlog {
		if !send(e) {
			return
		}
	}
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		case e := <-ch:
			if !send(e) {
				return
			}
		}
	}
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

const idAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = idAlphabet[int(b[i])%len(idAlphabet)]
	}
	return string(b)
}

func main() {
	port := flag.Int("port", 4800, "http port")
	dir := flag.String("data", "./v0-data", "data directory")
	flag.Parse()

	store, err := newStore(*dir)
	if err != nil {
		log.Fatal(err)
	}
	srv := &server{s: store}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		b, _ := ui.ReadFile("ui.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(b)
	})
	mux.HandleFunc("GET /api/tree", srv.tree)
	mux.HandleFunc("GET /api/context", srv.ctx)
	mux.HandleFunc("POST /api/nodes", srv.createNode)
	mux.HandleFunc("PATCH /api/nodes/{id}", srv.patchNode)
	mux.HandleFunc("DELETE /api/nodes/{id}", srv.deleteNode)
	mux.HandleFunc("POST /api/edges", srv.addEdge)
	mux.HandleFunc("POST /api/reparent", srv.reparent)
	mux.HandleFunc("POST /api/presence", srv.presence)
	mux.HandleFunc("GET /api/events", srv.events)

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("v0 outliner on http://localhost%s  data=%s", addr, *dir)
	log.Fatal(http.ListenAndServe(addr, mux))
}
