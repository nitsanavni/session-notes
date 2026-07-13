package cloud

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nitsanavni/session-notes/internal/board"
)

// TestTokenListAndRevoke round-trips the token-lifecycle store operations that
// back `server token list` / `server token revoke`.
func TestTokenListAndRevoke(t *testing.T) {
	s := newStore(t)
	if _, err := s.CreateToken("ops"); err != nil { // admin bootstrap
		t.Fatal(err)
	}
	scoped, err := s.CreateTokenAs("scout", "scout", false)
	if err != nil {
		t.Fatal(err)
	}

	toks, err := s.ListTokens()
	if err != nil {
		t.Fatal(err)
	}
	if len(toks) != 2 {
		t.Fatalf("ListTokens=%d want 2", len(toks))
	}
	var sawAdmin, sawScoped bool
	for _, tk := range toks {
		switch tk.Name {
		case "ops":
			sawAdmin = tk.Admin
		case "scout":
			sawScoped = !tk.Admin
		}
	}
	if !sawAdmin || !sawScoped {
		t.Fatalf("unexpected token list: %+v", toks)
	}

	// The scoped token validates until revoked, then does not.
	if !s.ValidToken(scoped) {
		t.Fatal("scoped token should validate before revoke")
	}
	n, err := s.RevokeToken("scout")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("revoked %d want 1", n)
	}
	if s.ValidToken(scoped) {
		t.Fatal("revoked token still validates")
	}
	if got, _ := s.RevokeToken("nope"); got != 0 {
		t.Fatalf("revoke unknown removed %d want 0", got)
	}
}

// TestHealthzNoAuth verifies the liveness probe answers 200 without any token
// even on a secured server, so load balancers and sidecars can reach it.
func TestHealthzNoAuth(t *testing.T) {
	s := newStore(t)
	ts := httptest.NewServer(NewServer(s, false).Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status=%d want 200", resp.StatusCode)
	}
}

// TestBodySizeGuard413 checks that an oversized POST is refused 413 before the
// handler runs.
func TestBodySizeGuard413(t *testing.T) {
	s := newStore(t)
	_ = s.CreateBoard("b1", seed)
	ts := httptest.NewServer(NewServer(s, true).Handler()) // insecure: isolate body guard
	t.Cleanup(ts.Close)

	big := bytes.Repeat([]byte("a"), maxBodyBytes+1024)
	resp, err := http.Post(ts.URL+"/api/board/b1/edit", "application/json", bytes.NewReader(big))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body status=%d want 413", resp.StatusCode)
	}
}

// TestExportReparseable verifies each stored board renders to markdown that
// parses back to an equivalent board (the format-durability guarantee behind
// `server export`).
func TestExportReparseable(t *testing.T) {
	s := newStore(t)
	if err := s.CreateBoard("alpha", seed); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBoard("beta", seed); err != nil {
		t.Fatal(err)
	}
	ids, err := s.ListBoards()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("ListBoards=%d want 2", len(ids))
	}
	for _, id := range ids {
		content, _, err := s.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		rendered := board.Parse(content).Render()
		// Re-parse the exported form and confirm the Threads section survives.
		reparsed := board.Parse(rendered)
		if reparsed.Section("Threads") == nil {
			t.Fatalf("exported board %s lost its Threads section:\n%s", id, rendered)
		}
		if !strings.Contains(rendered, "first task") {
			t.Fatalf("exported board %s lost its item:\n%s", id, rendered)
		}
	}
}
