package tui

import (
	"path/filepath"
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
// survive via m.mapFocusKey / m.mapFolded, both keyed by the stable node key.
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
func buildMapTree(b *board.Board, folded map[string]bool) (*mindmap.Node, map[*mindmap.Node]mapRef, map[*mindmap.Node]string) {
	refs := map[*mindmap.Node]mapRef{}
	keys := map[*mindmap.Node]string{}

	root := &mindmap.Node{Text: boardTitle(b), Heading: true}
	refs[root] = mapRef{kind: refCenter}
	keys[root] = ""

	for i, s := range b.Sections {
		skey := "s" + strconv.Itoa(i)
		sn := &mindmap.Node{Text: s.Title, Folded: folded[skey]}
		refs[sn] = mapRef{kind: refSection, section: s.Title}
		keys[sn] = skey
		root.Children = append(root.Children, sn)

		var walk func(items []*board.Item, parent *mindmap.Node, parentKey, sec string)
		walk = func(items []*board.Item, parent *mindmap.Node, parentKey, sec string) {
			idx := 0
			for _, it := range items {
				if !it.IsItem() {
					continue // continuation / stray lines are not map nodes
				}
				ck := parentKey + "/" + strconv.Itoa(idx)
				idx++
				cn := &mindmap.Node{Text: mapNodeText(it), Folded: folded[ck]}
				refs[cn] = mapRef{kind: refItem, section: sec, item: it}
				keys[cn] = ck
				parent.Children = append(parent.Children, cn)
				walk(it.Children, cn, ck, sec)
			}
		}
		walk(s.Items, sn, skey, s.Title)
	}
	return root, refs, keys
}

// ensureMap builds the map layout if it is stale (nil), re-resolving focus from
// the persisted mapFocusKey (falling back to the center when it no longer maps).
func (m *model) ensureMap() {
	if m.mp != nil {
		return
	}
	root, refs, keys := buildMapTree(m.board, m.mapFolded)
	ms := &mapState{
		root:   root,
		layout: mindmap.LayoutTree("", root, root.Children, mindmap.Rounded),
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

// mapMove moves focus to the spatially nearest node in the given direction
// ('h'/'j'/'k'/'l'), mm-style: candidates strictly on that side of the focused
// node's center, scored so on-axis distance dominates the cross-axis.
func (m *model) mapMove(dir rune) {
	ms := m.mp
	fp := ms.placement(ms.focus)
	if fp == nil {
		return
	}
	fr := fp.Row
	fc := fp.Col + fp.Width/2
	var best *mindmap.Node
	bestScore := 1 << 30
	for _, p := range ms.layout.Placements {
		if p.Node == ms.focus {
			continue
		}
		dr := p.Row - fr
		dc := (p.Col + p.Width/2) - fc
		switch dir {
		case 'l':
			if dc <= 0 {
				continue
			}
		case 'h':
			if dc >= 0 {
				continue
			}
		case 'j':
			if dr <= 0 {
				continue
			}
		case 'k':
			if dr >= 0 {
				continue
			}
		}
		var score int
		if dir == 'h' || dir == 'l' {
			score = absInt(dc) + 3*absInt(dr)
		} else {
			score = absInt(dr) + 3*absInt(dc)
		}
		if score < bestScore {
			bestScore = score
			best = p.Node
		}
	}
	if best != nil {
		ms.focus = best
		m.mapFocusKey = ms.keys[best]
	}
}

// --- key handling ---

func (m *model) handleMapKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.status = ""
	m.ensureMap()
	switch msg.String() {
	case "q", "esc":
		if m.cameFromDash {
			return m, m.enterDash()
		}
		return m, tea.Quit
	case "ctrl+c":
		return m, tea.Quit
	case "m":
		m.mapView = false
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
	case "a":
		m.startMapAdd()
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
	case "r":
		m.reloadExternal()
		m.status = "reloaded"
	case "?":
		m.prevMode = m.mode
		m.mode = modeHelp
	}
	return m, nil
}

// mapToggleFold collapses/expands the focused subtree (section or item). The
// center never folds; a childless node has nothing to fold.
func (m *model) mapToggleFold() {
	ms := m.mp
	ref := ms.refs[ms.focus]
	if ref.kind == refCenter {
		m.status = "can't fold the center"
		return
	}
	if len(ms.focus.Children) == 0 {
		m.status = "nothing to fold"
		return
	}
	if m.mapFolded == nil {
		m.mapFolded = map[string]bool{}
	}
	if m.mapFolded[m.mapFocusKey] {
		delete(m.mapFolded, m.mapFocusKey)
	} else {
		m.mapFolded[m.mapFocusKey] = true
	}
	m.mp = nil // re-layout with the new fold state
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
		r := p.Row - minRow
		if r < 0 || r >= gridH {
			continue
		}
		c0 := p.Col - layout.MinCol
		for k := 0; k < p.Width && c0+k < gridW; k++ {
			if c0+k >= 0 {
				cellStyle[r][c0+k] = id
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

	header := styleTitle.Render(ansi.Truncate(boardTitle(m.board), m.width, ""))
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
		}
		return styleStatus.Render(label+": ") + m.input.View()
	}
	detail := ""
	if m.mp != nil && m.mp.focus != nil {
		detail = styleDim.Render(ansi.Truncate(mapFocusDetail(m.mp), m.width, "…"))
	}
	hints := "hjkl move · enter fold · a add · e edit · space status · D delete · m list · u undo · ? help · q quit"
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
