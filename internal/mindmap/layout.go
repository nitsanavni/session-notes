package mindmap

import (
	"fmt"
	"sort"
	"strings"
)

// Options tune the layout beyond mm's plain center-outward tree. The zero value
// reproduces mm exactly (the golden fixtures rely on this): no truncation, no
// expansion. The TUI opts in to truncation; the mindmap package never does by
// default.
type Options struct {
	// MaxWidth caps a node's display width; longer text is truncated with a "…"
	// (or, when Expanded, wrapped into a multi-line block). 0 = no limit.
	MaxWidth int
	// Expanded holds the nodes rendered as wrapped multi-line blocks instead of
	// a single truncated line (the `w` toggle). Ignored when MaxWidth is 0.
	Expanded map[*Node]bool
}

// Placement is one node's box on the map. A node may span multiple rows (an
// expanded, wrapped node): Row is its vertical CENTER (where connectors attach),
// Height is the number of text rows, and the block's top row is
// Row-(Height-1)/2. Lines/RawLines hold the per-row text (len == Height).
type Placement struct {
	Node     *Node
	Text     string   // first visible line (markers stripped, checkbox glyph added)
	Raw      string   // first line with markdown markers kept, for styled rendering
	Lines    []string // all visible rows (len == Height; >1 only when expanded)
	RawLines []string // all rows with markers kept
	Row      int      // vertical center row (connector attach)
	Col      int      // left edge of visible text (layout coords, pre-normalization)
	Width    int      // visible width (widest line)
	Height   int      // number of text rows
	Side     int      // 1 right / -1 left / 0 center
	Virtual  bool     // stand-in center for an untitled (file-label) map
}

// Line is one rendered layout row: its layout-space row index and the plain
// text. One string rune per cell; the cell after a double-width glyph holds a
// NUL ('\x00') hole (strip before printing).
type Line struct {
	Row  int
	Text string
}

// Layout is the placed, drawable map.
type Layout struct {
	Placements []*Placement
	Lines      []Line
	MinCol     int
	Width      int
	Height     int
}

// DisplayText is a node's on-map text: markers stripped, checkbox glyph
// prepended, and a fold-count suffix when a folded node hides children.
func DisplayText(n *Node) string {
	return displayText(n, false)
}

func displayText(n *Node, keepMarkers bool) string {
	t := n.Text
	if !keepMarkers {
		t = stripMarkers(t)
	}
	if n.Checkbox != NoCheckbox {
		if n.Checkbox == Checked {
			t = "☑ " + t
		} else {
			t = "☐ " + t
		}
	}
	if n.Folded && len(n.Children) > 0 {
		t += fmt.Sprintf(" [+%d]", countNodes(n.Children))
	}
	return t
}

func countNodes(nodes []*Node) int {
	total := 0
	for _, n := range nodes {
		total += 1 + countNodes(n.Children)
	}
	return total
}

// nodeDisplayLines is a node's on-map text as one or more rows: a single
// (possibly truncated) line normally, or the wrapped multi-line block when the
// node is expanded. Returns visible rows and marker-kept rows in lockstep.
func nodeDisplayLines(n *Node, opts Options) (vis, raw []string) {
	full := displayText(n, false)
	rawFull := displayText(n, true)
	if opts.MaxWidth <= 0 {
		return []string{full + n.Suffix}, []string{rawFull + n.Suffix}
	}
	if opts.Expanded[n] {
		// Wrap the base text, then re-attach the suffix to the last line so a
		// wrapped node keeps its fold count too.
		return appendSuffixLines(wrapDisplay(full, opts.MaxWidth), n.Suffix),
			appendSuffixLines(wrapDisplay(rawFull, opts.MaxWidth), n.Suffix)
	}
	// Truncate the BASE first against a budget reduced by the suffix width, then
	// append the suffix, so the suffix survives however long the base is.
	budget := opts.MaxWidth
	if sw := dispWidth(n.Suffix); sw > 0 && sw < budget {
		budget -= sw
	}
	return []string{truncateDisplay(full, budget) + n.Suffix},
		[]string{truncateDisplay(rawFull, budget) + n.Suffix}
}

