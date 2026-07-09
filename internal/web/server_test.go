package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nitsanavni/session-notes/internal/board"
)

// newTestServer points the boards dir at a temp dir, writes one board there,
// and returns the handler plus the board's path.
func newTestServer(t *testing.T) (http.Handler, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SESSION_NOTES_DIR", dir)
	// Keep liveness scans away from the real ~/.claude/projects.
	t.Setenv("SESSION_NOTES_PROJECTS_DIR", filepath.Join(dir, "projects"))
	path := filepath.Join(dir, "abc-123.md")
	content := board.Template("abc-123", "/tmp/proj", time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return New().Handler(), path
}

func do(t *testing.T, h http.Handler, method, url, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, url, nil)
	} else {
		req = httptest.NewRequest(method, url, strings.NewReader(body))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func boardFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestBoardJSON(t *testing.T) {
	h, path := newTestServer(t)
	if err := os.WriteFile(path, []byte(`---
session: abc-123
cwd: /tmp/proj
started: 2026-07-09T10:00:00Z
title: auth refactor
---

## Threads
- [>] extracting middleware
  - claude: done with part one

## Questions
- [ ] !! drop the legacy endpoint? @user

## Log
- 10:00 start: session started
`), 0o644); err != nil {
		t.Fatal(err)
	}

	w := do(t, h, "GET", "/api/board/abc-123", "")
	if w.Code != 200 {
		t.Fatalf("GET board: %d %s", w.Code, w.Body)
	}
	var b boardJSON
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	if b.Title != "auth refactor" || b.Session != "abc-123" {
		t.Errorf("frontmatter: %+v", b)
	}
	var threads *sectionJSON
	for i := range b.Sections {
		if b.Sections[i].Title == "Threads" {
			threads = &b.Sections[i]
		}
	}
	if threads == nil || len(threads.Items) != 1 {
		t.Fatalf("threads section: %+v", threads)
	}
	it := threads.Items[0]
	if it.Status != "wip" || it.Text != "extracting middleware" {
		t.Errorf("item: %+v", it)
	}
	if len(it.Children) != 1 || it.Children[0].Text != "claude: done with part one" {
		t.Errorf("reply: %+v", it.Children)
	}
	// Urgent flag survives the trip.
	for _, s := range b.Sections {
		if s.Title == "Questions" && (len(s.Items) != 1 || !s.Items[0].Urgent) {
			t.Errorf("urgent question: %+v", s.Items)
		}
	}
}

func TestEditOps(t *testing.T) {
	h, path := newTestServer(t)

	// add
	w := do(t, h, "POST", "/api/board/abc-123/edit",
		`{"op":"add","section":"Threads","text":"port the web ui"}`)
	if w.Code != 204 {
		t.Fatalf("add: %d %s", w.Code, w.Body)
	}
	if !strings.Contains(boardFile(t, path), "- [ ] port the web ui") {
		t.Fatalf("add not in file:\n%s", boardFile(t, path))
	}

	// status by raw line
	w = do(t, h, "POST", "/api/board/abc-123/edit",
		`{"op":"status","section":"Threads","raw":"- [ ] port the web ui","status":"wip"}`)
	if w.Code != 204 {
		t.Fatalf("status: %d %s", w.Code, w.Body)
	}
	if !strings.Contains(boardFile(t, path), "- [>] port the web ui") {
		t.Fatalf("status not applied:\n%s", boardFile(t, path))
	}

	// reply under the (now changed) line via normalized-text fallback: address
	// it by the stale raw the client last saw.
	w = do(t, h, "POST", "/api/board/abc-123/edit",
		`{"op":"reply","section":"Threads","raw":"- [ ] port the web ui","text":"user: nice"}`)
	if w.Code != 204 {
		t.Fatalf("reply fallback: %d %s", w.Code, w.Body)
	}
	if !strings.Contains(boardFile(t, path), "  - user: nice") {
		t.Fatalf("reply not in file:\n%s", boardFile(t, path))
	}

	// edit text
	w = do(t, h, "POST", "/api/board/abc-123/edit",
		`{"op":"edit","section":"Threads","raw":"- [>] port the web ui","text":"port the web ui (v1)"}`)
	if w.Code != 204 {
		t.Fatalf("edit: %d %s", w.Code, w.Body)
	}
	if !strings.Contains(boardFile(t, path), "- [>] port the web ui (v1)") {
		t.Fatalf("edit not applied:\n%s", boardFile(t, path))
	}

	// urgent toggle
	w = do(t, h, "POST", "/api/board/abc-123/edit",
		`{"op":"urgent","section":"Threads","raw":"- [>] port the web ui (v1)","urgent":true}`)
	if w.Code != 204 {
		t.Fatalf("urgent: %d %s", w.Code, w.Body)
	}
	if !strings.Contains(boardFile(t, path), "- [>] !! port the web ui (v1)") {
		t.Fatalf("urgent not applied:\n%s", boardFile(t, path))
	}

	// log with default author
	w = do(t, h, "POST", "/api/board/abc-123/edit", `{"op":"log","text":"tried the web ui"}`)
	if w.Code != 204 {
		t.Fatalf("log: %d %s", w.Code, w.Body)
	}
	if !strings.Contains(boardFile(t, path), "user: tried the web ui") {
		t.Fatalf("log not in file:\n%s", boardFile(t, path))
	}

	// title
	w = do(t, h, "POST", "/api/board/abc-123/edit", `{"op":"title","text":"web ui spike"}`)
	if w.Code != 204 {
		t.Fatalf("title: %d %s", w.Code, w.Body)
	}
	if !strings.Contains(boardFile(t, path), "title: web ui spike") {
		t.Fatalf("title not in file:\n%s", boardFile(t, path))
	}

	// archive moves the item (and its reply) into ## Archive
	w = do(t, h, "POST", "/api/board/abc-123/edit",
		`{"op":"archive","section":"Threads","raw":"- [>] !! port the web ui (v1)"}`)
	if w.Code != 204 {
		t.Fatalf("archive: %d %s", w.Code, w.Body)
	}
	content := boardFile(t, path)
	if !strings.Contains(content, "## Archive") || !strings.Contains(content, "  - user: nice") {
		t.Fatalf("archive lost content:\n%s", content)
	}
}

func TestEditErrors(t *testing.T) {
	h, _ := newTestServer(t)

	// Missing item → 409 so the client refetches.
	w := do(t, h, "POST", "/api/board/abc-123/edit",
		`{"op":"status","section":"Threads","raw":"- [ ] no such item","status":"done"}`)
	if w.Code != http.StatusConflict {
		t.Errorf("missing item: want 409, got %d %s", w.Code, w.Body)
	}

	// Unknown section on add → 400.
	w = do(t, h, "POST", "/api/board/abc-123/edit",
		`{"op":"add","section":"Nope","text":"x"}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("bad section: want 400, got %d %s", w.Code, w.Body)
	}

	// Unknown op → 400.
	w = do(t, h, "POST", "/api/board/abc-123/edit", `{"op":"frobnicate"}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("bad op: want 400, got %d %s", w.Code, w.Body)
	}

	// Unknown board → 404.
	w = do(t, h, "POST", "/api/board/nope/edit", `{"op":"log","text":"x"}`)
	if w.Code != http.StatusNotFound {
		t.Errorf("bad board: want 404, got %d %s", w.Code, w.Body)
	}

	// Path traversal in the id must not resolve.
	w = do(t, h, "GET", "/api/board/..%2Fabc-123", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("traversal id: want 404, got %d", w.Code)
	}
}

func TestDashboardList(t *testing.T) {
	h, _ := newTestServer(t)
	w := do(t, h, "GET", "/api/boards", "")
	if w.Code != 200 {
		t.Fatalf("boards: %d %s", w.Code, w.Body)
	}
	var resp struct {
		Boards []cardJSON `json:"boards"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Boards) != 1 || resp.Boards[0].ID != "abc-123" {
		t.Fatalf("cards: %+v", resp.Boards)
	}
	if resp.Boards[0].Liveness != "gone" {
		t.Errorf("liveness with no transcript: want gone, got %q", resp.Boards[0].Liveness)
	}
}

func TestPagesServe(t *testing.T) {
	h, _ := newTestServer(t)
	if w := do(t, h, "GET", "/", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "session dashboard") {
		t.Errorf("index: %d", w.Code)
	}
	if w := do(t, h, "GET", "/b/abc-123", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "EventSource") {
		t.Errorf("board page: %d", w.Code)
	}
	if w := do(t, h, "GET", "/b/nope", ""); w.Code != http.StatusNotFound {
		t.Errorf("missing board page: want 404, got %d", w.Code)
	}
}

func TestRegisterAndHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SESSION_NOTES_DIR", filepath.Join(dir, "boards")) // empty boards dir
	t.Setenv("SESSION_NOTES_PROJECTS_DIR", filepath.Join(dir, "projects"))
	path := filepath.Join(dir, "elsewhere.md")
	content := board.Template("xyz-9", "/tmp/p", time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	s := New()
	id, err := s.Register(path)
	if err != nil {
		t.Fatal(err)
	}
	if id != "xyz-9" {
		t.Fatalf("id: %q", id)
	}
	s.SetHome(id)
	h := s.Handler()

	if w := do(t, h, "GET", "/", ""); w.Code != http.StatusFound || w.Header().Get("Location") != "/b/xyz-9" {
		t.Errorf("home redirect: %d %s", w.Code, w.Header().Get("Location"))
	}
	if w := do(t, h, "GET", "/api/board/xyz-9", ""); w.Code != 200 {
		t.Errorf("registered board: %d %s", w.Code, w.Body)
	}
	// Edits reach the registered path too.
	if w := do(t, h, "POST", "/api/board/xyz-9/edit", `{"op":"log","text":"hi"}`); w.Code != 204 {
		t.Errorf("edit registered: %d %s", w.Code, w.Body)
	}
	if !strings.Contains(boardFile(t, path), "user: hi") {
		t.Error("edit did not land in registered file")
	}
}

func TestFileStamp(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.md")
	if fileStamp(p) != "" {
		t.Error("missing file should stamp empty")
	}
	if err := os.WriteFile(p, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	s1 := fileStamp(p)
	if s1 == "" {
		t.Fatal("stamp empty for existing file")
	}
	// Same mtime granularity issues are avoided by also hashing size.
	if err := os.WriteFile(p, []byte("ab"), 0o644); err != nil {
		t.Fatal(err)
	}
	if s2 := fileStamp(p); s2 == s1 {
		t.Error("stamp unchanged after write")
	}
}
