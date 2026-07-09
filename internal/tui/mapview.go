package tui

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/nitsanavni/session-notes/internal/board"
	"github.com/nitsanavni/session-notes/internal/mindmap"
)

// refKind classifies a map node by what board thing it stands for. The map is a
// bridge over the board (built fresh in buildMapTree), not a shared model: the
// center is the board title, the first ring is the section skeleton (always all
// sections, even empty — protected), and the subtrees are the section items.
type refKind int

const (
	refCenter  refKind = iota // the title node at the map's center
	refSection                // a "## " section node (protected: no mark/delete/edit)
	refItem                   // a bullet item node
)

// mapRef ties a mindmap.Node back to the board thing it renders. section is the
// owning section title; item is the board item for refItem nodes.
type mapRef struct {
	kind    refKind
	section string
	item    *board.Item
}

// mapState is the transient, built map: the bridged tree, its center-outward
// layout, the node→board back-references and node→stable-key index, and the
// focused node. It is rebuilt (m.mp niled) on every board change; focus and fold
// survive via m.mapFocusKey / m.mapFold, both keyed by the stable node key.
type mapState struct {
	root   *mindmap.Node
	layout mindmap.Layout
	refs   map[*mindmap.Node]mapRef
	keys   map[*mindmap.Node]string
	focus  *mindmap.Node
}

// placement returns the layout placement for a node, or nil.
func (ms *mapState) placement(n *mindmap.Node) *mindmap.Placement {
	for _, p := range ms.layout.Placements {
		if p.Node == n {
			return p
		}
	}
	return nil
}

// itemKey returns the stable key of the node standing for a given board item.
func (ms *mapState) itemKey(it *board.Item) (string, bool) {
	for n, ref := range ms.refs {
		if ref.kind == refItem && ref.item == it {
			return ms.keys[n], true
		}
	}
	return "", false
}

// boardTitle is the map's center text: the frontmatter title, else the session
// id, else the board file's basename, else a generic label.
func boardTitle(b *board.Board) string {
	if t := strings.TrimSpace(b.Frontmatter.Title); t != "" {
		return t
	}
	if s := strings.TrimSpace(b.Frontmatter.Session); s != "" {
		return s
	}
	if b.Path != "" {
		return strings.TrimSuffix(filepath.Base(b.Path), ".md")
	}
	return "board"
}

// statusMarkerLiteral is the list view's literal glyph for a status, baked into
// the map node's text so the map shows the same [ ]/[>]/[x]/[?] markers. Styling
// (dim done, urgent highlight, blocked/in-progress color) is applied at render
// time from the node's board ref, not from these characters.
func statusMarkerLiteral(s board.Status) string {
	switch s {
	case board.StatusOpen:
		return "[ ]"
	case board.StatusInProgress:
		return "[>]"
	case board.StatusDone:
		return "[x]"
	case board.StatusBlocked:
		return "[?]"
	default:
		return ""
	}
}

// mapNodeText is a board item's on-map text: its status marker (if any), a "!!"
// urgent prefix (if urgent), then the item text.
func mapNodeText(it *board.Item) string {
	var b strings.Builder
	if mk := statusMarkerLiteral(it.Status); mk != "" {
		b.WriteString(mk)
		b.WriteByte(' ')
	}
	if it.Urgent {
		b.WriteString("!! ")
	}
	b.WriteString(it.Text)
	return b.String()
}

// replyRe matches a reply sub-bullet: an item whose text opens with a "user:"
// or "claude:" author token (the marker/urgent prefix is already stripped from
// Item.Text, so no tolerance for those is needed here).
var replyRe = regexp.MustCompile(`^(claude|user):`)

// isReplyItem reports whether an item is a conversational reply (user:/claude:).
func isReplyItem(it *board.Item) bool {
	return it != nil && it.IsItem() && replyRe.MatchString(it.Text)
}

// countReplies counts the reply items hidden when it's reply children collapse:
// its direct reply children plus every reply nested beneath them (a
// user->claude->user turn chain counts as 3). Non-reply branches are excluded.
func countReplies(it *board.Item) int {
	return countRepliesIn(it.Children)
}

// countRepliesIn counts reply items over a child slice (recursively down reply
// chains only), so it works for both an item's children and a section's items.
func countRepliesIn(items []*board.Item) int {
	n := 0
	for _, c := range items {
		if isReplyItem(c) {
			n++
			n += countRepliesIn(c.Children)
		}
	}
	return n
}

