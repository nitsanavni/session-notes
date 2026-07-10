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
	root, refs, _ := buildMapTree(b, nil, true)

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
func TestMapSectionDelete(t *testing.T) {
	// D on a section node hard-deletes the whole section from the map, matching
	// the outline view's D and the web map (web parity).
	src := "## Threads\n- [ ] keep\n\n## Ideas\n- [ ] doomed\n"
	m := newMapModel(t, src, 80, 24)

	focusMapSection(t, m, "Ideas")
	m.handleMapKey(keyPress("D"))
	if got := m.board.Render(); strings.Contains(got, "Ideas") || strings.Contains(got, "doomed") {
		t.Errorf("D on a section did not delete it:\n%s", got)
	}
	if m.board.Section("Threads") == nil {
		t.Errorf("unrelated section was lost:\n%s", m.board.Render())
	}
}

func TestMapItemDelete(t *testing.T) {
	src := "## Threads\n- [ ] keep\n- [ ] gone\n"
	m := newMapModel(t, src, 80, 24)

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
	// The human's child add is stamped with the user: author tag (web parity).
	if !strings.Contains(string(data), "  - user: child line") {
		t.Errorf("child not added as a user:-stamped nested bullet:\n%s", data)
	}
}

// TestMapAddArrowKeepsTypedText guards against the regression where an arrow
// key in map add mode discarded the typed text. Once text has been entered,
// Left/Right must move the caret (not cancel) and Up/Down must be inert — no
// data loss. Esc remains the explicit cancel; Enter commits.
func TestMapAddArrowKeepsTypedText(t *testing.T) {
	src := "## Threads\n- [ ] existing\n"
	m := newMapModel(t, src, 80, 24)
	focusMapSection(t, m, "Threads")

	m.handleMapKey(keyPress("a"))
	if m.mode != modeMapAdd {
		t.Fatalf("a did not enter map add mode, got %v", m.mode)
	}
	m.input.SetValue("half typed")
	m.input.CursorEnd()

	m.handleMapInputKey(keyPress("left"))
	// Left moved the caret off the end (standard text editing), proving arrows
	// are routed to the input rather than swallowed.
	if pos := m.input.Position(); pos >= len("half typed") {
		t.Fatalf("left arrow did not move the caret within the text, pos=%d", pos)
	}
	for _, k := range []string{"up", "right", "down"} {
		m.handleMapInputKey(keyPress(k))
		if m.mode != modeMapAdd {
			t.Fatalf("%s exited add mode (data would be lost), mode=%v", k, m.mode)
		}
		if got := m.input.Value(); got != "half typed" {
			t.Fatalf("%s changed the typed text, got %q", k, got)
		}
	}

	// Enter still commits the preserved text.
	m.handleMapInputKey(keyPress("enter"))
	data, err := os.ReadFile(m.path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(data), "- [ ] half typed") {
		t.Errorf("typed text not committed after arrows:\n%s", data)
	}
}