// appendSuffixLines appends suf to the last line of lines (empty suf is a
// no-op). Used to re-attach a node's never-truncated Suffix after wrapping.
func appendSuffixLines(lines []string, suf string) []string {
	if suf == "" || len(lines) == 0 {
		return lines
	}
	lines[len(lines)-1] += suf
	return lines
}

// nodeHeight is how many text rows a node occupies (1, or the wrapped line count
// when expanded).
func nodeHeight(n *Node, opts Options) int {
	vis, _ := nodeDisplayLines(n, opts)
	return len(vis)
}

func maxLineWidth(lines []string) int {
	w := 0
	for _, l := range lines {
		if d := dispWidth(l); d > w {
			w = d
		}
	}
	return maxInt(1, w)
}

// height is a node's total vertical extent in rows: the sum of its children's
// extents (folds collapse to the node's own height), but never less than the
// node's own multi-row block.
func height(n *Node, opts Options) int {
	nh := nodeHeight(n, opts)
	if n.Folded || len(n.Children) == 0 {
		return nh
	}
	total := 0
	for _, c := range n.Children {
		total += height(c, opts)
	}
	if total < nh {
		total = nh
	}
	return total
}

// weight ignores folds: folding a branch must not flip it across the map.
func weight(n *Node) int {
	if len(n.Children) == 0 {
		return 1
	}
	total := 0
	for _, c := range n.Children {
		total += weight(c)
	}
	if total < 1 {
		return 1
	}
	return total
}

type canvas struct {
	cells      map[[2]int]int
	placements []*Placement
	minRow     int
	maxRow     int
	minCol     int
	maxCol     int
}

func newCanvas() *canvas {
	return &canvas{cells: map[[2]int]int{}}
}

func (cv *canvas) touch(row, col int) {
	if row < cv.minRow {
		cv.minRow = row
	}
	if row > cv.maxRow {
		cv.maxRow = row
	}
	if col < cv.minCol {
		cv.minCol = col
	}
	if col > cv.maxCol {
		cv.maxCol = col
	}
}

func (cv *canvas) arm(row, col, arms int) {
	cv.cells[[2]int{row, col}] |= arms
	cv.touch(row, col)
}