// countDescendantsIn counts every map-visible (IsItem) descendant under a child
// slice — replies and non-replies alike. This is the N behind a fully-collapsed
// node's "[+N]" suffix.
func countDescendantsIn(items []*board.Item) int {
	n := 0
	for _, c := range items {
		if !c.IsItem() {
			continue
		}
		n++
		n += countDescendantsIn(c.Children)
	}
	return n
}

// hasNonReplyChild reports whether a child slice has any non-reply item child.
func hasNonReplyChild(items []*board.Item) bool {
	for _, c := range items {
		if c.IsItem() && !isReplyItem(c) {
			return true
		}
	}
	return false
}

// replyCountSuffix is the collapsed-reply indicator appended to a parent node,
// e.g. " [3 replies]" / " [1 reply]".
func replyCountSuffix(n int) string {
	if n == 1 {
		return " [1 reply]"
	}
	return fmt.Sprintf(" [%d replies]", n)
}

// foldState is a node's place in the unified fold cycle. A node has one state,
// persisted by stable key in model.mapFold (absent == foldDefault).
type foldState int

const (
	// foldDefault is the resting view: non-reply children shown, reply children
	// summarized into a "[N replies]" suffix.
	foldDefault foldState = iota
	// foldCollapsed hides ALL children behind one suffix ("[+N]" over every
	// hidden descendant, or "[N replies]" when the hidden set is replies only).
	foldCollapsed
	// foldRepliesExpanded shows everything: non-reply children AND the reply
	// thread, no suffix.
	foldRepliesExpanded
)

// foldSuffix is the "[…]" summary a node in state st shows for the children of
// the given slice. Empty when nothing is hidden (a leaf, or a fully-expanded
// node). "[+N]" counts every hidden descendant (replies included); "[N replies]"
// is used when the hidden set is replies only.
func foldSuffix(items []*board.Item, st foldState) string {
	replies := countRepliesIn(items)
	total := countDescendantsIn(items)
	if st == foldCollapsed {
		if total == 0 {
			return ""
		}
		if replies == total { // everything hidden is a reply
			return replyCountSuffix(replies)
		}
		return fmt.Sprintf(" [+%d]", total)
	}
	// Default view: reply children fold into a count; expanded view hides nothing.
	if st != foldRepliesExpanded && replies > 0 {
		return replyCountSuffix(replies)
	}
	return ""
}

// nextFold advances a node's fold state one step of the collapse cycle
// (collapsed -> default -> replies-expanded -> collapsed), skipping any state
// that would render identically to the current one for this node's children:
//   - no children: no change (nothing to fold).
//   - non-reply children, no replies: default <-> collapsed (replies-expanded ==
//     default, skipped).
//   - replies only: default <-> replies-expanded (collapsed == default, skipped).
//   - mixed: default -> replies-expanded -> collapsed -> default (all distinct).
func nextFold(items []*board.Item, cur foldState) foldState {
	hasReply := countRepliesIn(items) > 0
	hasNonReply := hasNonReplyChild(items)
	switch {
	case hasReply && hasNonReply:
		switch cur {
		case foldDefault:
			return foldRepliesExpanded
		case foldRepliesExpanded:
			return foldCollapsed
		default:
			return foldDefault
		}
	case hasReply: // replies only: toggle summary <-> expanded
		if cur == foldRepliesExpanded {
			return foldDefault
		}
		return foldRepliesExpanded
	case hasNonReply: // no replies: toggle shown <-> collapsed
		if cur == foldCollapsed {
			return foldDefault
		}
		return foldCollapsed
	default:
		return cur // leaf: nothing to fold
	}
}

// mapNextStatus is the map's `space` cycle: none -> [ ] -> [>] -> [x] -> [?] ->
// none. Unlike the list view's cycle it visits every state (including blocked)
// and can return to no-marker, per the map key scheme.
func mapNextStatus(s board.Status) board.Status {
	switch s {
	case board.StatusNone:
		return board.StatusOpen
	case board.StatusOpen:
		return board.StatusInProgress
	case board.StatusInProgress:
		return board.StatusDone
	case board.StatusDone:
		return board.StatusBlocked
	default: // StatusBlocked
		return board.StatusNone
	}
}

