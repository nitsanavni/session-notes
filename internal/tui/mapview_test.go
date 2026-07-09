package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nitsanavni/session-notes/internal/board"
)

// newMapModel builds a map-view model over src at the given size, with a
// throwaway board path (temp dir) so the locked-save path does not touch the
// working tree, and the map already built and focused on the center.
func newMapModel(t *testing.T, src string, w, h int) *model {
	t.Helper()
	m := newModel()
	m.board = board.Parse(src)
	m.path = filepath.Join(t.TempDir(), "map-test.md")
	m.board.Path = m.path
	m.mode = modeBoard
	m.width, m.height = w, h
	m.rebuildPositions()
	m.mapView = true
	m.mp = nil
	m.ensureMap()
	return m
}

// focusMapItem points map focus at the first item whose board text equals want.
func focusMapItem(t *testing.T, m *model, want string) {
	t.Helper()
	for n, ref := range m.mp.refs {
		if ref.kind == refItem && ref.item.Text == want {
			m.mp.focus = n
			m.mapFocusKey = m.mp.keys[n]
			return
		}
	}
	t.Fatalf("no map item with text %q", want)
}

// focusMapSection points map focus at the section node with the given title.
func focusMapSection(t *testing.T, m *model, title string) {
	t.Helper()
	for n, ref := range m.mp.refs {
		if ref.kind == refSection && ref.section == title {
			m.mp.focus = n
			m.mapFocusKey = m.mp.keys[n]
			return
		}
	}
	t.Fatalf("no map section %q", title)
}

// TestBridgeSkeletonAlwaysPresent asserts every section becomes a first-ring
// node even when empty, and the title heading is the center.
func TestBridgeSkeletonAlwaysPresent(t *testing.T) {
	src := "---\ntitle: My Board\n---\n## Plan\n\n## Threads\n- [ ] a task\n\n## Questions\n\n## Ideas\n\n## Log\n"
	b := board.Parse(src)
	root, refs, _ := buildMapTree(b, nil)

	if refs[root].kind != refCenter || root.Text != "My Board" {
		t.Fatalf("root is not the titled center: %+v text=%q", refs[root], root.Text)
	}
	if len(root.Children) != len(b.Sections) {
		t.Fatalf("first ring has %d nodes, want %d sections", len(root.Children), len(b.Sections))
	}
	want := []string{"Plan", "Threads", "Questions", "Ideas", "Log"}
	for i, sn := range root.Children {
		if refs[sn].kind != refSection || sn.Text != want[i] {
			t.Errorf("section node %d = %q (%+v), want %q", i, sn.Text, refs[sn], want[i])
		}
	}
	// The empty Plan section is present with no children; Threads has its item.
	if len(root.Children[0].Children) != 0 {
		t.Errorf("empty Plan should have no child nodes, got %d", len(root.Children[0].Children))
	}
	if len(root.Children[1].Children) != 1 {
		t.Errorf("Threads should have exactly its one item, got %d", len(root.Children[1].Children))
	}
}

// TestBridgeTitleFallback checks the center text falls back session -> basename
// -> "board" when there is no frontmatter title.
func TestBridgeTitleFallback(t *testing.T) {
	sess := board.Parse("---\nsession: abc123\n---\n## Plan\n")
	if got := boardTitle(sess); got != "abc123" {
		t.Errorf("session fallback = %q, want abc123", got)
	}
	noFM := board.Parse("## Plan\n")
	noFM.Path = "/tmp/foo/my-notes.md"
	if got := boardTitle(noFM); got != "my-notes" {
		t.Errorf("basename fallback = %q, want my-notes", got)
	}
	bare := board.Parse("## Plan\n")
	if got := boardTitle(bare); got != "board" {
		t.Errorf("bare fallback = %q, want board", got)
	}
}

// TestMapNodeTextMarkers checks the on-map text carries the list view's literal
// status markers and the urgent prefix.
func TestMapNodeTextMarkers(t *testing.T) {
	cases := []struct{ src, want string }{
		{"## Threads\n- [ ] open one\n", "[ ] open one"},
		{"## Threads\n- [>] doing\n", "[>] doing"},
		{"## Threads\n- [x] done\n", "[x] done"},
		{"## Threads\n- [?] blocked\n", "[?] blocked"},
		{"## Threads\n- [ ] !! urgent\n", "[ ] !! urgent"},
		{"## Ideas\n- plain idea\n", "plain idea"},
	}
	for _, c := range cases {
		b := board.Parse(c.src)
		it := b.Sections[0].Items[len(b.Sections[0].Items)-1]
		if got := mapNodeText(it); got != c.want {
			t.Errorf("mapNodeText(%q) = %q, want %q", c.src, got, c.want)
		}
	}
}

