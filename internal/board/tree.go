package board

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// Node is the exported, lossy read model of a board subtree addressed by node
// id: only the structured fields a caller reasons about, no verbatim raw lines
// or continuation blocks. Item stays the internal lossless representation used
// for parse/render.
type Node struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	Status   Status `json:"status"`
	Urgent   bool   `json:"urgent,omitempty"`
	Pinned   bool   `json:"pinned,omitempty"`
	Children []Node `json:"children,omitempty"`
}

// OpResult reports the outcome of a Tree.Apply. Note carries a non-fatal
// advisory (e.g. "3 items match; using the first"); ID is the addressed node's
// id when the op was id-addressed.
type OpResult struct {
	Note string
	ID   string
}

// Event is a change notification scoped to a watched subtree. The file backend
// degrades to a coarse "changed" kind derived from a whole-file diff; NodeID is
// the watched root the change fell within.
type Event struct {
	NodeID string
	Kind   string // "changed"
}

// Tree is the storage-agnostic node interface. The file backend implements it
// now; a server backend later implements the same contract so addressing
// (`<board>#<id>`) is uniform across CLI, web, TUI, watch, and remote URLs.
type Tree interface {
	// Get reads the subtree rooted at id (empty id = whole board root). depth<0
	// returns the whole subtree; depth==0 the node alone; depth==n n levels of
	// descendants.
	Get(id string, depth int) (*Node, error)
	// Apply executes an op addressed by node id (with Section+Raw / Query
	// fallbacks), assigning ids to any node that lacks one on the way out.
	Apply(op Op) (OpResult, error)
	// Watch delivers change events scoped to the subtree rooted at id; the
	// returned func cancels the watch. Events outside the subtree are filtered.
	Watch(id string) (<-chan Event, func(), error)
}

// FileTree is the file-backed Tree: every method funnels through the same
// board lock, atomic-rename, and undo journal the CLI and web edit paths use.
type FileTree struct {
	path     string
	snapshot string        // monitor self-edit snapshot to refresh on write ("" = none)
	author   string        // undo-journal author for writes
	interval time.Duration // Watch poll interval
}

// OpenFileTree opens the board at path as a Tree.
func OpenFileTree(path string) *FileTree {
	return &FileTree{path: path, author: "tree", interval: 500 * time.Millisecond}
}

// Get implements Tree.
func (t *FileTree) Get(id string, depth int) (*Node, error) {
	b, err := Load(t.path)
	if err != nil {
		return nil, err
	}
	return b.Node(id, depth)
}

// Node builds the exported read model for the subtree rooted at id (empty id =
// whole board root) from an in-memory board, limited to depth levels of
// descendants (depth<0 = unlimited). It is the storage-agnostic core the file
// backend's Get funnels through, reused by the SQLite backend so both produce
// identical node JSON. Missing id yields ErrOpNotFound.
func (b *Board) Node(id string, depth int) (*Node, error) {
	if id == "" {
		root := &Node{}
		for _, s := range b.Sections {
			root.Children = append(root.Children, sectionNode(s, depth-1))
		}
		return root, nil
	}
	if s := b.SectionByID(id); s != nil {
		n := sectionNode(s, depth)
		return &n, nil
	}
	if it := b.FindByID(id); it != nil {
		n := itemNode(it, depth)
		return &n, nil
	}
	return nil, fmt.Errorf("%w: no node with id %q", ErrOpNotFound, id)
}

// itemNode converts an item subtree to a Node, limited to depth levels of
// descendants (depth<0 = unlimited, depth==0 = no children). Continuation lines
// carry no id and are skipped.
func itemNode(it *Item, depth int) Node {
	n := Node{ID: it.ID, Text: it.Text, Status: it.Status, Urgent: it.Urgent, Pinned: it.Pinned}
	if depth == 0 {
		return n
	}
	for _, c := range it.Children {
		if !c.parsed {
			continue
		}
		n.Children = append(n.Children, itemNode(c, depth-1))
	}
	return n
}

// sectionNode converts a section to a Node (Text is the heading title).
func sectionNode(s *Section, depth int) Node {
	n := Node{ID: s.ID, Text: s.Title}
	if depth == 0 {
		return n
	}
	for _, it := range s.Items {
		if !it.parsed {
			continue
		}
		n.Children = append(n.Children, itemNode(it, depth-1))
	}
	return n
}

// Apply implements Tree: a locked, journaled read-modify-write that dispatches
// through the shared op layer and assigns ids before rendering.
func (t *FileTree) Apply(op Op) (OpResult, error) {
	var res OpResult
	err := EditUnderLockJournaled(t.path, t.snapshot, t.author, func(content string) (string, error) {
		b := Parse(content)
		b.Path = t.path
		note, aerr := Apply(b, op)
		if aerr != nil {
			return "", aerr
		}
		b.EnsureIDs()
		res.Note = note
		res.ID = op.ID
		return b.Render(), nil
	})
	if err != nil {
		return OpResult{}, err
	}
	return res, nil
}

// Watch implements Tree with a thin poller over the existing whole-file diff:
// on each interval it re-reads the board and, if the subtree rooted at id
// changed, emits a coarse "changed" event. Changes outside the subtree are
// filtered out. Minimal by design for M1; the cancel func is idempotent.
func (t *FileTree) Watch(id string) (<-chan Event, func(), error) {
	ch := make(chan Event, 8)
	stop := make(chan struct{})
	var once sync.Once
	cancel := func() { once.Do(func() { close(stop) }) }

	data, _ := os.ReadFile(t.path)
	prev := string(data)

	go func() {
		defer close(ch)
		ticker := time.NewTicker(t.interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				b, err := os.ReadFile(t.path)
				if err != nil {
					continue
				}
				cur := string(b)
				if cur == prev {
					continue
				}
				if subtreeChanged(prev, cur, id) {
					select {
					case ch <- Event{NodeID: id, Kind: "changed"}:
					case <-stop:
						return
					}
				}
				prev = cur
			}
		}
	}()
	return ch, cancel, nil
}

// subtreeChanged reports whether the subtree rooted at id differs between two
// board contents. Root (empty id) tracks any change; a node id compares just
// that node's rendered snapshot, so edits elsewhere on the board are ignored.
func subtreeChanged(before, after, id string) bool {
	if id == "" {
		return before != after
	}
	return nodeSnapshot(before, id) != nodeSnapshot(after, id)
}

// nodeSnapshot renders the subtree rooted at id from content to a stable string
// for change comparison. A missing node yields a sentinel so appearing or
// disappearing counts as a change.
func nodeSnapshot(content, id string) string {
	b := Parse(content)
	if s := b.SectionByID(id); s != nil {
		return renderNode(sectionNode(s, -1))
	}
	if it := b.FindByID(id); it != nil {
		return renderNode(itemNode(it, -1))
	}
	return "\x00gone"
}

func renderNode(n Node) string {
	var sb strings.Builder
	var walk func(n Node, depth int)
	walk = func(n Node, depth int) {
		fmt.Fprintf(&sb, "%s%d|%s|%v|%v|%s\n", strings.Repeat("  ", depth), n.Status, n.ID, n.Urgent, n.Pinned, n.Text)
		for _, c := range n.Children {
			walk(c, depth+1)
		}
	}
	walk(n, 0)
	return sb.String()
}