// buildMapTree bridges a board into a mindmap tree: a heading root (the title)
// whose children are all sections in order (empty ones included), each with its
// item subtree. Nodes are keyed by a stable position path ("" center, "sN"
// section, "sN/i/j" nested item) so focus and fold survive rebuilds; refs point
// each node back to its board thing. Continuation (non-item) lines are skipped.
func buildMapTree(b *board.Board, fold map[string]foldState, showLog bool) (*mindmap.Node, map[*mindmap.Node]mapRef, map[*mindmap.Node]string) {
	refs := map[*mindmap.Node]mapRef{}
	keys := map[*mindmap.Node]string{}

	root := &mindmap.Node{Text: boardTitle(b), Heading: true}
	refs[root] = mapRef{kind: refCenter}
	keys[root] = ""

	// walk emits the visible children of a parent given the PARENT's fold state,
	// and appends each child's own fold suffix (from the child's own state).
	// Nodes are never mindmap.Folded — collapse is done by simply not emitting
	// the hidden children, so the suffix counts are fully under our control.
	var walk func(items []*board.Item, parent *mindmap.Node, parentKey, sec string, parentState foldState)
	walk = func(items []*board.Item, parent *mindmap.Node, parentKey, sec string, parentState foldState) {
		if parentState == foldCollapsed {
			return // whole subtree hidden behind the parent's suffix
		}
		showReplies := parentState == foldRepliesExpanded
		idx := 0
		for _, it := range items {
			if !it.IsItem() {
				continue // continuation / stray lines are not map nodes
			}
			ck := parentKey + "/" + strconv.Itoa(idx)
			idx++
			if isReplyItem(it) && !showReplies {
				continue // folded into the parent's "[N replies]" suffix
			}
			st := fold[ck]
			cn := &mindmap.Node{Text: mapNodeText(it) + foldSuffix(it.Children, st)}
			refs[cn] = mapRef{kind: refItem, section: sec, item: it}
			keys[cn] = ck
			parent.Children = append(parent.Children, cn)
			walk(it.Children, cn, ck, sec, st)
		}
	}

	for i, s := range b.Sections {
		// The append-only Log section is excluded by default (noise); M toggles
		// it on. Section keys stay index-based so skipping Log doesn't shift the
		// stable keys of the other sections.
		if s.Title == board.LogTitle && !showLog {
			continue
		}
		skey := "s" + strconv.Itoa(i)
		sState := fold[skey]
		sn := &mindmap.Node{Text: s.Title + foldSuffix(s.Items, sState)}
		refs[sn] = mapRef{kind: refSection, section: s.Title}
		keys[sn] = skey
		root.Children = append(root.Children, sn)
		walk(s.Items, sn, skey, s.Title, sState)
	}
	return root, refs, keys
}

// mapNodeMaxWidth caps a node's on-map display width; longer text is truncated
// with a "…" (or wrapped when the node is expanded via `w`). The focused node's
// full text still shows in the detail footer. This is a TUI choice; the mindmap
// package never truncates by default (the mm golden fixtures depend on that).
const mapNodeMaxWidth = 40

// ensureMap builds the map layout if it is stale (nil), re-resolving focus from
// the persisted mapFocusKey (falling back to the center when it no longer maps).
func (m *model) ensureMap() {
	if m.mp != nil {
		return
	}
	root, refs, keys := buildMapTree(m.board, m.mapFold, m.mapShowLog)
	expanded := map[*mindmap.Node]bool{}
	for n, k := range keys {
		if m.mapExpanded[k] {
			expanded[n] = true
		}
	}
	opts := mindmap.Options{MaxWidth: mapNodeMaxWidth, Expanded: expanded}
	ms := &mapState{
		root:   root,
		layout: mindmap.LayoutTreeOpts("", root, root.Children, mindmap.Rounded, opts),
		refs:   refs,
		keys:   keys,
	}
	for n, k := range keys {
		if k == m.mapFocusKey {
			ms.focus = n
			break
		}
	}
	if ms.focus == nil {
		ms.focus = root
		m.mapFocusKey = ""
	}
	m.mp = ms
}

// enterMap switches to the map view, seeding focus from the list cursor's item
// (or section header) so the two views agree on where you are.
func (m *model) enterMap() {
	m.mapView = true
	m.mapFocusKey = ""
	m.mp = nil
	m.ensureMap()
	if it := m.currentItem(); it != nil {
		if k, ok := m.mp.itemKey(it); ok {
			m.mapFocusKey = k
		}
	} else if sec, ok := m.onHeader(); ok {
		for i, s := range m.board.Sections {
			if s == sec {
				m.mapFocusKey = "s" + strconv.Itoa(i)
				break
			}
		}
	}
	m.mp = nil // rebuild so mp.focus reflects the seeded key
	m.ensureMap()
}

// --- navigation ---