// place records a node's box. vis/raw are its per-row text (len == height); row
// is the vertical center (connector attach), so the block's top is
// row-(h-1)/2.
func (cv *canvas) place(n *Node, vis, raw []string, row, col, side int) *Placement {
	w := maxLineWidth(vis)
	h := len(vis)
	p := &Placement{
		Node: n, Text: vis[0], Raw: raw[0], Lines: vis, RawLines: raw,
		Row: row, Col: col, Width: w, Height: h, Side: side,
	}
	cv.placements = append(cv.placements, p)
	top := row - (h-1)/2
	for i := 0; i < h; i++ {
		cv.touch(top+i, col)
		cv.touch(top+i, col+w-1)
	}
	return p
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// placeNode places node n with its subtree rows in [row0, row0+height), growing
// right (dir=1, anchor = left text edge) or left (dir=-1, anchor = exclusive
// right text edge). Returns the node's own row.
func placeNode(cv *canvas, n *Node, row0, anchor, dir int, opts Options) int {
	vis, raw := nodeDisplayLines(n, opts)
	w := maxLineWidth(vis)
	nh := len(vis)
	var kids []*Node
	if !n.Folded {
		kids = n.Children
	}

	var textStart int
	if dir == 1 {
		textStart = anchor
	} else {
		textStart = anchor - w
	}
	textEnd := textStart + w // exclusive
	var jx, childAnchor int
	if dir == 1 {
		jx = textEnd + 2
		childAnchor = jx + 3
	} else {
		jx = textStart - 3
		childAnchor = jx - 2
	}

	var myRow int
	var childRows []int
	if len(kids) == 0 {
		myRow = row0 + (nh-1)/2 // center of the node's own block
	} else {
		r := row0
		for _, c := range kids {
			childRows = append(childRows, placeNode(cv, c, r, childAnchor, dir, opts))
			r += height(c, opts)
		}
		myRow = floorDiv(childRows[0]+childRows[len(childRows)-1], 2)
	}

	cv.place(n, vis, raw, myRow, textStart, dir)

	if len(kids) > 0 {
		drawConnectors(cv, myRow, textStart, textEnd, jx, dir, childRows)
	}
	return myRow
}

// drawConnectors draws the dash+junction fan from a parent (at myRow, whose text
// spans [textStart,textEnd), junction column jx) to its children on rows
// childRows. Shared by the center and every interior node.
func drawConnectors(cv *canvas, myRow, textStart, textEnd, jx, dir int, childRows []int) {
	var parentArm, childArm, dashCol, childDashCol int
	if dir == 1 {
		parentArm, childArm = left, right
		dashCol = textEnd + 1
		childDashCol = jx + 1
	} else {
		parentArm, childArm = right, left
		dashCol = textStart - 2
		childDashCol = jx - 1
	}
	cv.arm(myRow, dashCol, left|right)
	cv.arm(myRow, jx, parentArm)
	lo := minOf(myRow, childRows[0])
	hi := maxOf(myRow, childRows[len(childRows)-1])
	childSet := map[int]bool{}
	for _, r := range childRows {
		childSet[r] = true
	}
	for r := lo; r <= hi; r++ {
		arms := 0
		if r > lo {
			arms |= up
		}
		if r < hi {
			arms |= down
		}
		if childSet[r] {
			arms |= childArm
		}
		cv.arm(r, jx, arms)
	}
	for _, r := range childRows {
		cv.arm(r, childDashCol, left|right)
	}
}

// floorDiv is integer division rounding toward negative infinity, matching JS
// Math.floor(a/b) for b>0 (Go's / truncates toward zero, which differs for
// negative dividends — and map rows go negative on the upper half).
func floorDiv(a, b int) int {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

func minOf(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxOf(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// splitSides splits children into (right, left) so the two sides' heights come
// out as even as possible. Deterministic: heaviest first onto the lighter side;
// ties go left, except the first (or a lone) child always claims the right.
// Document order is preserved within each side. Ported from mm without the
// SidePref stabilization (printTree/immediate-mode layout passes no preference).
func splitSides(children []*Node) (rightKids, leftKids []*Node) {
	idx := map[*Node]int{}
	for i, c := range children {
		idx[c] = i
	}
	byWeight := append([]*Node(nil), children...)
	sort.SliceStable(byWeight, func(i, j int) bool {
		wi, wj := weight(byWeight[i]), weight(byWeight[j])
		if wi != wj {
			return wi > wj
		}
		return idx[byWeight[i]] < idx[byWeight[j]]
	})
	hRight, hLeft := 0, 0
	for _, c := range byWeight {
		if len(rightKids) == 0 || hRight < hLeft {
			rightKids = append(rightKids, c)
			hRight += weight(c)
		} else {
			leftKids = append(leftKids, c)
			hLeft += weight(c)
		}
	}
	docOrder := func(kids []*Node) {
		sort.SliceStable(kids, func(i, j int) bool { return idx[kids[i]] < idx[kids[j]] })
	}
	docOrder(rightKids)
	docOrder(leftKids)
	return rightKids, leftKids
}

// LayoutTree lays out a map with rootNode at center. If rootNode is nil, label
// is used as a virtual (file-label) root and children are the top-level nodes.
// It is the zero-Options layout — byte-identical to mm (see the golden tests).
func LayoutTree(label string, rootNode *Node, children []*Node, skin Skin) Layout {
	return LayoutTreeOpts(label, rootNode, children, skin, Options{})
}

// LayoutTreeOpts is LayoutTree with truncation/expansion Options. With the zero
// Options it is identical to LayoutTree.
func LayoutTreeOpts(label string, rootNode *Node, children []*Node, skin Skin, opts Options) Layout {
	cv := newCanvas()
	var rootVis, rootRaw []string
	if rootNode != nil {
		rootVis, rootRaw = nodeDisplayLines(rootNode, opts)
	} else {
		t := "[ " + label + " ]"
		rootVis, rootRaw = []string{t}, []string{t}
	}
	w := maxLineWidth(rootVis)
	rightKids, leftKids := splitSides(children)

	rootRow := 0
	placeSide := func(kids []*Node, dir, anchor int) {
		if len(kids) == 0 {
			return
		}
		total := 0
		for _, c := range kids {
			total += height(c, opts)
		}
		row0 := rootRow - floorDiv(total-1, 2)
		var textStart int
		if dir == 1 {
			textStart = anchor
		} else {
			textStart = anchor - w
		}
		textEnd := textStart + w
		var jx, childAnchor int
		if dir == 1 {
			jx = textEnd + 2
			childAnchor = jx + 3
		} else {
			jx = textStart - 3
			childAnchor = jx - 2
		}
		var childRows []int
		r := row0
		for _, c := range kids {
			childRows = append(childRows, placeNode(cv, c, r, childAnchor, dir, opts))
			r += height(c, opts)
		}
		drawConnectors(cv, rootRow, textStart, textEnd, jx, dir, childRows)
	}

	var rootNodeForPlacement *Node
	if rootNode != nil {
		rootNodeForPlacement = rootNode
	} else {
		rootNodeForPlacement = &Node{Text: label, Children: children}
	}
	rootPlacement := &Placement{
		Node: rootNodeForPlacement, Text: rootVis[0], Raw: rootRaw[0],
		Lines: rootVis, RawLines: rootRaw,
		Row: rootRow, Col: 0, Width: w, Height: len(rootVis), Side: 0, Virtual: rootNode == nil,
	}
	cv.placements = append(cv.placements, rootPlacement)
	top := rootRow - (len(rootVis)-1)/2
	for i := range rootVis {
		cv.touch(top+i, 0)
		cv.touch(top+i, w-1)
	}

	placeSide(rightKids, 1, 0)
	placeSide(leftKids, -1, w)

	lines := composeLines(cv, skin)
	return Layout{
		Placements: cv.placements,
		Lines:      lines,
		MinCol:     cv.minCol,
		Width:      cv.maxCol - cv.minCol + 1,
		Height:     cv.maxRow - cv.minRow + 1,
	}
}

func composeLines(cv *canvas, skin Skin) []Line {
	var lines []Line
	width := cv.maxCol - cv.minCol + 1
	for r := cv.minRow; r <= cv.maxRow; r++ {
		chars := make([]rune, width)
		for i := range chars {
			chars[i] = ' '
		}
		for key, arms := range cv.cells {
			if key[0] != r || arms == 0 {
				continue
			}
			ch, ok := skin.Junction[arms]
			if !ok {
				ch = skin.Dash
			}
			chars[key[1]-cv.minCol] = ch
		}
		for _, p := range cv.placements {
			top := p.Row - (p.Height-1)/2
			li := r - top
			if li < 0 || li >= len(p.Lines) {
				continue
			}
			cc := p.Col - cv.minCol
			for _, ch := range p.Lines[li] {
				cwid := charW(ch)
				if cwid == 0 {
					continue
				}
				if cc >= len(chars) {
					break
				}
				chars[cc] = ch
				for k := 1; k < cwid && cc+k < len(chars); k++ {
					chars[cc+k] = 0
				}
				cc += cwid
			}
		}
		lines = append(lines, Line{Row: r, Text: strings.TrimRight(string(chars), " ")})
	}
	return lines
}
