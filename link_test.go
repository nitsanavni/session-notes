package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nitsanavni/session-notes/internal/board"
	"github.com/nitsanavni/session-notes/internal/cloud"
)

// TestLinkedBoardPriority: an explicit --board (or --session) always beats a
// directory link; the link only fills in when both are absent.
func TestLinkedBoardPriority(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("SESSION_NOTES_CONFIG_DIR", cfg)
	work := t.TempDir()
	t.Chdir(work)

	boardFile := filepath.Join(work, "b.md")
	if err := cloud.SaveLink(work, boardFile+"#node9"); err != nil {
		t.Fatal(err)
	}

	// Explicit --board wins, no fragment from the link.
	if ref, node := linkedBoard("/explicit/path.md", ""); ref != "/explicit/path.md" || node != "" {
		t.Fatalf("explicit board should win: ref=%q node=%q", ref, node)
	}
	// Explicit --session wins (returns the empty board so editBoardPath resolves it).
	if ref, node := linkedBoard("", "sess1"); ref != "" || node != "" {
		t.Fatalf("explicit session should defer to session resolution: ref=%q node=%q", ref, node)
	}
	// No flags: link resolves, and a local ref's #fragment becomes the node.
	if ref, node := linkedBoard("", ""); ref != boardFile || node != "node9" {
		t.Fatalf("link fallback: ref=%q node=%q want %q node9", ref, boardFile, node)
	}
}

// TestLinkedBoardRemoteFragment: a remote linked ref keeps its #fragment in the
// string (ParseRef extracts it), so linkedBoard returns no separate node.
func TestLinkedBoardRemoteFragment(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("SESSION_NOTES_CONFIG_DIR", cfg)
	work := t.TempDir()
	t.Chdir(work)

	remote := "https://host/b/board#abc123"
	if err := cloud.SaveLink(work, remote); err != nil {
		t.Fatal(err)
	}
	ref, node := linkedBoard("", "")
	if ref != remote || node != "" {
		t.Fatalf("remote link: ref=%q node=%q (fragment stays in ref)", ref, node)
	}
}

func decodeChanges(t *testing.T, out string) []jsonChange {
	t.Helper()
	var got []jsonChange
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		var c jsonChange
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			t.Fatalf("invalid JSON line %q: %v", line, err)
		}
		got = append(got, c)
	}
	return got
}

// TestWatchJSONLocal: the local file watcher in --json mode emits one JSON line
// per item change, with the anchor stripped into id and the ref in board.
func TestWatchJSONLocal(t *testing.T) {
	w, boardPath := newWatchFixture(t, "# board\n- [ ] one ^aaaaaa\n")
	w.jsonOut = true
	w.ref = boardPath
	if err := w.init(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(boardPath, []byte("# board\n- [ ] one ^aaaaaa\n- [ ] two ^bbbbbb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, changed, _, err := w.poll()
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected change")
	}
	got := decodeChanges(t, out)
	if len(got) != 1 {
		t.Fatalf("expected 1 change, got %d: %q", len(got), out)
	}
	c := got[0]
	if c.Op != "add" || c.Line != "- [ ] two" || c.ID != "bbbbbb" || c.Board != boardPath || c.Node != "" {
		t.Fatalf("unexpected change: %+v", c)
	}
}

// TestWatchJSONRemote: driving the remote watch's core (fetch + jsonDiff) against
// an httptest cloud server yields the same JSON line shape.
func TestWatchJSONRemote(t *testing.T) {
	store, err := cloud.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.CreateBoard("b1", "# board\n\n## Threads\n"); err != nil {
		t.Fatal(err)
	}
	tok, _ := store.CreateToken("t")
	ts := httptest.NewServer(cloud.NewServer(store, false).Handler())
	defer ts.Close()

	ref := ts.URL + "/b/b1"
	tree := cloud.NewRemoteTree(ts.URL, "b1", tok)
	prev, _, err := tree.Raw()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tree.Apply(board.Op{Name: "add", Section: "Threads", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	cur, _, err := tree.Raw()
	if err != nil {
		t.Fatal(err)
	}
	got := decodeChanges(t, jsonDiff(prev, cur, ref, ""))
	if len(got) != 1 {
		t.Fatalf("expected 1 change, got %d", len(got))
	}
	c := got[0]
	if c.Op != "add" || !strings.Contains(c.Line, "hello") || c.Board != ref || c.ID == "" {
		t.Fatalf("unexpected remote change: %+v", c)
	}
}