// mapMove moves focus in the given direction ('h'/'j'/'k'/'l'). Navigation is
// TREE-STRUCTURAL, mirroring mm rather than scoring by pixel distance (the old
// geometric scorer is the root cause of the reported k-from-a-section surprise:
// it demanded candidates be strictly on one side of the focus CENTER and then
// weighted cross-axis distance, so vertically-stacked first-ring siblings — who
// share a column but have different widths, hence different centers — were
// rejected or mis-ranked, and up/down could jump across the map or not move).
//
// The structural model, which is deterministic and always lands on the visually
// adjacent node when one exists:
//   - j/k (down/up): walk the focused node's siblings ON ITS SIDE of the center,
//     in document (top-to-bottom) order. Siblings on the same side are stacked
//     vertically in that order, so the previous/next sibling is the node
//     visually above/below. Stops at the ends; never dives into a subtree or
//     crosses the map. On the center, step to the nearest first-ring node in
//     that vertical direction.
//   - h/l (left/right): move along the parent/child axis. Toward the center is
//     the parent; away from the center enters the branch at its first child.
//     From the center, l enters the right side and h the left side.
func (m *model) mapMove(dir rune) {
	switch dir {
	case 'j':
		m.mapVertical(1)
	case 'k':
		m.mapVertical(-1)
	case 'l':
		m.mapLateral(1)
	case 'h':
		m.mapLateral(-1)
	}
}

// setMapFocus points focus (and the persisted key) at a node.
func (m *model) setMapFocus(n *mindmap.Node) {
	if n == nil {
		return
	}
	m.mp.focus = n
	m.mapFocusKey = m.mp.keys[n]
}

// mapVertical walks same-side siblings (delta +1 down / -1 up).
func (m *model) mapVertical(delta int) {
	ms := m.mp
	if ms.focus == ms.root {
		m.centerVertical(delta)
		return
	}
	side := ms.sideOf(ms.focus)
	sibs := ms.sameSideSiblings(ms.focus, side)
	i := indexOfNode(sibs, ms.focus)
	if i < 0 {
		return
	}
	j := i + delta
	if j >= 0 && j < len(sibs) {
		m.setMapFocus(sibs[j])
	}
}

// centerVertical moves off the center to the first-ring node nearest in the
// given vertical direction (delta +1 down / -1 up).
func (m *model) centerVertical(delta int) {
	ms := m.mp
	cp := ms.placement(ms.root)
	if cp == nil {
		return
	}
	var best *mindmap.Node
	bestD := 1 << 30
	for _, c := range ms.root.Children {
		p := ms.placement(c)
		if p == nil {
			continue
		}
		dr := p.Row - cp.Row
		if delta < 0 && dr >= 0 {
			continue
		}
		if delta > 0 && dr <= 0 {
			continue
		}
		if d := absInt(dr); d < bestD {
			bestD = d
			best = c
		}
	}
	m.setMapFocus(best)
}

// mapLateral moves along the parent/child axis (dir +1 right / -1 left).
func (m *model) mapLateral(dir int) {
	ms := m.mp
	if ms.focus == ms.root {
		// From the center, enter the first child on the chosen side.
		for _, c := range ms.root.Children {
			if ms.sideOf(c) == dir {
				m.setMapFocus(c)
				return
			}
		}
		return
	}
	side := ms.sideOf(ms.focus)
	if side == dir {
		// Outward, deeper into this node's own branch: its first visible child.
		if !ms.focus.Folded && len(ms.focus.Children) > 0 {
			m.setMapFocus(ms.focus.Children[0])
		}
		return
	}
	// Inward, toward the center: the parent.
	m.setMapFocus(ms.parentOf(ms.focus))
}

// sideOf reports which side of the center a node sits on (1 right, -1 left, 0
// center), from its placement.
func (ms *mapState) sideOf(n *mindmap.Node) int {
	if p := ms.placement(n); p != nil {
		return p.Side
	}
	return 0
}

// parentOf returns a node's parent in the bridged tree, or nil for the center.
func (ms *mapState) parentOf(n *mindmap.Node) *mindmap.Node {
	var found *mindmap.Node
	var walk func(p *mindmap.Node)
	walk = func(p *mindmap.Node) {
		for _, c := range p.Children {
			if c == n {
				found = p
				return
			}
			walk(c)
		}
	}
	walk(ms.root)
	return found
}

// sameSideSiblings returns the focused node's siblings that render on the given
// side of the center, in document order (deeper nodes' siblings all share the
// branch's side, so the filter is a no-op there).
func (ms *mapState) sameSideSiblings(n *mindmap.Node, side int) []*mindmap.Node {
	p := ms.parentOf(n)
	if p == nil {
		return []*mindmap.Node{n}
	}
	var sibs []*mindmap.Node
	for _, c := range p.Children {
		if ms.sideOf(c) == side {
			sibs = append(sibs, c)
		}
	}
	return sibs
}

