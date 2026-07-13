package cloud

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
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
