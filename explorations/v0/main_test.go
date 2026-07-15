package main

import (
	"encoding/json"
	"testing"
)

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