// indexOfNode returns n's index in nodes, or -1.
func indexOfNode(nodes []*mindmap.Node, n *mindmap.Node) int {
	for i, x := range nodes {
		if x == n {
			return i
		}
	}
	return -1
}

// --- key handling ---

func (m *model) handleMapKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.status = ""
	m.ensureMap()
	key := msg.String()
	// Surprise recorder: capture a replayable before-state for map actions so
	// `!` can attach the recent trail to a feedback note (see feedback.go).
	var before *feedbackState
	if recordedMapKeys[key] {
		st := m.captureMapState()
		before = &st
	}
	defer func() {
		if before != nil {
			m.recordMapEvent(key, *before)
		}
	}()
	switch key {
	case "q", "esc":
		if m.cameFromDash {
			return m, m.enterDash()
		}
		return m, tea.Quit
	case "ctrl+c":
		return m, tea.Quit
	case "m":
		m.mapView = false
	case "M":
		m.mapShowLog = !m.mapShowLog
		m.mp = nil // section set changed -> re-layout
	case "h", "left":
		m.mapMove('h')
	case "j", "down":
		m.mapMove('j')
	case "k", "up":
		m.mapMove('k')
	case "l", "right":
		m.mapMove('l')
	case "enter":
		m.mapToggleFold()
	case "w":
		m.mapToggleExpand()
	case "a":
		m.startMapAdd()
	case "A":
		m.startMapAddSibling()
	case "d":
		m.mapArchive()
	case "e":
		m.startMapEdit()
	case " ":
		m.mapCycle()
	case "D":
		m.mapDelete()
	case "u":
		m.undo()
	case "ctrl+r":
		m.redo()
	case "y":
		m.yankBoardPath()
	case "r":
		m.reloadExternal()
		m.status = "reloaded"
	case "!":
		// A nav move surprised you: note where you expected focus to land.
		m.startInput(modeMapFeedback, "", "where did you expect it?")
	case "?":
		m.prevMode = m.mode
		m.mode = modeHelp
	}
	return m, nil
}

// mapToggleFold advances the focused node through the unified fold cycle
// (collapsed -> default -> replies-expanded -> collapsed, skipping states that
// render identically). enter reveals more each press until the whole subtree is
// shown, then one more press collapses it fully. The center never folds; a
// childless node has nothing to fold.
func (m *model) mapToggleFold() {
	ms := m.mp
	ref := ms.refs[ms.focus]
	if ref.kind == refCenter {
		m.status = "can't fold the center"
		return
	}
	// A node's children are the item's children, or a section's items.
	var items []*board.Item
	switch ref.kind {
	case refItem:
		items = ref.item.Children
	case refSection:
		if sec := m.board.Section(ref.section); sec != nil {
			items = sec.Items
		}
	}
	if countDescendantsIn(items) == 0 {
		m.status = "nothing to fold"
		return
	}
	cur := m.mapFold[m.mapFocusKey]
	next := nextFold(items, cur)
	if m.mapFold == nil {
		m.mapFold = map[string]foldState{}
	}
	if next == foldDefault {
		delete(m.mapFold, m.mapFocusKey)
	} else {
		m.mapFold[m.mapFocusKey] = next
	}
	m.mp = nil // re-layout with the new fold state
}

// mapToggleExpand flips the focused node between a single truncated line and a
// wrapped multi-line block (the `w` key). Nodes whose full text already fits
// within mapNodeMaxWidth render identically either way, but the toggle still
// records so the state is explicit.
func (m *model) mapToggleExpand() {
	if m.mapExpanded == nil {
		m.mapExpanded = map[string]bool{}
	}
	if m.mapExpanded[m.mapFocusKey] {
		delete(m.mapExpanded, m.mapFocusKey)
	} else {
		m.mapExpanded[m.mapFocusKey] = true
	}
	m.mp = nil // re-layout: node height/width changed
}

// mapCycle advances the focused item's status through the map cycle and saves.
// Sections and the center are not markable (beep via a status message).
func (m *model) mapCycle() {
	ref := m.mp.refs[m.mp.focus]
	if ref.kind != refItem {
		m.status = "sections can't be marked"
		return
	}
	it := ref.item
	m.snapshot()
	op := pendingOp{typ: opCycle, section: m.board.SectionTitleOf(it), rawLine: it.Raw()}
	it.Status = mapNextStatus(it.Status)
	op.newStatus = it.Status
	m.saveWithRebase(op)
	m.mp = nil // status change alters node text/width -> re-layout
}

