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
	// root is the layout/navigation center: the board title normally, or the
	// focused node when re-rooted (mm's `f`). boardRoot is always the full-tree
	// title root, kept so the breadcrumb can name focus-root ancestors.
	root      *mindmap.Node
	boardRoot *mindmap.Node
	layout    mindmap.Layout
	refs      map[*mindmap.Node]mapRef
	keys      map[*mindmap.Node]string
	nodeByKey map[string]*mindmap.Node
	focus     *mindmap.Node
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
	} else if it.Pinned {
		b.WriteString("!pin ")
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

// replyCountSuffix is the collapsed-children indicator appended to a parent
// node. One compact form for everything (user preference): " [+N]".
func replyCountSuffix(n int) string {
	return fmt.Sprintf(" [+%d]", n)
}

// foldState is a node's place in the unified fold cycle. A node has one state,
// persisted by stable key in model.mapFold (absent == foldDefault).
type foldState int

const (
	// foldDefault is the resting view: non-reply children shown, reply children
	// summarized into a "[+N]" suffix.
	foldDefault foldState = iota
	// foldCollapsed hides ALL children behind one suffix ("[+N]" over every
	// hidden descendant, always "[+N]").
	foldCollapsed
	// foldRepliesExpanded shows everything: non-reply children AND the reply
	// thread, no suffix.
	foldRepliesExpanded
)

// foldSuffix is the "[…]" summary a node in state st shows for the children of
// the given slice. Empty when nothing is hidden (a leaf, or a fully-expanded
// node). "[+N]" counts every hidden descendant (replies included).
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
				continue // folded into the parent's "[+N]" suffix
			}
			st := fold[ck]
			cn := &mindmap.Node{Text: mapNodeText(it), Suffix: foldSuffix(it.Children, st)}
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
		sn := &mindmap.Node{Text: s.Title, Suffix: foldSuffix(s.Items, sState)}
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
	nodeByKey := map[string]*mindmap.Node{}
	for n, k := range keys {
		nodeByKey[k] = n
		if m.mapExpanded[k] {
			expanded[n] = true
		}
	}
	// Re-root on the focused subtree when set (mm's `f`); fall back to the whole
	// board if the focus root no longer maps (e.g. its item was deleted).
	layoutRoot := root
	if m.mapFocusRoot != "" {
		if fr, ok := nodeByKey[m.mapFocusRoot]; ok {
			layoutRoot = fr
		} else {
			m.mapFocusRoot = ""
		}
	}
	opts := mindmap.Options{MaxWidth: mapNodeMaxWidth, Expanded: expanded}
	ms := &mapState{
		root:      layoutRoot,
		boardRoot: root,
		layout:    mindmap.LayoutTreeOpts("", layoutRoot, layoutRoot.Children, mindmap.Rounded, opts),
		refs:      refs,
		keys:      keys,
		nodeByKey: nodeByKey,
	}
	for n, k := range keys {
		if k == m.mapFocusKey {
			ms.focus = n
			break
		}
	}
	// Focus must live inside the current view; snap it to the (re-rooted) center
	// when it points outside the focused subtree or no longer maps.
	if ms.focus == nil || ms.placement(ms.focus) == nil {
		ms.focus = layoutRoot
		m.mapFocusKey = ms.keys[layoutRoot]
	}
	m.mp = ms
}

// enterMap switches to the map view, seeding focus from the list cursor's item
// (or section header) so the two views agree on where you are. Map fold, expand,
// and focus-root state persist across toggles (they live on the model, not the
// transient mapState); a persisted focus root is kept only while it still
// contains the seeded node, otherwise the map opens on the whole board so the
// selected item is always visible. If the seeded node is hidden behind a
// collapsed ancestor (or an un-expanded reply thread), the path is revealed so
// focus can land on it.
func (m *model) enterMap() {
	m.mapView = true
	seedKey := ""
	if it := m.currentItem(); it != nil {
		if k, ok := m.itemMapKey(it); ok {
			seedKey = k
		}
	} else if sec, ok := m.onHeader(); ok {
		for i, s := range m.board.Sections {
			if s == sec {
				seedKey = "s" + strconv.Itoa(i)
				break
			}
		}
	}
	if seedKey != "" {
		m.revealKey(seedKey)
		m.mapFocusKey = seedKey
		// Drop a persisted focus root that would hide the seeded node.
		if m.mapFocusRoot != "" && !keyWithin(seedKey, m.mapFocusRoot) {
			m.mapFocusRoot = ""
		}
	}
	m.mp = nil // rebuild so mp.focus reflects the seeded key
	m.ensureMap()
}

