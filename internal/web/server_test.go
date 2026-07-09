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

	// Unknown section on add → 409: an addressing failure (the section may
	// have been renamed under you), so the client refetches.
	w = do(t, h, "POST", "/api/board/abc-123/edit",
		`{"op":"add","section":"Nope","text":"x"}`)
	if w.Code != http.StatusConflict {
		t.Errorf("bad section: want 409, got %d %s", w.Code, w.Body)
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

// TestServedAssetsHaveWebFixes guards the client-side behaviors the served
// board/index pages must carry: the mindmap favicon on both pages, done items
// styled grey (no strikethrough) in both views, and the wrap/collapse/author
// wiring present. These are asserted on the embedded asset text so a stray edit
// that drops them fails CI without needing a browser.
func TestServedAssetsHaveWebFixes(t *testing.T) {
	h, _ := newTestServer(t)
	board := do(t, h, "GET", "/b/abc-123", "").Body.String()
	index := do(t, h, "GET", "/", "").Body.String()

	// Favicon: the mindmap mark (its teal center node color) on both pages.
	for name, page := range map[string]string{"board": board, "index": index} {
		if !strings.Contains(page, "fill='%2346B3A4'") {
			t.Errorf("%s page missing mindmap favicon", name)
		}
	}

	// Done items: grey, no line-through, in both outline and map.
	if strings.Contains(board, "text-decoration: line-through") &&
		strings.Contains(board, ".done > .row .text { color: var(--dim); text-decoration: line-through") {
		t.Error("outline done item still uses line-through")
	}
	if strings.Contains(board, ".mapnode.done { color: var(--dim); text-decoration: line-through") {
		t.Error("map done node still uses line-through")
	}

	// Wrap toggle, section collapse, author tint, and the map focus-root nav fix.
	for _, want := range []string{
		"function toggleWrap(",
		"function toggleSectionCollapse(",
		"function tintAuthor(",
		"n.parent.key === mapState.root",                 // focus-mode sibling filtering
		"n.kind === 'center' || n.key === mapState.root", // focus-root lateral nav
		"function searchMatches(",                        // incremental search core
		"function startSearch(",                          // `/` opens the prompt
		"function searchNext(",                           // n/N stepping, wrapping
		"searchprompt",                                   // the shared search overlay
	} {
		if !strings.Contains(board, want) {
			t.Errorf("board page missing %q", want)
		}
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

func TestReplySemantics(t *testing.T) {
	h, path := newTestServer(t)
	if err := os.WriteFile(path, []byte(`---
session: abc-123
cwd: /tmp/proj
started: 2026-07-09T10:00:00Z
---

## Questions
- [ ] q? @user
  - user: first
`), 0o644); err != nil {
		t.Fatal(err)
	}

	// reply on a reply stays flat: sibling under the top item.
	w := do(t, h, "POST", "/api/board/abc-123/edit",
		`{"op":"reply","section":"Questions","raw":"  - user: first","text":"claude: second"}`)
	if w.Code != 204 {
		t.Fatalf("reply: %d %s", w.Code, w.Body)
	}
	if !strings.Contains(boardFile(t, path), "\n  - claude: second\n") {
		t.Fatalf("flat reply missing:\n%s", boardFile(t, path))
	}

	// fork on the same reply nests beneath it.
	w = do(t, h, "POST", "/api/board/abc-123/edit",
		`{"op":"fork","section":"Questions","raw":"  - user: first","text":"claude: aside"}`)
	if w.Code != 204 {
		t.Fatalf("fork: %d %s", w.Code, w.Body)
	}
	if !strings.Contains(boardFile(t, path), "\n    - claude: aside\n") {
		t.Fatalf("fork not nested:\n%s", boardFile(t, path))
	}
}

func TestSectionOps(t *testing.T) {
	h, path := newTestServer(t)

	// add-section
	w := do(t, h, "POST", "/api/board/abc-123/edit", `{"op":"add-section","text":"Spikes"}`)
	if w.Code != 204 {
		t.Fatalf("add-section: %d %s", w.Code, w.Body)
	}
	if !strings.Contains(boardFile(t, path), "## Spikes") {
		t.Fatalf("section missing:\n%s", boardFile(t, path))
	}
	// duplicate refused
	if w = do(t, h, "POST", "/api/board/abc-123/edit", `{"op":"add-section","text":"Spikes"}`); w.Code != 400 {
		t.Errorf("duplicate add-section: want 400, got %d", w.Code)
	}

	// rename, collision refused
	if w = do(t, h, "POST", "/api/board/abc-123/edit", `{"op":"rename-section","section":"Spikes","text":"Experiments"}`); w.Code != 204 {
		t.Fatalf("rename-section: %d %s", w.Code, w.Body)
	}
	if !strings.Contains(boardFile(t, path), "## Experiments") {
		t.Fatalf("rename missing:\n%s", boardFile(t, path))
	}
	if w = do(t, h, "POST", "/api/board/abc-123/edit", `{"op":"rename-section","section":"Experiments","text":"Log"}`); w.Code != 400 {
		t.Errorf("rename collision: want 400, got %d", w.Code)
	}

	// archive-section moves contents; Log is guarded
	if w = do(t, h, "POST", "/api/board/abc-123/edit", `{"op":"add","section":"Experiments","text":"try it"}`); w.Code != 204 {
		t.Fatal("seed item")
	}
	if w = do(t, h, "POST", "/api/board/abc-123/edit", `{"op":"archive-section","section":"Experiments"}`); w.Code != 204 {
		t.Fatalf("archive-section: %d %s", w.Code, w.Body)
	}
	content := boardFile(t, path)
	if strings.Contains(content, "## Experiments") || !strings.Contains(content, "## Archive") {
		t.Fatalf("archive-section wrong:\n%s", content)
	}
	if w = do(t, h, "POST", "/api/board/abc-123/edit", `{"op":"archive-section","section":"Log"}`); w.Code != 400 {
		t.Errorf("archiving Log: want 400, got %d", w.Code)
	}

	// delete-section
	if w = do(t, h, "POST", "/api/board/abc-123/edit", `{"op":"delete-section","section":"Parked"}`); w.Code != 204 {
		t.Fatalf("delete-section: %d %s", w.Code, w.Body)
	}
	if strings.Contains(boardFile(t, path), "## Parked") {
		t.Error("Parked still present after delete-section")
	}
}

func TestUndoRedo(t *testing.T) {
	h, path := newTestServer(t)
	orig := boardFile(t, path)

	if w := do(t, h, "POST", "/api/board/abc-123/edit", `{"op":"add","section":"Ideas","text":"one"}`); w.Code != 204 {
		t.Fatal("add one")
	}
	afterOne := boardFile(t, path)
	if w := do(t, h, "POST", "/api/board/abc-123/edit", `{"op":"add","section":"Ideas","text":"two"}`); w.Code != 204 {
		t.Fatal("add two")
	}

	// undo twice walks back both edits
	if w := do(t, h, "POST", "/api/board/abc-123/edit", `{"op":"undo"}`); w.Code != 204 {
		t.Fatalf("undo 1: %d %s", w.Code, w.Body)
	}
	if got := boardFile(t, path); got != afterOne {
		t.Fatalf("undo 1: got\n%s\nwant\n%s", got, afterOne)
	}
	if w := do(t, h, "POST", "/api/board/abc-123/edit", `{"op":"undo"}`); w.Code != 204 {
		t.Fatalf("undo 2: %d %s", w.Code, w.Body)
	}
	if got := boardFile(t, path); got != orig {
		t.Fatalf("undo 2 did not restore original")
	}

	// redo replays
	if w := do(t, h, "POST", "/api/board/abc-123/edit", `{"op":"redo"}`); w.Code != 204 {
		t.Fatalf("redo: %d %s", w.Code, w.Body)
	}
	if got := boardFile(t, path); got != afterOne {
		t.Fatalf("redo did not reapply first edit")
	}

	// an external write (Claude, TUI) between edit and undo refuses with 409
	if err := os.WriteFile(path, []byte(afterOne+"- external\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if w := do(t, h, "POST", "/api/board/abc-123/edit", `{"op":"undo"}`); w.Code != http.StatusConflict {
		t.Errorf("undo over external write: want 409, got %d %s", w.Code, w.Body)
	}

	// empty stack refuses politely
	h2, _ := newTestServer(t)
	if w := do(t, h2, "POST", "/api/board/abc-123/edit", `{"op":"undo"}`); w.Code != 400 {
		t.Errorf("undo on empty history: want 400, got %d", w.Code)
	}
}

func TestNotesAPI(t *testing.T) {
	h, _ := newTestServer(t)

	// missing note reads as a seeded draft, not an error, and is not created
	w := do(t, h, "GET", "/api/board/abc-123/note/design", "")
	if w.Code != 200 {
		t.Fatalf("note get: %d %s", w.Code, w.Body)
	}
	var n struct {
		Content string `json:"content"`
		Exists  bool   `json:"exists"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &n); err != nil {
		t.Fatal(err)
	}
	if n.Exists || !strings.HasPrefix(n.Content, "# design") {
		t.Errorf("seed: %+v", n)
	}
	notePath := filepath.Join(board.BoardsDir(), "abc-123.notes", "design.md")
	if _, err := os.Stat(notePath); !os.IsNotExist(err) {
		t.Error("GET must not create the note")
	}

	// save creates it
	w = do(t, h, "POST", "/api/board/abc-123/note/design", `{"content":"# design\n\nhello\n"}`)
	if w.Code != 204 {
		t.Fatalf("note save: %d %s", w.Code, w.Body)
	}
	data, err := os.ReadFile(notePath)
	if err != nil || !strings.Contains(string(data), "hello") {
		t.Fatalf("note content: %q err %v", data, err)
	}

	// path-form and traversal names are refused at the HTTP layer (".."
	// alone never reaches the handler — the mux 307-normalizes it away)...
	for _, bad := range []string{"a..b", "~x"} {
		if w = do(t, h, "GET", "/api/board/abc-123/note/"+bad, ""); w.Code != 400 {
			t.Errorf("bad note name %q: want 400, got %d", bad, w.Code)
		}
	}
	if w = do(t, h, "GET", "/api/board/abc-123/note/with%2Fslash", ""); w.Code == 200 {
		t.Error("path-form note name must not be served")
	}
	// ...and the validator itself refuses every escape shape directly.
	bd := &board.Board{}
	for _, bad := range []string{"", "..", "a..b", "a/b", "/abs", "~home", `a\b`} {
		if _, err := noteFile(bd, bad); err == nil {
			t.Errorf("noteFile(%q): expected error", bad)
		}
	}
}

func TestRawAndSetContent(t *testing.T) {
	h, path := newTestServer(t)

	// GET raw returns the verbatim file
	w := do(t, h, "GET", "/api/board/abc-123/raw", "")
	if w.Code != 200 {
		t.Fatalf("raw: %d %s", w.Code, w.Body)
	}
	var raw struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if raw.Content != boardFile(t, path) {
		t.Fatal("raw != file")
	}

	// set-content with a matching base rewrites the file (newline ensured)
	newContent := raw.Content + "\n## Scratch\n- [ ] free-form"
	body, _ := json.Marshal(map[string]string{"op": "set-content", "text": newContent, "base": raw.Content})
	if w = do(t, h, "POST", "/api/board/abc-123/edit", string(body)); w.Code != 204 {
		t.Fatalf("set-content: %d %s", w.Code, w.Body)
	}
	if got := boardFile(t, path); got != newContent+"\n" {
		t.Fatalf("set-content wrote:\n%q\nwant:\n%q", got, newContent+"\n")
	}

	// a stale base (someone wrote since the editor loaded) is refused with 409
	body, _ = json.Marshal(map[string]string{"op": "set-content", "text": "clobber\n", "base": raw.Content})
	if w = do(t, h, "POST", "/api/board/abc-123/edit", string(body)); w.Code != http.StatusConflict {
		t.Fatalf("stale set-content: want 409, got %d %s", w.Code, w.Body)
	}
	if strings.Contains(boardFile(t, path), "clobber") {
		t.Fatal("stale set-content clobbered the board")
	}

	// set-content participates in undo
	if w = do(t, h, "POST", "/api/board/abc-123/edit", `{"op":"undo"}`); w.Code != 204 {
		t.Fatalf("undo set-content: %d %s", w.Code, w.Body)
	}
	if got := boardFile(t, path); got != raw.Content {
		t.Fatal("undo did not restore pre-set-content board")
	}
}

func TestHistoryEndpoint(t *testing.T) {
	h, _ := newTestServer(t)

	// Empty journal: empty list, not an error.
	w := do(t, h, "GET", "/api/board/abc-123/history", "")
	if w.Code != 200 {
		t.Fatalf("history: %d %s", w.Code, w.Body)
	}

	if w = do(t, h, "POST", "/api/board/abc-123/edit", `{"op":"add","section":"Plan","text":"first"}`); w.Code != 204 {
		t.Fatal("seed 1")
	}
	if w = do(t, h, "POST", "/api/board/abc-123/edit", `{"op":"add","section":"Plan","text":"second"}`); w.Code != 204 {
		t.Fatal("seed 2")
	}

	w = do(t, h, "GET", "/api/board/abc-123/history", "")
	var resp struct {
		Entries []struct {
			Author  string   `json:"author"`
			Added   []string `json:"added"`
			Removed []string `json:"removed"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Entries) != 2 {
		t.Fatalf("entries: %d", len(resp.Entries))
	}
	// Newest first, authored "web", diffed to the added line.
	if resp.Entries[0].Author != "web" || len(resp.Entries[0].Added) != 1 ||
		resp.Entries[0].Added[0] != "- [ ] second" || len(resp.Entries[0].Removed) != 0 {
		t.Fatalf("newest entry: %+v", resp.Entries[0])
	}
	if resp.Entries[1].Added[0] != "- [ ] first" {
		t.Fatalf("oldest entry: %+v", resp.Entries[1])
	}
}

func TestAuthToken(t *testing.T) {
	h, _ := newTestServer(t)
	// newTestServer builds an un-tokened handler; rebuild with a token.
	s := New()
	s.SetToken("s3cret")
	h = s.Handler()

	// No credentials → 401, for pages and API alike.
	for _, url := range []string{"/", "/api/boards", "/b/abc-123"} {
		if w := do(t, h, "GET", url, ""); w.Code != http.StatusUnauthorized {
			t.Errorf("%s without token: want 401, got %d", url, w.Code)
		}
	}

	// Bearer header works.
	req := httptest.NewRequest("GET", "/api/boards", nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("bearer: want 200, got %d", w.Code)
	}

	// Wrong bearer refused.
	req = httptest.NewRequest("GET", "/api/boards", nil)
	req.Header.Set("Authorization", "Bearer nope")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("bad bearer: want 401, got %d", w.Code)
	}

	// ?token= sets the cookie and redirects to a clean URL.
	w = do(t, h, "GET", "/b/abc-123?token=s3cret", "")
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/b/abc-123" {
		t.Fatalf("token param: %d %s", w.Code, w.Header().Get("Location"))
	}
	var cookie string
	for _, c := range w.Result().Cookies() {
		if c.Name == authCookie {
			cookie = c.Value
		}
	}
	if cookie != "s3cret" {
		t.Fatalf("cookie not set: %q", cookie)
	}

	// The cookie then admits everything (this is how SSE authenticates).
	req = httptest.NewRequest("GET", "/api/boards", nil)
	req.AddCookie(&http.Cookie{Name: authCookie, Value: cookie})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("cookie: want 200, got %d", w.Code)
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