// mapDelete hard-deletes the focused item's subtree (matching the list view's
// D, which has no confirm), moving focus to the parent. Sections are protected;
// the center is not deletable.
func (m *model) mapDelete() {
	ref := m.mp.refs[m.mp.focus]
	switch ref.kind {
	case refCenter:
		m.status = "can't delete the title"
		return
	case refSection:
		m.status = "sections can't be deleted from the map"
		return
	}
	it := ref.item
	m.snapshot()
	op := pendingOp{typ: opDeleteItem, section: m.board.SectionTitleOf(it), rawLine: it.Raw()}
	m.mapFocusKey = parentKeyOf(m.mapFocusKey)
	m.board.Remove(it)
	m.saveWithRebase(op)
	m.mp = nil
}

// parentKeyOf drops the last path segment of a node key: "s0/1/2" -> "s0/1",
// "s0/0" -> "s0", "s0" -> "" (the center).
func parentKeyOf(key string) string {
	if i := strings.LastIndexByte(key, '/'); i >= 0 {
		return key[:i]
	}
	return ""
}

func (m *model) startMapAdd() {
	ref := m.mp.refs[m.mp.focus]
	if ref.kind == refCenter {
		m.status = "focus a section or item to add"
		return
	}
	m.mapInputParent = nil
	m.mapInputSection = ref.section
	if ref.kind == refItem {
		m.mapInputParent = ref.item
	}
	m.startInput(modeMapAdd, "", "add child")
}

// startMapAddSibling adds next to the focused node: for an item, a new item
// under the same parent (top-level item for a section child, sibling reply in
// a thread); for a section node it behaves like add-into-section.
func (m *model) startMapAddSibling() {
	ref := m.mp.refs[m.mp.focus]
	if ref.kind == refCenter {
		m.status = "focus a node to add a sibling"
		return
	}
	m.mapInputParent = nil
	m.mapInputSection = ref.section
	if ref.kind == refItem {
		if par := m.mp.parentOf(m.mp.focus); par != nil {
			if pref, ok := m.mp.refs[par]; ok && pref.kind == refItem {
				m.mapInputParent = pref.item
			}
		}
	}
	m.startInput(modeMapAdd, "", "add sibling")
}

// mapArchive moves the focused item (+ subtree) to ## Archive, like the list
// view's d.
func (m *model) mapArchive() {
	ref := m.mp.refs[m.mp.focus]
	if ref.kind != refItem {
		m.status = "only items can be archived from the map"
		return
	}
	it := ref.item
	m.snapshot()
	op := pendingOp{typ: opArchiveItem, section: m.board.SectionTitleOf(it), rawLine: it.Raw()}
	if m.board.ArchiveItem(it) {
		m.mapFocusKey = parentKeyOf(m.mapFocusKey)
		m.rebuildPositions()
		m.saveWithRebase(op)
		m.status = "archived"
	}
}

func (m *model) startMapEdit() {
	ref := m.mp.refs[m.mp.focus]
	if ref.kind != refItem {
		m.status = "only items are editable"
		return
	}
	m.mapInputItem = ref.item
	m.mapInputSection = ref.section
	m.startInput(modeMapEdit, ref.item.DisplayText(), "edit item")
}

// handleMapInputKey drives the inline add/edit text entry, persisting through
// the same locked save+rebase path the list view uses.
func (m *model) handleMapInputKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.mode = modeBoard
		m.input.Blur()
		m.reloadExternal()
		return m, nil
	case "enter":
		text := strings.TrimSpace(m.input.Value())
		mo := m.mode
		m.mode = modeBoard
		m.input.Blur()
		if text == "" {
			return m, nil
		}
		m.snapshot()
		switch mo {
		case modeMapAdd:
			if m.mapInputParent != nil {
				op := pendingOp{typ: opAddChild, section: m.mapInputSection, rawLine: m.mapInputParent.Raw(), payload: text}
				m.board.AddReply(m.mapInputParent, text)
				m.saveWithRebase(op)
			} else {
				m.board.AddItem(m.mapInputSection, text)
				m.saveWithRebase(pendingOp{typ: opAdd, section: m.mapInputSection, payload: text})
			}
		case modeMapEdit:
			if m.mapInputItem != nil {
				op := pendingOp{typ: opEdit, section: m.board.SectionTitleOf(m.mapInputItem), rawLine: m.mapInputItem.Raw(), payload: text}
				m.mapInputItem.Text = text
				m.saveWithRebase(op)
			}
		}
		m.mp = nil
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// --- rendering ---

// mapConnStyle dims the box-drawing connectors so the node text reads first.
var mapConnStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