// exitMap leaves the map for the outline, syncing the outline cursor to the map's
// focused node so the selection carries over: an item node moves the cursor onto
// that item, a section node jumps to that section. The map's fold/expand/focus
// state stays on the model for the next toggle back.
func (m *model) exitMap() {
	m.mapView = false
	if m.mp == nil {
		return
	}
	ref := m.mp.refs[m.mp.focus]
	switch ref.kind {
	case refItem:
		m.setCursorToItem(ref.item)
	case refSection:
		for i, s := range m.board.Sections {
			if s.Title == ref.section {
				m.jumpToSection(i)
				break
			}
		}
	}
}

// setCursorToItem points the outline cursor at the navigation stop for it,
// reporting whether one was found. The outline lists every item, so a map item
// always has a stop.
func (m *model) setCursorToItem(it *board.Item) bool {
	for i, p := range m.positions {
		if p.item == it {
			m.cursor = i
			m.selSec = p.sec
			return true
		}
	}
	return false
}

// keyWithin reports whether key names root or a node nested beneath it.
func keyWithin(key, root string) bool {
	return key == root || strings.HasPrefix(key, root+"/")
}

// itemMapKey returns the stable map node key for a board item, computed straight
// from its position in the board tree (index path, replies counted) so it is
// valid even for items currently hidden by a fold. Mirrors buildMapTree's keying.
func (m *model) itemMapKey(target *board.Item) (string, bool) {
	for si, s := range m.board.Sections {
		if k, ok := keyForItemIn(s.Items, "s"+strconv.Itoa(si), target); ok {
			return k, true
		}
	}
	return "", false
}

func keyForItemIn(items []*board.Item, parentKey string, target *board.Item) (string, bool) {
	idx := 0
	for _, it := range items {
		if !it.IsItem() {
			continue
		}
		ck := parentKey + "/" + strconv.Itoa(idx)
		idx++
		if it == target {
			return ck, true
		}
		if k, ok := keyForItemIn(it.Children, ck, target); ok {
			return k, true
		}
	}
	return "", false
}

// itemForKey resolves a stable node key back to its board item (nil for a
// section or the center), walking the same index path itemMapKey encodes.
func (m *model) itemForKey(key string) *board.Item {
	if !strings.HasPrefix(key, "s") {
		return nil
	}
	parts := strings.Split(key, "/")
	si, err := strconv.Atoi(parts[0][1:])
	if err != nil || si < 0 || si >= len(m.board.Sections) {
		return nil
	}
	items := m.board.Sections[si].Items
	var it *board.Item
	for _, p := range parts[1:] {
		idx, err := strconv.Atoi(p)
		if err != nil {
			return nil
		}
		found := (*board.Item)(nil)
		count := 0
		for _, c := range items {
			if !c.IsItem() {
				continue
			}
			if count == idx {
				found = c
				break
			}
			count++
		}
		if found == nil {
			return nil
		}
		it = found
		items = found.Children
	}
	return it
}

// revealKey clears fold state that would hide the node named by key, so a seeded
// focus can render: no ancestor may stay fully collapsed, and any ancestor whose
// path child is a reply must have its reply thread expanded.
func (m *model) revealKey(key string) {
	if key == "" {
		return
	}
	var chain []string
	for k := key; k != ""; k = parentKeyOf(k) {
		chain = append([]string{k}, chain...)
	}
	for _, k := range chain {
		parent := parentKeyOf(k)
		if m.mapFold[parent] == foldCollapsed {
			delete(m.mapFold, parent) // -> foldDefault, children shown again
		}
		if it := m.itemForKey(k); it != nil && isReplyItem(it) {
			if m.mapFold == nil {
				m.mapFold = map[string]foldState{}
			}
			m.mapFold[parent] = foldRepliesExpanded
		}
	}
}

// --- focus (re-rooting, mm's `f`/`b`) ---

// focusIn re-roots the map on the focused node's subtree: that node becomes the
// center, its children fan out, and a breadcrumb trail names the path back. The
// center and childless nodes can't be focused into. Matches mm: a collapsed
// focus root is un-collapsed (it would otherwise claim a "[+N]" while showing
// its children), and focus lands on the new center's first child.
func (m *model) focusIn() {
	ms := m.mp
	ref := ms.refs[ms.focus]
	if ref.kind == refCenter || ms.focus == ms.root {
		m.status = "already the whole map"
		return
	}
	// Use the BOARD children, not the mindmap node's: a collapsed node has no
	// mindmap children (they're hidden behind its suffix) yet is still focusable.
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
		m.status = "no subtree to focus"
		return
	}
	key := m.mapFocusKey
	// A collapsed focus root would hide the very children we want to reveal.
	if m.mapFold[key] == foldCollapsed {
		delete(m.mapFold, key)
	}
	m.mapFocusRoot = key
	m.mp = nil
	m.ensureMap()
	// Land on the first visible child of the new center.
	if len(m.mp.root.Children) > 0 {
		m.setMapFocus(m.mp.root.Children[0])
	} else {
		m.setMapFocus(m.mp.root)
	}
}