// TestMapSpatialNav asserts hjkl move focus spatially: from the center, l moves
// onto a right-side node and h comes back, and the moves land on real nodes.
func TestMapSpatialNav(t *testing.T) {
	src := "---\ntitle: T\n---\n## Plan\n- [ ] alpha\n\n## Threads\n- [ ] beta\n\n## Questions\n- [ ] gamma\n"
	m := newMapModel(t, src, 100, 30)
	// Start on the center.
	if m.mp.refs[m.mp.focus].kind != refCenter {
		t.Fatalf("expected initial focus on center")
	}
	// Move right: should land on some node to the right of center.
	m.mapMove('l')
	if m.mp.refs[m.mp.focus].kind == refCenter {
		t.Fatalf("l from center did not move focus")
	}
	fp := m.mp.placement(m.mp.focus)
	cp := m.mp.placement(m.mp.root)
	if fp.Col+fp.Width/2 <= cp.Col+cp.Width/2 {
		t.Errorf("l moved to a node not to the right of center")
	}
	// Moving back left should eventually return toward the center column.
	before := m.mp.focus
	m.mapMove('h')
	if m.mp.focus == before {
		t.Errorf("h did not move focus back")
	}
}

// TestMapNavKeysWork drives focus changes through the real key handler.
func TestMapNavKeysWork(t *testing.T) {
	src := "---\ntitle: T\n---\n## Plan\n- [ ] alpha\n- [ ] delta\n\n## Threads\n- [ ] beta\n"
	m := newMapModel(t, src, 100, 30)
	start := m.mapFocusKey
	m.handleMapKey(keyPress("l"))
	if m.mapFocusKey == start {
		t.Errorf("l key did not change focus key")
	}
}

// TestMapSectionCannotBeMarked asserts space is a no-op on a section node
// (protected) while it cycles an item's status.
func TestMapSectionCannotBeMarked(t *testing.T) {
	src := "## Threads\n- [ ] task\n"
	m := newMapModel(t, src, 80, 24)

	focusMapSection(t, m, "Threads")
	before := m.board.Render()
	m.handleMapKey(keyPress(" "))
	if m.board.Render() != before {
		t.Errorf("space on a section mutated the board:\n%s", m.board.Render())
	}
	if m.status == "" {
		t.Errorf("expected a status message rejecting the section mark")
	}

	// On the item it cycles [ ] -> [>].
	focusMapItem(t, m, "task")
	m.handleMapKey(keyPress(" "))
	if got := m.board.Sections[0].Items[0].Status; got != board.StatusInProgress {
		t.Errorf("space on item: status = %v, want InProgress", got)
	}
}

// TestMapCycleFullRing walks the map's status cycle through every state,
// including blocked and back to none.
func TestMapCycleFullRing(t *testing.T) {
	src := "## Threads\n- task\n" // starts StatusNone (plain bullet)
	m := newMapModel(t, src, 80, 24)
	focusMapItem(t, m, "task")
	it := m.board.Sections[0].Items[0]
	want := []board.Status{
		board.StatusOpen, board.StatusInProgress, board.StatusDone,
		board.StatusBlocked, board.StatusNone,
	}
	for i, w := range want {
		m.handleMapKey(keyPress(" "))
		if it.Status != w {
			t.Fatalf("cycle step %d: status = %v, want %v", i, it.Status, w)
		}
	}
}

// TestMapSectionCannotBeDeleted asserts D is refused on a section node but
// deletes an item's subtree.
func TestMapSectionCannotBeDeleted(t *testing.T) {
	src := "## Threads\n- [ ] keep\n- [ ] gone\n"
	m := newMapModel(t, src, 80, 24)

	focusMapSection(t, m, "Threads")
	before := m.board.Render()
	m.handleMapKey(keyPress("D"))
	if m.board.Render() != before {
		t.Errorf("D on a section mutated the board:\n%s", m.board.Render())
	}

	focusMapItem(t, m, "gone")
	m.handleMapKey(keyPress("D"))
	got := m.board.Render()
	if strings.Contains(got, "gone") {
		t.Errorf("item not deleted:\n%s", got)
	}
	if !strings.Contains(got, "keep") {
		t.Errorf("sibling item lost:\n%s", got)
	}
}

// TestMapEditPersists exercises the inline edit through the real key handlers
// and asserts the change lands on disk via the locked save path.
func TestMapEditPersists(t *testing.T) {
	src := "## Threads\n- [ ] old text\n"
	m := newMapModel(t, src, 80, 24)
	focusMapItem(t, m, "old text")

	m.handleMapKey(keyPress("e"))
	if m.mode != modeMapEdit {
		t.Fatalf("e did not enter map edit mode, got %v", m.mode)
	}
	m.input.SetValue("new text")
	m.handleMapInputKey(keyPress("enter"))

	// In-memory board updated.
	if got := m.board.Sections[0].Items[0].Text; got != "new text" {
		t.Errorf("in-memory text = %q, want new text", got)
	}
	// Persisted to disk through the locked save.
	data, err := os.ReadFile(m.path)
	if err != nil {
		t.Fatalf("read back board: %v", err)
	}
	if !strings.Contains(string(data), "- [ ] new text") {
		t.Errorf("edit not persisted to disk:\n%s", data)
	}
}