// mapNodeStyle is the base style for a node from its board ref, with the focus
// overlay (a bright reverse-video bar, matching the list view's selection)
// applied after so it is uniform across every status.
func mapNodeStyle(ref mapRef, focus bool) lipgloss.Style {
	var base lipgloss.Style
	switch ref.kind {
	case refCenter:
		base = styleTitle
	case refSection:
		base = styleSection
	default:
		it := ref.item
		switch {
		case it.Status == board.StatusDone:
			base = styleDone
		case it.Urgent:
			base = styleUrgent
		case isReplyItem(it): // conversational sub-bullets read dim
			base = styleDim
		case it.Status == board.StatusBlocked:
			base = styleBlocked
		case it.Status == board.StatusInProgress:
			base = styleInProg
		default:
			base = lipgloss.NewStyle()
		}
	}
	if focus {
		base = base.Foreground(lipgloss.Color("252")).Reverse(true).Bold(true)
	}
	return base
}

func (m *model) viewMap() string {
	if m.board == nil {
		return "no board\n"
	}
	m.ensureMap()
	ms := m.mp
	layout := ms.layout

	footer := m.viewMapFooter()
	footerH := lipgloss.Height(footer)
	headerH := 1
	bodyH := m.height - headerH - footerH
	if bodyH < 3 {
		bodyH = 3
	}
	bodyW := m.width
	if bodyW < 1 {
		bodyW = 1
	}

	gridH := len(layout.Lines)
	gridW := layout.Width
	minRow := 0
	if gridH > 0 {
		minRow = layout.Lines[0].Row
	}

	// Cell grid of runes (NUL holes preserved) + a parallel per-cell style id
	// (-1 = connector). Styling overlays whole node spans (incl. hole cells) so
	// the focus reverse bar stays continuous under wide glyphs.
	grid := make([][]rune, gridH)
	cellStyle := make([][]int, gridH)
	for i, ln := range layout.Lines {
		row := make([]rune, gridW)
		ids := make([]int, gridW)
		for j := range row {
			row[j] = ' '
			ids[j] = -1
		}
		col := 0
		for _, r := range ln.Text {
			if col >= gridW {
				break
			}
			row[col] = r
			col++
		}
		grid[i] = row
		cellStyle[i] = ids
	}
	var styles []lipgloss.Style
	for _, p := range layout.Placements {
		ref := ms.refs[p.Node]
		id := len(styles)
		styles = append(styles, mapNodeStyle(ref, p.Node == ms.focus))
		c0 := p.Col - layout.MinCol
		top := p.Row - (p.Height-1)/2 - minRow
		// Style every row of the node's block across its full width, so an
		// expanded (multi-line) node reads as one styled rectangle and the focus
		// bar stays continuous.
		for li := 0; li < p.Height; li++ {
			r := top + li
			if r < 0 || r >= gridH {
				continue
			}
			for k := 0; k < p.Width && c0+k < gridW; k++ {
				if c0+k >= 0 {
					cellStyle[r][c0+k] = id
				}
			}
		}
		// Tint the leading author token of a reply ("user:"/"claude:") in the
		// list view's author colors, keeping the rest of the node's style.
		if ref.kind == refItem && isReplyItem(ref.item) {
			if loc := authorRe.FindStringSubmatchIndex(ref.item.Text); loc != nil {
				author := ref.item.Text[loc[2]:loc[3]]
				fg := styleAuthorUser.GetForeground()
				if author == "claude" {
					fg = styleAuthorClaude.GetForeground()
				}
				tokID := len(styles)
				styles = append(styles, styles[id].Foreground(fg))
				if top >= 0 && top < gridH {
					// token is ASCII, so byte offsets == display columns
					for k := loc[2]; k < loc[3]+1 && c0+k < gridW; k++ {
						if c0+k >= 0 {
							cellStyle[top][c0+k] = tokID
						}
					}
				}
			}
		}
	}

	// Center the viewport on the focus node; small maps center within the window
	// (negative offset -> padding), larger maps clamp so focus stays on screen.
	fp := ms.placement(ms.focus)
	frow, fcol := 0, 0
	if fp != nil {
		frow = fp.Row - minRow
		fcol = fp.Col - layout.MinCol + fp.Width/2
	}
	offY := offsetFor(frow, bodyH, gridH)
	offX := offsetFor(fcol, bodyW, gridW)

	lines := make([]string, 0, bodyH)
	for dy := 0; dy < bodyH; dy++ {
		gy := offY + dy
		if gy < 0 || gy >= gridH {
			lines = append(lines, "")
			continue
		}
		lines = append(lines, renderMapRow(grid[gy], cellStyle[gy], styles, offX, bodyW))
	}

	// When a frontmatter title is set it stands in for the session id (both in
	// the header and the center node), so tag the header with a dimmed short id
	// for reference. Without a title the session id already serves as the title.
	var idTag string
	if strings.TrimSpace(m.board.Frontmatter.Title) != "" {
		if id := shortSessionID(strings.TrimSpace(m.board.Frontmatter.Session)); id != "" {
			idTag = " " + styleDim.Render(id)
		}
	}
	titleW := m.width - lipgloss.Width(idTag)
	if titleW < 1 {
		titleW = 1
	}
	header := styleTitle.Render(ansi.Truncate(boardTitle(m.board), titleW, "")) + idTag
	return header + "\n" + strings.Join(lines, "\n") + "\n" + footer
}

