package tui

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nitsanavni/session-notes/internal/board"
	"github.com/nitsanavni/session-notes/internal/cloud"
)

// remoteFixture stands up an in-process cloud server (insecure, so no token is
// needed) seeded with one board, and returns a model already attached to it plus
// a second RemoteTree standing in for an external writer (Claude / the web UI).
func remoteFixture(t *testing.T, seed string) (*model, *cloud.RemoteTree, string) {
	t.Helper()
	store, err := cloud.Open(filepath.Join(t.TempDir(), "boards.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.CreateBoard("b1", seed); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(cloud.NewServer(store, true).Handler())
	t.Cleanup(ts.Close)

	ref, err := cloud.ParseRef(ts.URL + "/b/b1")
	if err != nil {
		t.Fatal(err)
	}
	m := newModel()
	m.width, m.height = 80, 24
	if err := m.openRemote(newRemoteStore(ref, "")); err != nil {
		t.Fatal(err)
	}
	// Close the SSE watch before the httptest server (LIFO cleanup order), so
	// Server.Close does not block on the open event stream until the client's
	// 30s timeout.
	t.Cleanup(m.remote.close)
	external := cloud.NewRemoteTree(ref.Server, "b1", "")
	return m, external, ts.URL
}

const remoteSeed = "## Threads\n- [ ] alpha\n- [ ] beta\n\n## Log\n"

// TestRemoteEditRoundTrip: an edit made through the model lands on the server and
// the model adopts the authoritative content.
func TestRemoteEditRoundTrip(t *testing.T) {
	m, external, _ := remoteFixture(t, remoteSeed)

	target := m.board.Section("Threads").Items[0]
	op := pendingOp{typ: opEdit, section: "Threads", rawLine: target.Raw(), payload: "alpha edited"}
	target.Text = "alpha edited" // optimistic, as handleInputKey does
	m.saveWithRebase(op)

	if !hasItem(m.board, "Threads", "alpha edited") {
		t.Errorf("model board did not adopt edit: %q", m.board.Render())
	}
	// Server-side confirmation via an independent client.
	n, err := external.Get("", -1)
	if err != nil {
		t.Fatal(err)
	}
	if !nodeHasText(n, "alpha edited") {
		t.Error("edit not persisted server-side")
	}
}

// TestRemoteAddRoundTrip covers the add op (different addressing path).
func TestRemoteAddRoundTrip(t *testing.T) {
	m, external, _ := remoteFixture(t, remoteSeed)
	m.selSec = 0
	m.board.AddItem("Threads", "gamma")
	m.saveWithRebase(pendingOp{typ: opAdd, section: "Threads", payload: "gamma"})

	n, _ := external.Get("", -1)
	if !nodeHasText(n, "gamma") {
		t.Errorf("add not persisted: %q", m.board.Render())
	}
}

// TestRemoteExternalChangeReload: a change by another writer becomes visible when
// the model reloads (the SSE watch drives this reload in the running program).
func TestRemoteExternalChangeReload(t *testing.T) {
	m, external, _ := remoteFixture(t, remoteSeed)
	if _, err := external.Apply(board.Op{Name: "add", Section: "Threads", Text: "from-claude"}); err != nil {
		t.Fatal(err)
	}
	m.reloadExternal()
	if !hasItem(m.board, "Threads", "from-claude") {
		t.Errorf("external change not reflected after reload: %q", m.board.Render())
	}
	// The external item is flagged fresh (novelty feed / accent), like the file path.
	if len(m.fresh) == 0 {
		t.Error("external change not marked fresh")
	}
}

// TestRemoteSaveConflictNoLoss: an external write lands between the model's load
// and its save; both edits must survive (the server merges the surgical op).
func TestRemoteSaveConflictNoLoss(t *testing.T) {
	m, external, _ := remoteFixture(t, remoteSeed)

	// User begins editing alpha (optimistic, base version now stale).
	target := m.board.Section("Threads").Items[0]
	op := pendingOp{typ: opEdit, section: "Threads", rawLine: target.Raw(), payload: "alpha edited"}
	target.Text = "alpha edited"

	// External writer advances the board underneath.
	if _, err := external.Apply(board.Op{Name: "add", Section: "Threads", Text: "concurrent"}); err != nil {
		t.Fatal(err)
	}

	m.saveWithRebase(op)

	n, _ := external.Get("", -1)
	if !nodeHasText(n, "alpha edited") {
		t.Error("user edit lost in conflict")
	}
	if !nodeHasText(n, "concurrent") {
		t.Error("external edit clobbered in conflict")
	}
}

// TestRemoteWatchSignals: the SSE watch fires a reloadMsg-bearing signal when an
// external writer changes the board.
func TestRemoteWatchSignals(t *testing.T) {
	m, external, _ := remoteFixture(t, remoteSeed) // openRemote already started the watch

	// Let the stream connect.
	deadline := time.Now().Add(2 * time.Second)
	for !m.remote.isConnected() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !m.remote.isConnected() {
		t.Fatal("SSE stream never connected")
	}
	// Drain the connect-time catch-up signal.
	select {
	case <-m.remote.ch:
	default:
	}

	if _, err := external.Apply(board.Op{Name: "add", Section: "Threads", Text: "ping"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-m.remote.ch:
	case <-time.After(3 * time.Second):
		t.Fatal("no watch signal for external change")
	}
}

// TestRemotePermissionRefusalSurfaced: an op addressing outside the #node
// carve-out is refused server-side; remoteSave surfaces it in the status line and
// does NOT crash or claim success.
func TestRemotePermissionRefusalSurfaced(t *testing.T) {
	m, _, _ := remoteFixture(t, remoteSeed)
	// Pretend we opened zoomed to a node id that does not contain the target.
	m.remote.node = "nonexistent-node-id"
	op := pendingOp{typ: opEdit, section: "Threads", rawLine: "- [ ] alpha", payload: "x"}
	m.remoteSave(op)
	if m.status == "" || !strings.Contains(strings.ToLower(m.status), "refused") && !strings.Contains(strings.ToLower(m.status), "unsaved") {
		t.Errorf("refusal not surfaced in status: %q", m.status)
	}
}

// TestRemoteUndoRedoDegraded: undo/redo report the documented degradation instead
// of mutating.
func TestRemoteUndoRedoDegraded(t *testing.T) {
	m, _, _ := remoteFixture(t, remoteSeed)
	m.undo()
	if !strings.Contains(m.status, "undo not available") {
		t.Errorf("undo status = %q", m.status)
	}
	m.redo()
	if !strings.Contains(m.status, "redo not available") {
		t.Errorf("redo status = %q", m.status)
	}
}

// TestRemoteAuthErrorHint: a secured server with no stored token yields an auth
// error at open time (RunRemote turns this into the login hint).
func TestRemoteAuthErrorHint(t *testing.T) {
	store, err := cloud.Open(filepath.Join(t.TempDir(), "boards.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_ = store.CreateBoard("b1", remoteSeed)
	ts := httptest.NewServer(cloud.NewServer(store, false).Handler()) // secured
	defer ts.Close()

	ref, _ := cloud.ParseRef(ts.URL + "/b/b1")
	m := newModel()
	err = m.openRemote(newRemoteStore(ref, "")) // no token
	if err == nil {
		t.Fatal("expected auth error opening secured board without token")
	}
	if !isAuthError(err) {
		t.Errorf("isAuthError false for %v", err)
	}
}

// TestPendingToOps maps each pending op to the wire op(s), matching applyOp.
func TestPendingToOps(t *testing.T) {
	cases := []struct {
		op   pendingOp
		want string // op name of the (first) produced board.Op
		n    int
	}{
		{pendingOp{typ: opAdd, section: "S", payload: "x"}, "add", 1},
		{pendingOp{typ: opEdit, section: "S", rawLine: "r", payload: "x"}, "edit", 1},
		{pendingOp{typ: opReply, section: "S", rawLine: "r", payload: "x"}, "fork", 1},
		{pendingOp{typ: opCycle, section: "S", rawLine: "r"}, "status", 1},
		{pendingOp{typ: opUrgent, section: "S", rawLine: "r"}, "urgent", 1},
		{pendingOp{typ: opPin, section: "S", rawLine: "r"}, "pin", 1},
		{pendingOp{typ: opArchiveItem, section: "S", rawLine: "r"}, "archive", 1},
		{pendingOp{typ: opDeleteItem, section: "S", rawLine: "r"}, "delete", 1},
		{pendingOp{typ: opArchiveSection, section: "S"}, "archive-section", 1},
		{pendingOp{typ: opDeleteSection, section: "S"}, "delete-section", 1},
		{pendingOp{typ: opRenameSection, section: "S", payload: "n"}, "rename-section", 1},
		{pendingOp{typ: opSetTitle, payload: "t"}, "title", 1},
		{pendingOp{typ: opLog, payload: "l"}, "log", 1},
		{pendingOp{typ: opAddSection, sections: []string{"A", "B"}}, "add-section", 2},
	}
	for _, c := range cases {
		ops := pendingToOps(c.op, "root1")
		if len(ops) != c.n {
			t.Errorf("%v: got %d ops want %d", c.op.typ, len(ops), c.n)
			continue
		}
		if ops[0].Name != c.want {
			t.Errorf("%v: name %q want %q", c.op.typ, ops[0].Name, c.want)
		}
		if ops[0].Root != "root1" {
			t.Errorf("%v: root not propagated", c.op.typ)
		}
	}
	// The reply op prefixes the human turn with "user:".
	if ops := pendingToOps(pendingOp{typ: opReply, section: "S", rawLine: "r", payload: "hi"}, ""); ops[0].Text != "user: hi" {
		t.Errorf("reply text = %q", ops[0].Text)
	}
}

// nodeHasText reports whether text appears anywhere in the node tree.
func nodeHasText(n *board.Node, text string) bool {
	if n == nil {
		return false
	}
	if strings.Contains(n.Text, text) {
		return true
	}
	for i := range n.Children {
		if nodeHasText(&n.Children[i], text) {
			return true
		}
	}
	return false
}