// focusOut steps the focus root out one level (mm's `b`): the previous center
// becomes an ordinary node again and focus rests on it, so repeated `b` walks
// back up to the whole board. A no-op (with a hint) at the top level.
func (m *model) focusOut() {
	if m.mapFocusRoot == "" {
		m.status = "already the whole map"
		return
	}
	prev := m.mapFocusRoot
	m.mapFocusRoot = parentKeyOf(prev)
	m.mapFocusKey = prev // cursor rests on the node we just stepped out of
	m.mp = nil
	m.ensureMap()
}

// mapCrumbs is the breadcrumb path from the board center down to (and including)
// the current focus root, e.g. ["board", "Threads", "port mm…"]. Empty when the
// map is not focused. Each crumb is a short label of the ancestor node.
func (m *model) mapCrumbs() []string {
	if m.mapFocusRoot == "" || m.mp == nil {
		return nil
	}
	// Ancestor keys from the focus root up to the board root (""), innermost last.
	var keys []string
	for k := m.mapFocusRoot; ; k = parentKeyOf(k) {
		keys = append([]string{k}, keys...)
		if k == "" {
			break
		}
	}
	var crumbs []string
	for _, k := range keys {
		if n, ok := m.mp.nodeByKey[k]; ok {
			crumbs = append(crumbs, crumbLabel(m.mp, n))
		}
	}
	return crumbs
}

// crumbLabel is a node's short breadcrumb label: the plain item/section text
// (no status marker, no fold suffix), truncated for the trail.
func crumbLabel(ms *mapState, n *mindmap.Node) string {
	ref := ms.refs[n]
	var s string
	switch ref.kind {
	case refSection:
		s = ref.section
	case refItem:
		s = ref.item.Text
	default:
		s = n.Text
	}
	return ansi.Truncate(strings.TrimSpace(s), 18, "…")
}

// mapOpenLink opens a [[wiki-link]] on the focused item's text, reusing the list
// view's chooser (modeLinkPick) and note/file machinery. The chooser and the
// editor round-trip return to the map (mapView stays set). Sections and the
// center have no links.
func (m *model) mapOpenLink() tea.Cmd {
	ref := m.mp.refs[m.mp.focus]
	if ref.kind != refItem {
		m.status = "focus an item to open its links"
		return nil
	}
	links := board.ExtractLinks(ref.item.DisplayText())
	switch len(links) {
	case 0:
		m.status = "no links on this item"
		return nil
	case 1:
		return m.openLinkByName(links[0])
	}
	m.linkOpts = links
	m.linkCur = 0
	m.mode = modeLinkPick
	return nil
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

// rememberChild records, for parentKey on the given side, that descent last came
// from childKey — so a later re-entry of that branch returns to childKey instead
// of the first child (mm's lastVisited). Per parent + side, session-scoped.
func (m *model) rememberChild(parentKey string, side int, childKey string) {
	if m.mapChildMem == nil {
		m.mapChildMem = map[string]map[int]string{}
	}
	rec := m.mapChildMem[parentKey]
	if rec == nil {
		rec = map[int]string{}
		m.mapChildMem[parentKey] = rec
	}
	rec[side] = childKey
}

// rememberedChild returns the remembered child key for parentKey on side, or "".
func (m *model) rememberedChild(parentKey string, side int) string {
	if rec := m.mapChildMem[parentKey]; rec != nil {
		return rec[side]
	}
	return ""
}

// preferredChild picks which of parent's visible children on the given side to
// land on when descending: the remembered one if it is still a visible child on
// that side, otherwise fall back to fallback (the first child). parent is the
// mindmap node whose stable key holds the memory.
func (m *model) preferredChild(parent *mindmap.Node, side int, fallback *mindmap.Node) *mindmap.Node {
	ms := m.mp
	pkey := ms.keys[parent]
	memKey := m.rememberedChild(pkey, side)
	if memKey == "" {
		return fallback
	}
	if n, ok := ms.nodeByKey[memKey]; ok && ms.parentOf(n) == parent && ms.sideOf(n) == side {
		return n
	}
	return fallback
}

// setMapFocus points focus (and the persisted key) at a node.
func (m *model) setMapFocus(n *mindmap.Node) {
	if n == nil {
		return
	}
	m.mp.focus = n
	m.mapFocusKey = m.mp.keys[n]
}

// mapFocusSiblingEnd is g/G in the map: move focus to the first (last=false) or
// last (last=true) of the focused node's same-side siblings — the map analogue
// of the outline's jump-to-first/last stop. The center has no siblings (no-op).
func (m *model) mapFocusSiblingEnd(last bool) {
	ms := m.mp
	if ms.focus == nil || ms.focus == ms.root {
		return
	}
	sibs := ms.sameSideSiblings(ms.focus, ms.sideOf(ms.focus))
	if len(sibs) == 0 {
		return
	}
	if last {
		m.setMapFocus(sibs[len(sibs)-1])
	} else {
		m.setMapFocus(sibs[0])
	}
}

// mapJumpToSection is 1-9 in the map: focus the Nth (0-based) section node —
// the outline's number-jump. A hidden (Log while filtered) or out-of-view
// (re-rooted subtree) section is a no-op.
func (m *model) mapJumpToSection(idx int) {
	if idx < 0 || idx >= len(m.board.Sections) {
		return
	}
	if n, ok := m.mp.nodeByKey["s"+strconv.Itoa(idx)]; ok && m.mp.placement(n) != nil {
		m.setMapFocus(n)
	}
}

// mapCycleSection is tab/shift-tab in the map: cycle focus through the visible
// section nodes in board order, wrapping — the outline's section cycling.
// Starts from the focus's own section (or the first/last when on the center).
func (m *model) mapCycleSection(delta int) {
	ms := m.mp
	var secs []*mindmap.Node
	for i := range m.board.Sections {
		if n, ok := ms.nodeByKey["s"+strconv.Itoa(i)]; ok && ms.placement(n) != nil {
			secs = append(secs, n)
		}
	}
	if len(secs) == 0 {
		return
	}
	// Index of the focus's owning section within the visible section ring.
	cur := -1
	if ms.focus != nil {
		key := ms.keys[ms.focus]
		for i, n := range secs {
			if keyWithin(key, ms.keys[n]) {
				cur = i
				break
			}
		}
	}
	var next int
	if cur < 0 {
		if delta > 0 {
			next = 0
		} else {
			next = len(secs) - 1
		}
	} else {
		next = ((cur+delta)%len(secs) + len(secs)) % len(secs)
	}
	m.setMapFocus(secs[next])
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
		// From the center, enter that side's remembered branch (mm), falling back
		// to the first child on the chosen side.
		var first *mindmap.Node
		for _, c := range ms.root.Children {
			if ms.sideOf(c) == dir {
				first = c
				break
			}
		}
		if first == nil {
			return
		}
		m.setMapFocus(m.preferredChild(ms.root, dir, first))
		return
	}
	side := ms.sideOf(ms.focus)
	if side == dir {
		// Outward, deeper into this node's own branch: the remembered child (mm),
		// else its first visible child.
		if len(ms.focus.Children) > 0 {
			m.setMapFocus(m.preferredChild(ms.focus, dir, ms.focus.Children[0]))
			return
		}
		// No visible children: the node may still have hidden ones (a collapsed
		// node, or a default node whose only children are its reply thread).
		// Advance its fold one step toward visible and step onto the first newly
		// revealed child, rather than dead-ending.
		m.expandInto()
		return
	}
	// Inward, toward the center: the parent remembers which child (and side) we
	// left, so re-descending returns here (mm's lastVisited).
	parent := ms.parentOf(ms.focus)
	if parent != nil {
		m.rememberChild(ms.keys[parent], side, ms.keys[ms.focus])
	}
	m.setMapFocus(parent)
}