// TestMapAddChildPersists adds a child under an item and asserts it nests and
// persists.
func TestMapAddChildPersists(t *testing.T) {
	src := "## Threads\n- [ ] parent\n"
	m := newMapModel(t, src, 80, 24)
	focusMapItem(t, m, "parent")

	m.handleMapKey(keyPress("a"))
	if m.mode != modeMapAdd {
		t.Fatalf("a did not enter map add mode, got %v", m.mode)
	}
	m.input.SetValue("child line")
	m.handleMapInputKey(keyPress("enter"))

	data, err := os.ReadFile(m.path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(data), "  - child line") {
		t.Errorf("child not added as a nested bullet:\n%s", data)
	}
}

// TestMapAddToSectionPersists adds a top-level item when a section node is
// focused.
func TestMapAddToSectionPersists(t *testing.T) {
	src := "## Threads\n- [ ] existing\n"
	m := newMapModel(t, src, 80, 24)
	focusMapSection(t, m, "Threads")

	m.handleMapKey(keyPress("a"))
	m.input.SetValue("fresh item")
	m.handleMapInputKey(keyPress("enter"))

	data, err := os.ReadFile(m.path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(data), "- [ ] fresh item") {
		t.Errorf("section add not persisted:\n%s", data)
	}
}

// TestMapFoldReduces asserts folding a section removes its item nodes from the
// laid-out placements, and unfolding restores them; fold state survives rebuild.
func TestMapFoldReduces(t *testing.T) {
	src := "## Threads\n- [ ] one\n- [ ] two\n- [ ] three\n"
	m := newMapModel(t, src, 100, 30)
	full := len(m.mp.layout.Placements)

	focusMapSection(t, m, "Threads")
	m.handleMapKey(keyPress("enter")) // fold
	m.ensureMap()
	if !m.mapFolded[m.mapFocusKey] {
		t.Fatalf("section not marked folded")
	}
	folded := len(m.mp.layout.Placements)
	if folded >= full {
		t.Errorf("folding did not reduce placements: full=%d folded=%d", full, folded)
	}

	m.handleMapKey(keyPress("enter")) // unfold
	m.ensureMap()
	if len(m.mp.layout.Placements) != full {
		t.Errorf("unfold did not restore placements: got %d want %d", len(m.mp.layout.Placements), full)
	}
}

// TestMapViewRendersFocusAndSections is a golden-ish view test: the map body
// shows the center title, every section, and the focused item, without the
// horizontal body ever exceeding the viewport width.
func TestMapViewRendersFocusAndSections(t *testing.T) {
	src := "---\ntitle: Demo\n---\n## Plan\n- [ ] draft\n\n## Threads\n- [>] wip\n\n## Ideas\n"
	m := newMapModel(t, src, 100, 30)
	focusMapItem(t, m, "wip")

	out := stripANSI(m.viewMap())
	for _, want := range []string{"Demo", "Plan", "Threads", "Ideas", "wip", "draft"} {
		if !strings.Contains(out, want) {
			t.Errorf("map view missing %q:\n%s", want, out)
		}
	}
	// The focused node's full text shows in the detail footer line.
	if !strings.Contains(out, "[>] wip") {
		t.Errorf("focused detail line missing:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		// The help/detail footer is a status line clipped by the terminal (like
		// the list view's), not a wrapped body row — skip it.
		if strings.Contains(line, "hjkl move") {
			break
		}
		if w := len([]rune(line)); w > 100 {
			t.Errorf("map row exceeds width: %q (%d)", line, w)
		}
	}
}

// TestMapToggleFromListView asserts `m` in the list view enters the map (seeding
// focus from the list cursor) and `m` in the map returns to the list.
func TestMapToggleFromListView(t *testing.T) {
	src := "## Threads\n- [ ] alpha\n- [ ] beta\n"
	m := newTestModel(src, 80, 24)
	focus(t, m, "beta")

	m.handleBoardKey(keyPress("m"))
	if !m.mapView {
		t.Fatalf("m did not enter map view")
	}
	if ref := m.mp.refs[m.mp.focus]; ref.kind != refItem || ref.item.Text != "beta" {
		t.Errorf("map focus not seeded from list cursor: %+v", ref)
	}

	m.handleMapKey(keyPress("m"))
	if m.mapView {
		t.Errorf("m in map view did not return to the list")
	}
}