// offsetFor centers a focus coordinate within a window over a grid dimension:
// negative (pad) when the grid is smaller than the window, otherwise clamped so
// the focus sits mid-window without scrolling past either edge.
func offsetFor(focus, window, grid int) int {
	if grid <= window {
		return -((window - grid) / 2)
	}
	off := focus - window/2
	if off < 0 {
		off = 0
	}
	if off > grid-window {
		off = grid - window
	}
	return off
}

// renderMapRow renders one grid row over the window [offX, offX+w), grouping
// consecutive same-style cells into a single styled segment. NUL hole cells (the
// second half of a wide glyph) contribute nothing except when a wide glyph is
// cut at the window's left edge, where a space stands in.
func renderMapRow(runes []rune, ids []int, styles []lipgloss.Style, offX, w int) string {
	var b strings.Builder
	dx := 0
	for dx < w {
		gx := offX + dx
		if gx < 0 {
			b.WriteByte(' ')
			dx++
			continue
		}
		if gx >= len(runes) {
			break
		}
		id := ids[gx]
		var seg []rune
		start := dx
		for dx < w {
			gx = offX + dx
			if gx < 0 || gx >= len(runes) || ids[gx] != id {
				break
			}
			r := runes[gx]
			if r == 0 {
				if dx == start {
					seg = append(seg, ' ') // wide glyph cut at window start
				}
			} else {
				seg = append(seg, r)
			}
			dx++
		}
		s := string(seg)
		if id < 0 {
			b.WriteString(mapConnStyle.Render(s))
		} else {
			b.WriteString(styles[id].Render(s))
		}
	}
	return b.String()
}

// mapFocusDetail is the focused node's full (untruncated on-map) text, shown in
// the footer so long nodes stay readable even though the map truncates the view.
func mapFocusDetail(ms *mapState) string {
	ref := ms.refs[ms.focus]
	switch ref.kind {
	case refSection:
		return "## " + ref.section
	case refItem:
		return mapNodeText(ref.item)
	default:
		return ms.focus.Text
	}
}

func (m *model) viewMapFooter() string {
	if m.mode == modeMapAdd || m.mode == modeMapEdit {
		label := "add"
		if m.mode == modeMapEdit {
			label = "edit"
		} else {
			parent := ""
			if m.mapInputParent != nil {
				parent = m.mapInputParent.Text
			} else if m.mapInputSection != "" {
				parent = m.mapInputSection
			}
			if parent != "" {
				label = "add under " + strconv.Quote(ansi.Truncate(parent, 40, "…"))
			}
		}
		return styleStatus.Render(label+": ") + m.input.View()
	}
	if m.mode == modeMapFeedback {
		return styleDim.Render(ansi.Truncate(m.lastMoveSummary(), m.width, "…")) + "\n" +
			styleStatus.Render("feedback: ") + m.input.View()
	}
	detail := ""
	if m.mp != nil && m.mp.focus != nil {
		detail = styleDim.Render(ansi.Truncate(mapFocusDetail(m.mp), m.width, "…"))
	}
	// Subtle reminder that the Log section is filtered out and how to bring it
	// back, shown only when the board actually has a (hidden) Log section.
	if !m.mapShowLog && m.board != nil && m.board.Section(board.LogTitle) != nil {
		note := styleDim.Render("Log hidden · M")
		if detail == "" {
			detail = note
		} else {
			detail += "  " + note
		}
	}
	hints := "hjkl move · enter fold · w wrap · a add · A sibling · e edit · space status · d archive · D delete · y copy path · M log · m list · u undo · ! surprised? · ? help · q quit"
	line := styleHelpBar.Render(hints)
	if m.status != "" {
		line = styleStatus.Render(m.status) + "  " + line
	}
	return detail + "\n" + line
}

func absInt(a int) int {
	if a < 0 {
		return -a
	}
	return a
}
