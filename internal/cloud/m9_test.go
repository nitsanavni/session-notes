package cloud

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/nitsanavni/session-notes/internal/board"
)

// TestJournalStoresDiffsNotSnapshots verifies the M9 journal format: an op row
// carries the line-diff (added/removed) and no full before/after snapshot, and
// History reconstructs exactly the diff board.DiffLines would compute from the
// full snapshots — so /history's user-visible behavior is preserved.
func TestJournalStoresDiffsNotSnapshots(t *testing.T) {
	s := newStore(t)
	if err := s.CreateBoard("b1", seed); err != nil {
		t.Fatal(err)
	}
	before, _, _ := s.Get("b1")
	if _, err := s.ApplyOp("b1", board.Op{Name: "add", Section: "Threads", Text: "second"}, -1, "alice"); err != nil {
		t.Fatal(err)
	}
	after, _, _ := s.Get("b1")

	// No stored full snapshots.
	var nBloat int
	_ = s.db.QueryRow(`SELECT COUNT(1) FROM ops WHERE before<>'' OR after<>''`).Scan(&nBloat)
	if nBloat != 0 {
		t.Fatalf("expected 0 rows with full snapshots, got %d", nBloat)
	}

	hist, err := s.History("b1", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	// CreateBoard does not journal; the "second" add is the sole entry.
	if len(hist) != 1 {
		t.Fatalf("history len=%d want 1", len(hist))
	}
	wantAdded, wantRemoved := board.DiffLines(before, after)
	if !reflect.DeepEqual(hist[0].Added, wantAdded) || !reflect.DeepEqual(hist[0].Removed, wantRemoved) {
		t.Fatalf("diff mismatch: got +%v -%v, want +%v -%v", hist[0].Added, hist[0].Removed, wantAdded, wantRemoved)
	}
	if hist[0].Author != "alice" {
		t.Fatalf("author=%q want alice", hist[0].Author)
	}
}

// TestHistoryPagination verifies ?limit/?before params bound the /history query
// and page backward through the journal.
func TestHistoryPagination(t *testing.T) {
	s, ts := testServer(t, false)
	tok, _ := s.CreateToken("boss")
	tree := NewRemoteTree(ts.URL, "b1", tok)
	for i := 0; i < 10; i++ {
		if _, err := tree.Apply(board.Op{Name: "add", Section: "Threads", Text: fmt.Sprintf("t%d", i)}); err != nil {
			t.Fatal(err)
		}
	}

	get := func(query string) (entries []map[string]any, before float64, hasBefore bool) {
		req, _ := http.NewRequest("GET", ts.URL+"/api/board/b1/history"+query, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out struct {
			Entries []map[string]any `json:"entries"`
			Before  *float64         `json:"before"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		if out.Before != nil {
			return out.Entries, *out.Before, true
		}
		return out.Entries, 0, false
	}

	// First page of 4 (there are 11 total: 10 adds + creation).
	page1, cursor, hasCursor := get("?limit=4")
	if len(page1) != 4 {
		t.Fatalf("page1 len=%d want 4", len(page1))
	}
	if !hasCursor {
		t.Fatal("expected a before cursor on a full page")
	}
	// Next page via the cursor.
	page2, _, _ := get(fmt.Sprintf("?limit=4&before=%d", int(cursor)))
	if len(page2) != 4 {
		t.Fatalf("page2 len=%d want 4", len(page2))
	}
	// The two pages must not overlap (newest-first, strictly older).
	if fmt.Sprint(page1[len(page1)-1]) == fmt.Sprint(page2[0]) {
		t.Fatal("pages overlap")
	}

	// A short final page carries no cursor.
	_, _, last := get("?limit=100")
	if last {
		t.Fatal("short page should not advertise a before cursor")
	}
}

// TestGrantsRejectUnknownFields is the security-adjacent footgun fix: a typo'd
// field (e.g. "node" instead of "root") must 400, not silently mint a
// whole-board grant.
func TestGrantsRejectUnknownFields(t *testing.T) {
	s, ts := testServer(t, false)
	admin, _ := s.CreateToken("boss")

	post := func(body string) int {
		req, _ := http.NewRequest("POST", ts.URL+"/api/board/b1/grants", bytes.NewReader([]byte(body)))
		req.Header.Set("Authorization", "Bearer "+admin)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	// "node" is not a grant field — must be rejected, not treated as a
	// whole-board grant (root defaulting to "").
	if got := post(`{"newToken":"scout","perm":"write","node":"abc123"}`); got != http.StatusBadRequest {
		t.Fatalf("typo'd field: got %d want 400", got)
	}
	// No scout grant should have been created.
	if _, ok := s.GrantFor("scout", "b1"); ok {
		t.Fatal("unknown-field grant leaked a whole-board grant")
	}
	// The correct body still works.
	if got := post(`{"newToken":"scout","perm":"write","root":"abc123"}`); got != http.StatusOK {
		t.Fatalf("valid grant: got %d want 200", got)
	}
}

// TestSSEAuthorField verifies the SSE change event carries the author(s) of the
// change, so a remote `watch --json` event is attributed (previously empty).
func TestSSEAuthorField(t *testing.T) {
	s, ts := testServer(t, false)
	admin, _ := s.CreateToken("boss")
	// A distinct-subject writer so the author is unambiguous.
	writerTok, _ := s.CreateToken("carol")
	writer := NewRemoteTree(ts.URL, "b1", writerTok)

	watcher := NewRemoteTree(ts.URL, "b1", admin)
	ch, cancel, err := watcher.Watch("")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	// Give the SSE goroutine a moment to establish + snapshot prev.
	time.Sleep(100 * time.Millisecond)
	if _, err := writer.Apply(board.Op{Name: "add", Section: "Threads", Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-ch:
		if ev.Author != "carol" {
			t.Fatalf("event author=%q want carol", ev.Author)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no SSE event")
	}
}

// TestMigrateFatJournal seeds a legacy full-snapshot journal, reopens the db to
// trigger the M9 migration, and asserts the rows converted to diffs, the full
// snapshots were cleared, and the journal pruned to journalKeep rows — with the
// newest rows surviving and rendering correct diffs.
func TestMigrateFatJournal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fat.db")

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBoard("b1", seed); err != nil {
		t.Fatal(err)
	}

	// Simulate a pre-M9 fat journal: many rows carrying full before/after
	// snapshots, and reset the schema marker so reopen re-migrates.
	const legacy = 600
	now := time.Now().UnixNano()
	for i := 0; i < legacy; i++ {
		before := fmt.Sprintf("- [ ] item %d ^aaaaaa\n", i)
		after := fmt.Sprintf("- [x] item %d ^aaaaaa\n- [ ] extra %d ^bbbbbb\n", i, i)
		if _, err := s.db.Exec(
			`INSERT INTO ops(board_id, version, author, before, after, added, removed, ts) VALUES(?,?,?,?,?,'','',?)`,
			"b1", 100+i, "legacy", before, after, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`PRAGMA user_version=0`); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// Reopen: migrateJournal converts + prunes.
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	var nBloat int
	_ = s2.db.QueryRow(`SELECT COUNT(1) FROM ops WHERE before<>'' OR after<>''`).Scan(&nBloat)
	if nBloat != 0 {
		t.Fatalf("migration left %d full-snapshot rows", nBloat)
	}
	var nRows int
	_ = s2.db.QueryRow(`SELECT COUNT(1) FROM ops WHERE board_id='b1'`).Scan(&nRows)
	if nRows > journalKeep {
		t.Fatalf("journal not pruned: %d rows > %d", nRows, journalKeep)
	}
	// The newest legacy row (i=599) must survive with a correct diff.
	hist, err := s2.History("b1", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) == 0 {
		t.Fatal("no history after migration")
	}
	top := hist[0]
	wantAdded, wantRemoved := board.DiffLines(
		"- [ ] item 599 ^aaaaaa\n",
		"- [x] item 599 ^aaaaaa\n- [ ] extra 599 ^bbbbbb\n")
	if !reflect.DeepEqual(top.Added, wantAdded) || !reflect.DeepEqual(top.Removed, wantRemoved) {
		t.Fatalf("migrated diff mismatch: +%v -%v want +%v -%v", top.Added, top.Removed, wantAdded, wantRemoved)
	}

	// Idempotent: a second reopen is a no-op (user_version already set).
	var uv int
	_ = s2.db.QueryRow(`PRAGMA user_version`).Scan(&uv)
	if uv != journalSchemaVersion {
		t.Fatalf("user_version=%d want %d", uv, journalSchemaVersion)
	}
}
