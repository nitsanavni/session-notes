package cloud

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/nitsanavni/session-notes/internal/board"
)

// postEdit posts an editReq to a server and returns the status code.
func postEdit(t *testing.T, base, boardID string, r editReq) int {
	t.Helper()
	buf, _ := json.Marshal(r)
	resp, err := http.Post(base+"/api/board/"+boardID+"/edit", "application/json", bytes.NewReader(buf))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

func TestLoginTokenStorage(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SESSION_NOTES_CONFIG_DIR", dir)

	if got := TokenFor("host:1"); got != "" {
		t.Fatalf("expected no token, got %q", got)
	}
	if err := SaveToken("host:1", "secret"); err != nil {
		t.Fatal(err)
	}
	if got := TokenFor("host:1"); got != "secret" {
		t.Fatalf("TokenFor=%q want secret", got)
	}
	// Overwrite for the same host.
	if err := SaveToken("host:1", "secret2"); err != nil {
		t.Fatal(err)
	}
	if got := TokenFor("host:1"); got != "secret2" {
		t.Fatalf("TokenFor=%q want secret2", got)
	}
	// A second host is independent.
	_ = SaveToken("host:2", "other")
	if got := TokenFor("host:1"); got != "secret2" {
		t.Fatalf("host:1 clobbered: %q", got)
	}

	// File must be 0600.
	fi, err := os.Stat(filepath.Join(dir, "tokens.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("perm=%v want 0600", fi.Mode().Perm())
	}
}

func TestLinkStorage(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("SESSION_NOTES_CONFIG_DIR", cfg)

	if _, _, ok := LinkFor("/work/proj"); ok {
		t.Fatal("expected no link initially")
	}
	if err := SaveLink("/work/proj", "/boards/x.md"); err != nil {
		t.Fatal(err)
	}
	ref, src, ok := LinkFor("/work/proj")
	if !ok || ref != "/boards/x.md" || src != "/work/proj" {
		t.Fatalf("LinkFor exact: ref=%q src=%q ok=%v", ref, src, ok)
	}

	// Parent-dir walk (git-style): a child dir resolves the ancestor's link.
	ref, src, ok = LinkFor("/work/proj/a/b/c")
	if !ok || ref != "/boards/x.md" || src != "/work/proj" {
		t.Fatalf("LinkFor walk: ref=%q src=%q ok=%v", ref, src, ok)
	}

	// A nearer link wins over the ancestor.
	if err := SaveLink("/work/proj/a", "/boards/near.md"); err != nil {
		t.Fatal(err)
	}
	ref, src, _ = LinkFor("/work/proj/a/b")
	if ref != "/boards/near.md" || src != "/work/proj/a" {
		t.Fatalf("nearest link: ref=%q src=%q", ref, src)
	}

	// File must be 0600.
	fi, err := os.Stat(filepath.Join(cfg, "links.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("perm=%v want 0600", fi.Mode().Perm())
	}

	// Unlink removes only the target entry.
	removed, err := RemoveLink("/work/proj")
	if err != nil || !removed {
		t.Fatalf("RemoveLink=%v err=%v", removed, err)
	}
	if _, _, ok := LinkFor("/work/proj"); ok {
		// The ancestor is gone but the nearer /work/proj/a link still resolves
		// for its own subtree.
		t.Fatal("expected /work/proj link removed")
	}
	if _, _, ok := LinkFor("/work/proj/a/b"); !ok {
		t.Fatal("nearer link should survive unlink of ancestor")
	}
	if removed, _ := RemoveLink("/work/proj"); removed {
		t.Fatal("second RemoveLink should report not-found")
	}
}

func TestAuthorsSince(t *testing.T) {
	s := newStore(t)
	_ = s.CreateBoard("b", "# b\n\n## Threads\n")
	_, v0, _ := s.Get("b")
	if _, err := s.ApplyOp("b", board.Op{Name: "add", Section: "Threads", Text: "one"}, -1, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyOp("b", board.Op{Name: "add", Section: "Threads", Text: "two"}, -1, "bob"); err != nil {
		t.Fatal(err)
	}
	authors, err := s.AuthorsSince("b", v0)
	if err != nil {
		t.Fatal(err)
	}
	if len(authors) != 2 || authors[0] != "alice" || authors[1] != "bob" {
		t.Fatalf("AuthorsSince=%v", authors)
	}
	// Nothing after the latest version.
	_, vNow, _ := s.Get("b")
	if a, _ := s.AuthorsSince("b", vNow); len(a) != 0 {
		t.Fatalf("expected no authors after latest, got %v", a)
	}
}

func TestParseRef(t *testing.T) {
	cases := []struct {
		in          string
		server, brd string
		node        string
	}{
		{"https://h.example/b/myboard", "https://h.example", "myboard", ""},
		{"https://h.example:8080/b/x#node9", "https://h.example:8080", "x", "node9"},
		{"http://localhost:7099/api/board/y", "http://localhost:7099", "y", ""},
	}
	for _, c := range cases {
		ref, err := ParseRef(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if ref.Server != c.server || ref.Board != c.brd || ref.Node != c.node {
			t.Fatalf("%s: got %+v", c.in, ref)
		}
	}
	if !IsRemote("https://x/b/y") || IsRemote("/tmp/board.md") {
		t.Fatal("IsRemote misclassified")
	}
}
