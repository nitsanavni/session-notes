package tui

import (
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/nitsanavni/session-notes/internal/board"
	"github.com/nitsanavni/session-notes/internal/cloud"
)

// retryDelay is how long the SSE watch waits before reconnecting after the
// stream drops (connection loss / server restart).
const retryDelay = 2 * time.Second

// remoteStore is the BoardStore for a cloud board: it wraps a cloud.RemoteTree
// (the same HTTP+SSE client the `edit`/`watch` CLIs use) so the TUI can open,
// edit, and live-reload a `https://host/b/<board>[#<node>]` board with the same
// experience as a local file, minus the explicitly-degraded features (undo/redo,
// $EDITOR merge, and the shared-journal history overlay — see Capabilities).
//
// Conflict resolution differs from the file backend: instead of the flock-based
// 3-way rebase (SaveRebasing), every edit is a surgical op sent through
// RemoteTree.Apply with an optimistic base version — on a 409 the client
// refetches head and retries, so a racing writer is never silently clobbered and
// the op lands exactly once. Watch is the server's SSE stream (whole board or a
// #node subtree), which also drives self-reconnect on connection loss.
type remoteStore struct {
	tree *cloud.RemoteTree
	ref  cloud.Ref
	node string // #node fragment: edit carve-out root + map zoom root ("" = whole board)

	ch   chan struct{} // external-change signals delivered to the model as reloadMsg
	stop chan struct{}
	once sync.Once

	mu        sync.Mutex
	connected bool // whether the SSE stream is currently live (drives the footer warning)
}

// newRemoteStore builds a store for a parsed remote ref. token may be "" for an
// --insecure server; an authenticated server rejects the first Load and RunRemote
// turns that into the login hint.
func newRemoteStore(ref cloud.Ref, token string) *remoteStore {
	return &remoteStore{
		tree: cloud.NewRemoteTree(ref.Server, ref.Board, token),
		ref:  ref,
		node: ref.Node,
		ch:   make(chan struct{}, 1),
		stop: make(chan struct{}),
	}
}

// Capabilities reports which features are supported on this backend. Remote
// boards degrade undo/redo, the $EDITOR merge, and the shared-journal history
// overlay (all file/flock-specific) to a clear status-line message in v1.
type Capabilities struct {
	Undo        bool
	EditorMerge bool
	History     bool
}

func (rs *remoteStore) caps() Capabilities { return Capabilities{} }

// Load fetches the whole board markdown (the subtree the token is scoped to,
// when the grant carves one out — the server renders it for us).
func (rs *remoteStore) load() (string, error) {
	content, _, err := rs.tree.Raw()
	return content, err
}

// applyOp sends the TUI's pending mutation to the server as one surgical op and
// returns the authoritative board content after it lands. The op's Root is the
// #node carve-out, so an edit addressing outside it is refused server-side and
// surfaces in the status line rather than crashing. Errors (network outage, a
// permission refusal) propagate so the caller can keep the user's optimistic
// in-memory edit visible instead of dropping it.
func (rs *remoteStore) applyOp(op pendingOp) (string, error) {
	for _, bop := range pendingToOps(op, rs.node) {
		if _, err := rs.tree.Apply(bop); err != nil {
			return "", err
		}
	}
	return rs.load()
}