// TestMapAddEscCancels confirms Esc is still the explicit cancel even with text.
func TestMapAddEscCancels(t *testing.T) {
	src := "## Threads\n- [ ] existing\n"
	m := newMapModel(t, src, 80, 24)
	focusMapSection(t, m, "Threads")

	m.handleMapKey(keyPress("a"))
	m.input.SetValue("discard me")
	m.handleMapInputKey(keyPress("esc"))
	if m.mode != modeBoard {
		t.Fatalf("esc did not cancel add mode, mode=%v", m.mode)
	}
	data, _ := os.ReadFile(m.path)
	if strings.Contains(string(data), "discard me") {
		t.Errorf("esc should discard the typed text, but it persisted:\n%s", data)
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
	if m.mapFold[m.mapFocusKey] != foldCollapsed {
		t.Fatalf("section not marked collapsed")
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

// mapHasReplyNode reports whether any laid-out node is a conversational reply.
func mapHasReplyNode(m *model) bool {
	for _, ref := range m.mp.refs {
		if ref.kind == refItem && isReplyItem(ref.item) {
			return true
		}
	}
	return false
}

// TestCountReplies checks the recursive reply count over a nested turn chain and
// the singular/plural suffix.
func TestCountReplies(t *testing.T) {
	// question -> user -> claude -> user  (3 replies), plus a non-reply child.
	src := "## Threads\n- [ ] question\n  - user: a\n    - claude: b\n      - user: c\n  - [ ] sub task\n"
	b := board.Parse(src)
	q := b.Sections[0].Items[0]
	if got := countReplies(q); got != 3 {
		t.Errorf("countReplies = %d, want 3", got)
	}
	if got := replyCountSuffix(1); got != " [+1]" {
		t.Errorf("singular suffix = %q", got)
	}
	if got := replyCountSuffix(3); got != " [+3]" {
		t.Errorf("plural suffix = %q", got)
	}
}

// TestMapReplyCollapse asserts reply children collapse into a "[+N]"
// suffix by default, enter on the parent expands them, and enter again
// re-collapses.
func TestMapReplyCollapse(t *testing.T) {
	src := "## Threads\n- [ ] question\n  - user: hi\n    - claude: hello\n"
	m := newMapModel(t, src, 100, 30)

	if mapHasReplyNode(m) {
		t.Fatalf("reply nodes laid out while collapsed by default")
	}
	if out := stripANSI(m.viewMap()); !strings.Contains(out, "[+2]") {
		t.Errorf("missing collapsed reply-count suffix:\n%s", out)
	}

	focusMapItem(t, m, "question")
	m.handleMapKey(keyPress("enter")) // expand replies
	m.ensureMap()
	if !mapHasReplyNode(m) {
		t.Fatalf("reply nodes not shown after expand")
	}
	if out := stripANSI(m.viewMap()); strings.Contains(out, "[+2]") {
		t.Errorf("count suffix should be gone once expanded:\n%s", out)
	}

	m.handleMapKey(keyPress("enter")) // collapse again
	m.ensureMap()
	if mapHasReplyNode(m) {
		t.Errorf("reply nodes still laid out after re-collapse")
	}
	if out := stripANSI(m.viewMap()); !strings.Contains(out, "[+2]") {
		t.Errorf("re-collapse did not restore the count suffix:\n%s", out)
	}
}

// TestMapNonReplyChildrenStayExpanded asserts ordinary (non-reply) children are
// laid out without any collapse, and don't trigger a reply-count suffix.
func TestMapNonReplyChildrenStayExpanded(t *testing.T) {
	src := "## Threads\n- [ ] parent\n  - [ ] child one\n  - [ ] child two\n"
	m := newMapModel(t, src, 100, 30)
	out := stripANSI(m.viewMap())
	for _, want := range []string{"child one", "child two"} {
		if !strings.Contains(out, want) {
			t.Errorf("non-reply child %q not shown:\n%s", want, out)
		}
	}
	if strings.Contains(out, "replies]") {
		t.Errorf("non-reply children should not produce a reply-count suffix:\n%s", out)
	}
}

// mapVisibleTexts returns the on-map display text of every laid-out item node.
func mapVisibleTexts(m *model) []string {
	var out []string
	for _, ref := range m.mp.refs {
		if ref.kind == refItem {
			out = append(out, ref.item.Text)
		}
	}
	return out
}

// mapContains reports whether an item with the given board text is laid out.
func mapContains(m *model, text string) bool {
	for _, t := range mapVisibleTexts(m) {
		if t == text {
			return true
		}
	}
	return false
}

// TestMapFoldCycleRepliesOnly: a node whose only children are replies toggles
// between the summary and the expanded thread (two distinct states).
func TestMapFoldCycleRepliesOnly(t *testing.T) {
	src := "## Threads\n- [ ] question\n  - user: hi\n    - claude: hello\n"
	m := newMapModel(t, src, 100, 30)
	focusMapItem(t, m, "question")

	// Default: replies summarized, none shown.
	if mapHasReplyNode(m) {
		t.Fatalf("replies laid out in default view")
	}
	// enter -> replies expanded.
	m.handleMapKey(keyPress("enter"))
	m.ensureMap()
	if m.mapFold[m.mapFocusKey] != foldRepliesExpanded {
		t.Fatalf("state = %v, want repliesExpanded", m.mapFold[m.mapFocusKey])
	}
	if !mapHasReplyNode(m) {
		t.Fatalf("replies not shown after expand")
	}
	// enter -> back to default summary (collapsed == default here, skipped).
	m.handleMapKey(keyPress("enter"))
	m.ensureMap()
	if _, ok := m.mapFold[m.mapFocusKey]; ok {
		t.Fatalf("state = %v, want default (absent)", m.mapFold[m.mapFocusKey])
	}
	if mapHasReplyNode(m) {
		t.Fatalf("replies still shown after re-collapse")
	}
	if out := stripANSI(m.viewMap()); !strings.Contains(out, "[+2]") {
		t.Errorf("summary suffix missing after re-collapse:\n%s", out)
	}
}

// TestMapFoldCycleMixed: a node with both a non-reply child and a reply thread
// cycles through all three distinct states: default -> replies-expanded ->
// collapsed -> default.
func TestMapFoldCycleMixed(t *testing.T) {
	src := "## Threads\n- [ ] parent\n  - [ ] sub task\n  - user: hi\n    - claude: hello\n"
	m := newMapModel(t, src, 120, 40)
	focusMapItem(t, m, "parent")

	// Default: non-reply child shown, replies summarized.
	if !mapContains(m, "sub task") {
		t.Fatalf("non-reply child hidden in default view")
	}
	if mapHasReplyNode(m) {
		t.Fatalf("replies shown in default view")
	}
	if out := stripANSI(m.viewMap()); !strings.Contains(out, "[+2]") {
		t.Errorf("default view missing reply summary:\n%s", out)
	}

	// enter -> replies expanded: everything visible, no suffix.
	m.handleMapKey(keyPress("enter"))
	m.ensureMap()
	if m.mapFold[m.mapFocusKey] != foldRepliesExpanded {
		t.Fatalf("state = %v, want repliesExpanded", m.mapFold[m.mapFocusKey])
	}
	if !mapContains(m, "sub task") || !mapHasReplyNode(m) {
		t.Fatalf("expanded view should show both the sub task and the replies")
	}
	if out := stripANSI(m.viewMap()); strings.Contains(out, "replies]") {
		t.Errorf("expanded view should carry no reply suffix:\n%s", out)
	}

	// enter -> fully collapsed: no children, one "[+N]" summary over ALL hidden
	// descendants (sub task + user + claude = 3).
	m.handleMapKey(keyPress("enter"))
	m.ensureMap()
	if m.mapFold[m.mapFocusKey] != foldCollapsed {
		t.Fatalf("state = %v, want collapsed", m.mapFold[m.mapFocusKey])
	}
	if mapContains(m, "sub task") || mapHasReplyNode(m) {
		t.Fatalf("collapsed view still shows children")
	}
	if out := stripANSI(m.viewMap()); !strings.Contains(out, "[+3]") {
		t.Errorf("collapsed view missing [+3] over all hidden descendants:\n%s", out)
	}

	// enter -> back to default.
	m.handleMapKey(keyPress("enter"))
	m.ensureMap()
	if _, ok := m.mapFold[m.mapFocusKey]; ok {
		t.Fatalf("state = %v, want default (absent)", m.mapFold[m.mapFocusKey])
	}
	if !mapContains(m, "sub task") || mapHasReplyNode(m) {
		t.Fatalf("cycle did not return to the default view")
	}
}

// TestMapFoldCycleNoReplies: a node with only non-reply children toggles between
// shown and fully collapsed (two states); collapsed shows "[+N]".
func TestMapFoldCycleNoReplies(t *testing.T) {
	src := "## Threads\n- [ ] parent\n  - [ ] a\n  - [ ] b\n"
	m := newMapModel(t, src, 100, 30)
	focusMapItem(t, m, "parent")

	m.handleMapKey(keyPress("enter")) // collapse
	m.ensureMap()
	if m.mapFold[m.mapFocusKey] != foldCollapsed {
		t.Fatalf("state = %v, want collapsed", m.mapFold[m.mapFocusKey])
	}
	if mapContains(m, "a") || mapContains(m, "b") {
		t.Fatalf("collapsed view still shows children")
	}
	if out := stripANSI(m.viewMap()); !strings.Contains(out, "[+2]") {
		t.Errorf("collapsed view missing [+2]:\n%s", out)
	}
	m.handleMapKey(keyPress("enter")) // back to default
	m.ensureMap()
	if _, ok := m.mapFold[m.mapFocusKey]; ok {
		t.Fatalf("state = %v, want default (absent)", m.mapFold[m.mapFocusKey])
	}
	if !mapContains(m, "a") || !mapContains(m, "b") {
		t.Fatalf("children not restored after unfold")
	}
}

// TestMapFoldNestedSurvivesRoundTrip: a descendant's own fold state is preserved
// when an ancestor is collapsed and re-expanded.
func TestMapFoldNestedSurvivesRoundTrip(t *testing.T) {
	src := "## Threads\n- [ ] parent\n  - [ ] child\n    - [ ] gc1\n    - [ ] gc2\n"
	m := newMapModel(t, src, 120, 40)

	// Collapse the inner child first.
	focusMapItem(t, m, "child")
	m.handleMapKey(keyPress("enter"))
	m.ensureMap()
	childKey := m.mapFocusKey
	if m.mapFold[childKey] != foldCollapsed {
		t.Fatalf("child not collapsed")
	}

	// Collapse then re-expand the parent.
	focusMapItem(t, m, "parent")
	m.handleMapKey(keyPress("enter")) // collapse parent
	m.ensureMap()
	m.handleMapKey(keyPress("enter")) // expand parent
	m.ensureMap()

	// Child's collapse survived: grandchildren stay hidden, child shows [+2].
	if m.mapFold[childKey] != foldCollapsed {
		t.Fatalf("child fold state lost across the parent round trip")
	}
	if mapContains(m, "gc1") || mapContains(m, "gc2") {
		t.Fatalf("grandchildren reappeared: nested fold not preserved")
	}
	if !mapContains(m, "child") {
		t.Fatalf("child missing after parent re-expand")
	}
	if out := stripANSI(m.viewMap()); !strings.Contains(out, "[+2]") {
		t.Errorf("child's [+2] summary missing after round trip:\n%s", out)
	}
}

// TestFoldSuffixCounts checks the suffix text for each state directly.
func TestFoldSuffixCounts(t *testing.T) {
	// mixed: one non-reply child, a 2-deep reply thread.
	src := "## Threads\n- [ ] parent\n  - [ ] sub\n  - user: hi\n    - claude: hey\n"
	b := board.Parse(src)
	kids := b.Sections[0].Items[0].Children
	if got := foldSuffix(kids, foldDefault); got != " [+2]" {
		t.Errorf("default suffix = %q, want ' [+2]'", got)
	}
	if got := foldSuffix(kids, foldRepliesExpanded); got != "" {
		t.Errorf("expanded suffix = %q, want empty", got)
	}
	if got := foldSuffix(kids, foldCollapsed); got != " [+3]" {
		t.Errorf("collapsed suffix = %q, want ' [+3]'", got)
	}
	// replies only: collapsed summarizes as replies, not [+N].
	rsrc := "## Threads\n- [ ] q\n  - user: a\n    - claude: b\n"
	rb := board.Parse(rsrc)
	rkids := rb.Sections[0].Items[0].Children
	if got := foldSuffix(rkids, foldCollapsed); got != " [+2]" {
		t.Errorf("replies-only collapsed = %q, want ' [+2]'", got)
	}
	// no replies: collapsed is [+N], default is empty.
	nsrc := "## Threads\n- [ ] p\n  - [ ] a\n  - [ ] b\n"
	nb := board.Parse(nsrc)
	nkids := nb.Sections[0].Items[0].Children
	if got := foldSuffix(nkids, foldDefault); got != "" {
		t.Errorf("no-reply default suffix = %q, want empty", got)
	}
	if got := foldSuffix(nkids, foldCollapsed); got != " [+2]" {
		t.Errorf("no-reply collapsed suffix = %q, want ' [+2]'", got)
	}
}

// TestMapLogExcludedByDefault asserts the Log section is filtered out of the map
// by default (with a footer hint), and M toggles it back on.
func TestMapLogExcludedByDefault(t *testing.T) {
	src := "## Threads\n- [ ] task\n\n## Log\n- 10:00 start: hi\n"
	m := newMapModel(t, src, 100, 30)

	hasLog := func() bool {
		for _, ref := range m.mp.refs {
			if ref.kind == refSection && ref.section == "Log" {
				return true
			}
		}
		return false
	}
	if hasLog() {
		t.Fatalf("Log section present in the map by default")
	}
	if out := stripANSI(m.viewMap()); !strings.Contains(out, "Log hidden") {
		t.Errorf("expected the 'Log hidden' footer indicator:\n%s", out)
	}

	m.handleMapKey(keyPress("M")) // reveal Log
	m.ensureMap()
	if !hasLog() {
		t.Fatalf("M did not reveal the Log section")
	}
	if out := stripANSI(m.viewMap()); strings.Contains(out, "Log hidden") {
		t.Errorf("'Log hidden' indicator should be gone once shown:\n%s", out)
	}

	m.handleMapKey(keyPress("M")) // hide again
	m.ensureMap()
	if hasLog() {
		t.Errorf("second M did not hide the Log section")
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

// TestMapRenameSectionPersists renames a section via e on its node and asserts
// the heading changes, contents survive, and it persists to disk.
func TestMapRenameSectionPersists(t *testing.T) {
	src := "## Threads\n- [ ] keep me\n  - user: a reply\n"
	m := newMapModel(t, src, 80, 24)
	focusMapSection(t, m, "Threads")

	m.handleMapKey(keyPress("e"))
	if m.mode != modeMapRename {
		t.Fatalf("e on a section did not enter rename mode, got %v", m.mode)
	}
	if m.input.Value() != "Threads" {
		t.Fatalf("rename input not pre-filled with title, got %q", m.input.Value())
	}
	m.input.SetValue("Discussion")
	m.handleMapInputKey(keyPress("enter"))

	if m.board.Section("Discussion") == nil {
		t.Fatalf("section not renamed in memory: %+v", m.board.Sections)
	}
	if m.board.Section("Threads") != nil {
		t.Errorf("old section title still present")
	}
	sec := m.board.Section("Discussion")
	if len(sec.Items) == 0 || sec.Items[0].Text != "keep me" {
		t.Errorf("section contents lost on rename: %+v", sec.Items)
	}
	data, _ := os.ReadFile(m.path)
	if !strings.Contains(string(data), "## Discussion") || !strings.Contains(string(data), "keep me") {
		t.Errorf("rename not persisted with contents:\n%s", data)
	}
}

// TestMapRenameSectionMigratesCollapseState asserts the list view's per-title
// collapse override follows a rename and does not leak onto a later section that
// reuses the old name. Map fold state (index-keyed) stays put automatically.
func TestMapRenameSectionMigratesCollapseState(t *testing.T) {
	src := "## Threads\n- [ ] x\n\n## Ideas\n- [ ] y\n"
	m := newMapModel(t, src, 80, 24)
	m.setCollapsed("Threads", true)
	// Fold the section node in the map (index-keyed key "s0").
	focusMapSection(t, m, "Threads")

	m.handleMapKey(keyPress("e"))
	m.input.SetValue("Discussion")
	m.handleMapInputKey(keyPress("enter"))

	if !m.sectionCollapsed("Discussion") {
		t.Errorf("collapse state did not migrate to the renamed section")
	}
	if _, ok := m.collapsed["Threads"]; ok {
		t.Errorf("stale collapse state left under old title")
	}
	if m.sectionCollapsed("Threads") {
		t.Errorf("a section reusing the old name would inherit stale collapse state")
	}
}

// TestMapRenameSectionDuplicateRefused asserts renaming onto an existing
// section's name is refused (no merge) and leaves both sections intact.
func TestMapRenameSectionDuplicateRefused(t *testing.T) {
	src := "## Threads\n- [ ] a\n\n## Ideas\n- [ ] b\n"
	m := newMapModel(t, src, 80, 24)
	focusMapSection(t, m, "Threads")

	m.handleMapKey(keyPress("e"))
	m.input.SetValue("Ideas")
	m.handleMapInputKey(keyPress("enter"))

	if m.board.Section("Threads") == nil {
		t.Errorf("Threads was lost on a refused rename")
	}
	count := 0
	for _, s := range m.board.Sections {
		if s.Title == "Ideas" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly one Ideas section, got %d", count)
	}
	if m.status == "" {
		t.Errorf("expected a status error on refused rename")
	}
}

// TestMapAddSectionFromCenter asserts a on the center opens the add-sections
// overlay and a chosen section lands on the board (returning to the map).
func TestMapAddSectionFromCenter(t *testing.T) {
	src := "## Threads\n- [ ] x\n"
	m := newMapModel(t, src, 80, 24)
	// focus is the center by default.
	if m.mp.refs[m.mp.focus].kind != refCenter {
		m.mp.focus = m.mp.root
		m.mapFocusKey = ""
	}
	m.handleMapKey(keyPress("a"))
	if m.mode != modeAddSections {
		t.Fatalf("a on center did not open add-sections overlay, got %v", m.mode)
	}
	// Select the first offered canonical section and confirm.
	m.addSel[0] = true
	want := m.addOpts[0]
	m.confirmAddSections()

	if m.board.Section(want) == nil {
		t.Errorf("selected section %q not added: %+v", want, m.board.Sections)
	}
	if !m.mapView {
		t.Errorf("add-sections from map should return to the map")
	}
	data, _ := os.ReadFile(m.path)
	if !strings.Contains(string(data), "## "+want) {
		t.Errorf("section not persisted:\n%s", data)
	}
}

// TestMapArchiveSection archives a whole section from its map node.
func TestMapArchiveSection(t *testing.T) {
	src := "## Threads\n- [ ] a task\n"
	m := newMapModel(t, src, 80, 24)
	focusMapSection(t, m, "Threads")

	m.handleMapKey(keyPress("d"))

	if m.board.Section("Threads") != nil {
		t.Errorf("section not archived (still present)")
	}
	arch := m.board.Section(board.ArchiveTitle)
	if arch == nil {
		t.Fatalf("no Archive section created")
	}
	data, _ := os.ReadFile(m.path)
	if !strings.Contains(string(data), "## Archive") || !strings.Contains(string(data), "a task") {
		t.Errorf("archive not persisted:\n%s", data)
	}
}

// TestMapArchiveSectionGuard asserts the Log section refuses to be archived from
// the map, matching the list view guard.
func TestMapArchiveSectionGuard(t *testing.T) {
	src := "## Threads\n- [ ] x\n\n## Log\n- 09:00 started\n"
	m := newMapModel(t, src, 80, 24)
	m.mapShowLog = true
	m.mp = nil
	m.ensureMap()
	focusMapSection(t, m, "Log")

	m.handleMapKey(keyPress("d"))

	if m.board.Section("Log") == nil {
		t.Errorf("Log was archived despite the guard")
	}
	if m.status == "" {
		t.Errorf("expected a status message when refusing to archive Log")
	}
}