// expandInto handles a lateral away-from-center move onto a node with no visible
// children: it advances the node's fold state one step toward visible
// (collapsed -> default; default -> replies-expanded only when the node has no
// visible non-reply children, so mixed nodes are never over-expanded) and lands
// focus on the first now-visible child. A fully-expanded or truly childless node
// is a dead end (status message). The board mutation-free fold change re-layouts
// the map like enter does.
func (m *model) expandInto() {
	ms := m.mp
	ref := ms.refs[ms.focus]
	var items []*board.Item
	switch ref.kind {
	case refItem:
		items = ref.item.Children
	case refSection:
		if sec := m.board.Section(ref.section); sec != nil {
			items = sec.Items
		}
	default:
		return // the center has no branch to enter
	}
	if countDescendantsIn(items) == 0 {
		m.status = "nothing to expand"
		return
	}
	key := m.mapFocusKey
	cur := m.mapFold[key]
	var next foldState
	switch cur {
	case foldCollapsed:
		next = foldDefault
		// A replies-only node is still empty at default — go straight to
		// replies-expanded so the descent is a single press: the same keystroke
		// expands the fold AND lands focus on the revealed child.
		if !hasNonReplyChild(items) {
			next = foldRepliesExpanded
		}
	case foldDefault:
		// Only reveal the reply thread when there are no visible non-reply
		// children — a mixed node already shows its non-reply children, so
		// stepping into it would have landed on one above; don't over-expand it.
		if hasNonReplyChild(items) || countRepliesIn(items) == 0 {
			m.status = "already expanded"
			return
		}
		next = foldRepliesExpanded
	default: // foldRepliesExpanded: nothing left to reveal
		m.status = "already expanded"
		return
	}
	if m.mapFold == nil {
		m.mapFold = map[string]foldState{}
	}
	if next == foldDefault {
		delete(m.mapFold, key)
	} else {
		m.mapFold[key] = next
	}
	m.mp = nil
	m.ensureMap()
	// Land on the child the new fold state revealed: the remembered one (mm's
	// child memory composes with auto-expand), else the first.
	if n, ok := m.mp.nodeByKey[key]; ok && len(n.Children) > 0 {
		m.setMapFocus(m.preferredChild(n, m.mp.sideOf(n), n.Children[0]))
	} else {
		// Defensive: the step revealed no child node (shouldn't happen now that
		// a collapsed replies-only node expands straight to replies-expanded);
		// keep focus on this node for the next press.
		if n, ok := m.mp.nodeByKey[key]; ok {
			m.setMapFocus(n)
		}
	}
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
	if m.clearSearchOnEsc(key) {
		return m, nil
	}
	if m.searchActive && key == "tab" {
		m.toggleSearchScope()
		return m, nil
	}
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
	case "esc":
		// esc steps focus out one level before it quits: only an unfocused map
		// (or q) leaves the view.
		if m.mapFocusRoot != "" {
			m.focusOut()
			return m, nil
		}
		if m.cameFromDash {
			return m, m.enterDash()
		}
		return m, tea.Quit
	case "q":
		if m.cameFromDash {
			return m, m.enterDash()
		}
		return m, tea.Quit
	case "ctrl+c":
		return m, tea.Quit
	case "f":
		m.focusIn()
	case "b":
		// b toggles blocked [?] (outline muscle memory; web convention 9729cad).
		// Stepping out of a focused subtree is esc only.
		m.mapBlocked()
	case "o":
		return m, m.mapOpenLink()
	case "O":
		if ref := m.mp.refs[m.mp.focus]; ref.kind == refItem {
			m.yankLinkPath(ref.item)
		} else {
			m.status = "focus an item to copy its link path"
		}
	case "m":
		m.exitMap()
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
	case "g":
		m.mapFocusSiblingEnd(false)
	case "G":
		m.mapFocusSiblingEnd(true)
	case "tab":
		m.mapCycleSection(1)
	case "shift+tab":
		m.mapCycleSection(-1)
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		m.mapJumpToSection(int(key[0] - '1'))
	case "enter":
		m.mapToggleFold()
	case "z":
		m.mapFocusFoldToggle()
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
	case "p":
		m.mapPin()
	case "D":
		m.mapDelete()
	case "B":
		// Back to the board picker, same as the outline's B. exitMap first so a
		// board picked next opens in the outline (enterPicker clears the
		// per-board map state anyway; this keeps the flag honest too).
		m.exitMap()
		return m, m.enterPicker()
	case "L":
		// Quick log entry (outline parity); confirming returns to the map.
		m.startInput(modeInputLog, "", "log entry")
	case "T":
		// Edit the board title — same as e on the center node, kept for outline
		// muscle memory.
		m.startTitleInput()
	case "E":
		// Open the whole board in $EDITOR (outline parity); the 3-way merge on
		// return reloads and the map re-layouts.
		m.snapshot()
		return m, m.openEditor()
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
		// Toggle urgent on the focused item — same meaning as the list view and the
		// web map, so `!` is consistent everywhere.
		m.mapUrgent()
	case "S":
		// Surprise recorder (TUI-dev-only): a nav move surprised you, note where you
		// expected focus to land. Moved off `!` to restore cross-frontend key parity.
		m.startInput(modeMapFeedback, "", "where did you expect it?")
	case "/":
		m.startSearch()
	case "n":
		m.searchNext(1)
	case "N":
		m.searchNext(-1)
	case "H":
		// Shared-journal history overlay — same as the outline's H (parity).
		m.openHistory()
	case "V":
		// Recent-changes overlay — same as the outline's V (parity). Enter jumps
		// to the changed node in the map (jumpToMatch is map-aware).
		m.openRecents()
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

// mapFocusFoldToggle is the map view's `z` key: org-mode-style zoom onto the
// focused node, mirroring the outline's z (and the web map's mapFocusFold).
// Every subtree collapses except the ancestor path from the layout root down to
// the focus; ancestors (the focus included) open fully (replies shown) so the
// path is visible, while the focus's direct children — ordinary non-ancestors —
// stay visible but each collapsed: exactly one level open. A second z restores
// the exact fold snapshot taken before the zoom. Folds outside a re-rooted
// (`f`) subtree are dropped to default, like the web's rebuild-from-the-root.
func (m *model) mapFocusFoldToggle() {
	if m.mapFoldPrev != nil { // toggle back: restore the pre-zoom fold state
		m.mapFold = m.mapFoldPrev
		m.mapFoldPrev = nil
		m.mp = nil
		m.ensureMap() // snaps focus to the layout root if the restored folds hide it
		return
	}
	m.ensureMap()
	focus := m.mapFocusKey
	root := m.mapFocusRoot // "" = the whole board (center)
	snap := make(map[string]foldState, len(m.mapFold))
	for k, v := range m.mapFold {
		snap[k] = v
	}
	m.mapFoldPrev = snap
	// Ancestor path: the focus up to (and including) the layout root.
	anc := map[string]bool{}
	for k := focus; ; k = parentKeyOf(k) {
		anc[k] = true
		if k == root || k == "" {
			break
		}
	}
	// Rebuild the folds from scratch over the FULL board tree within the layout
	// root: ancestors open fully, every other node with children collapses.
	folds := map[string]foldState{}
	var walk func(items []*board.Item, key string)
	walk = func(items []*board.Item, key string) {
		if countDescendantsIn(items) > 0 && (root == "" || keyWithin(key, root)) {
			if anc[key] {
				folds[key] = foldRepliesExpanded
			} else {
				folds[key] = foldCollapsed
			}
		}
		idx := 0
		for _, it := range items {
			if !it.IsItem() {
				continue
			}
			walk(it.Children, key+"/"+strconv.Itoa(idx))
			idx++
		}
	}
	for i, s := range m.board.Sections {
		walk(s.Items, "s"+strconv.Itoa(i))
	}
	m.mapFold = folds
	m.mp = nil
	m.ensureMap() // focus is on the ancestor path, so it stays visible
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

// mapPin toggles the "!pin" marker on the focused item and saves. Sections and
// the center are not pinnable (beep via a status message). Mirrors the list
// view's p; pinned items are re-injected into Claude's context on a cadence.
func (m *model) mapPin() {
	ref := m.mp.refs[m.mp.focus]
	if ref.kind != refItem {
		m.status = "sections can't be pinned"
		return
	}
	it := ref.item
	m.snapshot()
	op := pendingOp{typ: opPin, section: m.board.SectionTitleOf(it), rawLine: it.Raw()}
	it.TogglePinned()
	op.newPinned = it.Pinned
	m.saveWithRebase(op)
	if it.Pinned {
		m.status = "pinned"
	} else {
		m.status = "unpinned"
	}
	m.mp = nil // pin marker alters node text/width -> re-layout
}

// mapUrgent toggles the "!!" urgent marker on the focused item and saves.
// Sections and the center are not urgent-able (beep via a status message).
// Mirrors the list view's `!` and the web map's urgent toggle.
// mapBlocked toggles blocked [?] on the focused item directly — outline muscle
// memory (web convention 9729cad: map b = blocked; stepping out is esc only).
func (m *model) mapBlocked() {
	ref := m.mp.refs[m.mp.focus]
	if ref.kind != refItem || ref.item.Status == board.StatusNone {
		m.status = "nothing to block here"
		return
	}
	it := ref.item
	m.snapshot()
	op := pendingOp{typ: opCycle, section: m.board.SectionTitleOf(it), rawLine: it.Raw()}
	if it.Status == board.StatusBlocked {
		it.Status = board.StatusOpen
	} else {
		it.Status = board.StatusBlocked
	}
	op.newStatus = it.Status
	m.saveWithRebase(op)
	m.mp = nil // marker changed -> re-render
}

func (m *model) mapUrgent() {
	ref := m.mp.refs[m.mp.focus]
	if ref.kind != refItem {
		m.status = "sections can't be urgent"
		return
	}
	it := ref.item
	m.snapshot()
	op := pendingOp{typ: opUrgent, section: m.board.SectionTitleOf(it), rawLine: it.Raw()}
	it.ToggleUrgent()
	op.newUrgent = it.Urgent
	m.saveWithRebase(op)
	if it.Urgent {
		m.status = "urgent"
	} else {
		m.status = "not urgent"
	}
	m.mp = nil // urgent marker alters node text/width -> re-layout
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
		// Hard-delete a whole section, matching the list view's D and the web map.
		sec := m.board.Section(ref.section)
		if sec == nil {
			m.status = "section already gone"
			return
		}
		title := sec.Title
		m.snapshot()
		m.board.RemoveSection(sec)
		m.mapFocusKey = "" // focus falls back to the center
		m.mapFocusRoot = ""
		m.saveWithRebase(pendingOp{typ: opDeleteSection, section: title})
		m.status = "deleted section " + title
		m.mp = nil
		return
	}
	it := ref.item
	m.snapshot()
	op := pendingOp{typ: opDeleteItem, section: m.board.SectionTitleOf(it), rawLine: it.Raw()}
	// Focus lands on the next sibling (which takes the deleted node's place),
	// else the previous sibling, else the parent.
	m.mapFocusKey = m.mapRemovalFocusKey()
	m.board.Remove(it)
	m.saveWithRebase(op)
	m.mp = nil
}

// mapRemovalFocusKey picks where map focus should land after the focused item
// (+ its subtree) is archived/deleted: the next visible sibling (whose key
// index drops by one once the removed IsItem sibling vanishes), else the
// previous visible sibling, else the parent node. Must run BEFORE the board
// mutation, while m.mp still reflects the pre-removal tree.
func (m *model) mapRemovalFocusKey() string {
	parentKey := parentKeyOf(m.mapFocusKey)
	parent := m.mp.parentOf(m.mp.focus)
	if parent == nil {
		return parentKey
	}
	kids := parent.Children // visible siblings, document order
	pos := -1
	for i, c := range kids {
		if c == m.mp.focus {
			pos = i
			break
		}
	}
	if pos < 0 {
		return parentKey
	}
	if pos+1 < len(kids) {
		// The next sibling shifts one index down: exactly one IsItem sibling
		// (the removed one) precedes it less after the removal.
		k := m.mp.keys[kids[pos+1]]
		if i := strings.LastIndexByte(k, '/'); i >= 0 {
			if idx, err := strconv.Atoi(k[i+1:]); err == nil && idx > 0 {
				return k[:i+1] + strconv.Itoa(idx-1)
			}
		}
		return parentKey
	}
	if pos > 0 {
		return m.mp.keys[kids[pos-1]] // last child: fall back to the previous
	}
	return parentKey // only child: rest on the parent
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
		// a on the center adds a section: reuse the list view's add-sections
		// overlay. mapView stays set, so confirming returns to the map.
		m.openAddSections()
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
// view's d. On a section node it archives the whole section (list view's d on a
// header), keeping the "cannot archive Archive/Log" guards. The center is never
// archivable.
func (m *model) mapArchive() {
	ref := m.mp.refs[m.mp.focus]
	if ref.kind == refCenter {
		m.status = "can't archive the title"
		return
	}
	if ref.kind == refSection {
		m.mapArchiveSection(ref.section)
		return
	}
	it := ref.item
	m.snapshot()
	op := pendingOp{typ: opArchiveItem, section: m.board.SectionTitleOf(it), rawLine: it.Raw()}
	// Compute the post-archive focus BEFORE the mutation (it reads the still-
	// current m.mp): next sibling, else previous sibling, else the parent.
	next := m.mapRemovalFocusKey()
	if m.board.ArchiveItem(it) {
		m.mapFocusKey = next
		m.rebuildPositions()
		m.saveWithRebase(op)
		m.status = "archived"
	}
}

// mapArchiveSection archives the section named title (its items move to ##
// Archive, the heading is removed), mirroring the list view's d on a header.
// The Archive and Log sections refuse to be archived. Focus falls back to the
// center after the section vanishes; the map re-layouts.
func (m *model) mapArchiveSection(title string) {
	sec := m.board.Section(title)
	if sec == nil {
		m.status = "section already gone"
		return
	}
	m.snapshot()
	if !m.board.ArchiveSection(sec) {
		m.status = "can't archive this section"
		return
	}
	m.migrateSectionState(title, "") // section gone: drop its per-title state
	m.mapFocusKey = ""               // node vanished -> snap to center on rebuild
	m.rebuildPositions()
	m.saveWithRebase(pendingOp{typ: opArchiveSection, section: title})
	m.mp = nil
	m.status = "section archived"
}

// migrateSectionState moves (or drops, when newTitle is "") the list view's
// per-section collapse override from oldTitle to newTitle. The map's own fold
// and expand state is keyed by section INDEX ("sN"), not title, so a rename
// leaves those keys pointing at the same (renamed) section with no migration —
// only the title-keyed collapse map needs to follow. Migrating rather than
// resetting keeps state attached to the right section and, crucially, prevents
// stale state from leaking onto a later section that happens to reuse oldTitle.
func (m *model) migrateSectionState(oldTitle, newTitle string) {
	if m.collapsed == nil {
		return
	}
	v, ok := m.collapsed[oldTitle]
	if !ok {
		return
	}
	delete(m.collapsed, oldTitle)
	if newTitle != "" {
		m.setCollapsed(newTitle, v)
	}
}

// applySectionRename renames the section captured in m.mapInputSection to newTitle.
// A no-op when the name is unchanged; refused (with a status error) when another
// section already carries newTitle — merging sections is out of scope. On
// success it migrates the list view's per-title collapse state and persists via
// the rebase path (rebase matches by the OLD title, see applyOp's
// opRenameSection). The caller already snapshotted for undo.
func (m *model) applySectionRename(newTitle string) {
	oldTitle := m.mapInputSection
	if newTitle == oldTitle {
		return // nothing changed
	}
	sec := m.board.Section(oldTitle)
	if sec == nil {
		m.status = "section already gone"
		return
	}
	if m.board.Section(newTitle) != nil {
		m.status = "a section named " + strconv.Quote(newTitle) + " already exists"
		return
	}
	if !m.board.RenameSection(sec, newTitle) {
		m.status = "rename refused"
		return
	}
	m.migrateSectionState(oldTitle, newTitle)
	m.rebuildPositions()
	m.saveWithRebase(pendingOp{typ: opRenameSection, section: oldTitle, payload: newTitle})
	m.status = "section renamed"
}

func (m *model) startMapEdit() {
	ref := m.mp.refs[m.mp.focus]
	if ref.kind == refCenter {
		// The center node stands for the board title; e edits the frontmatter title.
		m.startTitleInput()
		return
	}
	if ref.kind == refSection {
		// e on a section renames its heading: pre-fill with the current title.
		m.mapInputSection = ref.section
		m.startInput(modeMapRename, ref.section, "rename section")
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
				// A child added under an item is the human speaking in that
				// thread: stamp the "user:" author tag, matching the outline
				// reply and the web map. The op payload carries the stamped
				// text so a rebase replay keeps the attribution.
				text = "user: " + text
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
		case modeMapRename:
			m.applySectionRename(text)
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
// applied after so it is uniform across every status. fresh marks a node whose
// item changed out-of-band (the map counterpart of the outline's ◆ accent).
func mapNodeStyle(ref mapRef, focus, search, fresh bool) lipgloss.Style {
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
		case it.Pinned:
			base = stylePin
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
	// A node matching the active search is tinted (styleSearch), unless it is the
	// focused node — the reverse-video focus bar wins there, as in the outline.
	// The map layer groups cells by whole-node style id, so search highlighting is
	// a whole-node tint here rather than a substring span (the outline does the
	// substring accent).
	if search && !focus {
		base = base.Foreground(styleSearch.GetForeground())
	}
	// Fresh (out-of-band changed) nodes get the outline's ◆ accent color; the
	// focus bar wins on the focused node (landing there settles the mark).
	if fresh && !focus {
		base = base.Foreground(styleFresh.GetForeground()).Bold(true)
	}
	if focus {
		base = base.Foreground(selFG).Reverse(true).Bold(true)
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
	crumbLine := m.viewMapCrumbs()
	headerH := 1
	if crumbLine != "" {
		headerH = 2
	}
	panel := m.viewSearchPanel()
	panelH := 0
	if panel != "" {
		panelH = lipgloss.Height(panel)
	}
	bodyH := m.height - headerH - footerH - panelH
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
	query := m.searchHighlight()
	var styles []lipgloss.Style
	for _, p := range layout.Placements {
		ref := ms.refs[p.Node]
		id := len(styles)
		matched := ref.kind == refItem && ref.item != nil && itemMatchesSearch(ref.item, query)
		focused := p.Node == ms.focus
		fresh := false
		if ref.kind == refItem && ref.item != nil {
			fkey := strings.TrimSpace(ref.item.Raw())
			fresh = m.fresh[fkey]
			if fresh && focused {
				delete(m.fresh, fkey) // landing on a change settles it (outline parity)
				fresh = false
			}
		}
		styles = append(styles, mapNodeStyle(ref, focused, matched, fresh))
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
	if crumbLine != "" {
		header += "\n" + crumbLine
	}
	if panel != "" {
		return header + "\n" + strings.Join(lines, "\n") + "\n" + panel + "\n" + footer
	}
	return header + "\n" + strings.Join(lines, "\n") + "\n" + footer
}

// viewMapCrumbs renders the focus breadcrumb trail (empty when unfocused), e.g.
// a dimmed "board › Threads › port mm…".
func (m *model) viewMapCrumbs() string {
	crumbs := m.mapCrumbs()
	if len(crumbs) == 0 {
		return ""
	}
	return styleDim.Render(ansi.Truncate(strings.Join(crumbs, " › "), m.width, "…"))
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
	if m.mode == modeInputTitle {
		return styleStatus.Render("title: ") + m.input.View()
	}
	if m.mode == modeMapRename {
		return styleStatus.Render("rename section: ") + m.input.View()
	}
	if m.mode == modeInputLog {
		return styleStatus.Render("log: ") + m.input.View()
	}
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
	if m.mode == modeSearch {
		// Live tally while typing, mirroring the outline's search footer.
		tail := ""
		if m.status != "" {
			tail = "  " + styleDim.Render(m.status)
		}
		return styleStatus.Render("search: ") + m.input.View() + tail
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
	hints := "hjkl move · g/G ends · tab section · 1-9 jump · / search · n/N next/prev · f focus · esc out · enter fold · z zoom · w wrap · o open link · a add (section on center) · A sibling · e edit/rename (title on center) · space status · b blocked · ! urgent · p pin · d archive (item/section) · D delete (item/section) · y copy path · E editor · T title · L log entry · H history · V recents · M log ring · B boards · m outline · u undo · S surprised? · ? help · q quit"
	line := styleHelpBar.Render(hints)
	if m.status != "" {
		line = styleStatus.Render(m.status) + "  " + line
	}
	// Live session-status segment (model · context bar · activity), same as the
	// outline footer (statusFooter reads the sidecar on every render).
	if seg := m.statusFooter(); seg != "" {
		line = seg + "\n" + line
	}
	return detail + "\n" + line
}

func absInt(a int) int {
	if a < 0 {
		return -a
	}
	return a
}