// pendingToOps converts a TUI pendingOp into the board.Op(s) the edit endpoint
// applies, mirroring applyOp's file-tree replay exactly (same addressing, same
// "user: " reply prefix, same AddReply-vs-AddItem choice). Structural ops carry
// their absolute resulting state. root scopes every op to the #node carve-out.
func pendingToOps(op pendingOp, root string) []board.Op {
	base := board.Op{Root: root, Section: op.section, Raw: op.rawLine}
	switch op.typ {
	case opAdd:
		return []board.Op{{Name: "add", Root: root, Section: op.section, Text: op.payload}}
	case opEdit:
		base.Name, base.Text = "edit", op.payload
	case opReply:
		// The TUI authors the human's reply "user:" and appends it under the
		// thread parent (AddReply == the "fork" op).
		base.Name, base.Text = "fork", "user: "+op.payload
	case opAddChild:
		base.Name, base.Text = "fork", op.payload
	case opLog:
		return []board.Op{{Name: "log", Root: root, Text: op.payload}}
	case opCycle:
		base.Name, base.Status = "status", op.newStatus
	case opUrgent:
		base.Name, base.Urgent = "urgent", op.newUrgent
	case opPin:
		base.Name, base.Pinned = "pin", op.newPinned
	case opArchiveItem:
		base.Name = "archive"
	case opDeleteItem:
		base.Name = "delete"
	case opArchiveSection:
		return []board.Op{{Name: "archive-section", Root: root, Section: op.section}}
	case opDeleteSection:
		return []board.Op{{Name: "delete-section", Root: root, Section: op.section}}
	case opRenameSection:
		return []board.Op{{Name: "rename-section", Root: root, Section: op.section, Text: op.payload}}
	case opSetTitle:
		return []board.Op{{Name: "title", Root: root, Text: op.payload}}
	case opAddSection:
		var ops []board.Op
		for _, title := range op.sections {
			ops = append(ops, board.Op{Name: "add-section", Root: root, Text: title})
		}
		return ops
	default:
		return nil
	}
	return []board.Op{base}
}

// startWatch opens the SSE change stream and keeps it alive across connection
// loss: on any stream end it marks the store disconnected (the footer warns) and
// retries with backoff; a successful (re)connect signals a reload so a change
// missed during the outage is caught up. Runs until close().
func (rs *remoteStore) startWatch() {
	go rs.watchLoop()
}

func (rs *remoteStore) watchLoop() {
	for {
		select {
		case <-rs.stop:
			return
		default:
		}
		events, cancel, err := rs.tree.WatchFiltered(rs.node, "")
		if err != nil {
			rs.setConnected(false)
			if !rs.sleep(retryDelay) {
				return
			}
			continue
		}
		rs.setConnected(true)
		rs.signal() // catch up on anything that changed while (re)connecting
		rs.drain(events)
		cancel()
		rs.setConnected(false)
		if !rs.sleep(retryDelay) {
			return
		}
	}
}

// drain forwards each SSE change to the model until the stream closes or the
// store is stopped.
func (rs *remoteStore) drain(events <-chan board.Event) {
	for {
		select {
		case <-rs.stop:
			return
		case _, ok := <-events:
			if !ok {
				return // stream ended (server closed / connection dropped)
			}
			rs.signal()
		}
	}
}

func (rs *remoteStore) signal() {
	select {
	case rs.ch <- struct{}{}:
	default: // coalesce bursts
	}
}

// sleep waits d, returning false if the store was stopped meanwhile.
func (rs *remoteStore) sleep(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-rs.stop:
		return false
	case <-t.C:
		return true
	}
}

func (rs *remoteStore) setConnected(v bool) {
	rs.mu.Lock()
	rs.connected = v
	rs.mu.Unlock()
}

func (rs *remoteStore) isConnected() bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.connected
}

// wait returns a Cmd that resolves to a reloadMsg on the next external change —
// the SSE counterpart of the file watcher's wait().
func (rs *remoteStore) wait() tea.Cmd {
	return func() tea.Msg {
		select {
		case <-rs.ch:
			return reloadMsg{}
		case <-rs.stop:
			return nil
		}
	}
}

func (rs *remoteStore) close() {
	rs.once.Do(func() { close(rs.stop) })
}

// url is the human-facing board reference (for the header and `y` yank).
func (rs *remoteStore) url() string {
	u := rs.ref.Server + "/b/" + rs.ref.Board
	if rs.node != "" {
		u += "#" + rs.node
	}
	return u
}

// isAuthError reports whether err from an initial Load is an authentication
// failure (missing/invalid token), so RunRemote can print the login hint.
func isAuthError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "401") || strings.Contains(s, "Unauthorized")
}
