// Package mindmap is a Go port of mm (github.com/nitsanavni/mm), a terminal
// mindmap editor: a lossless tree model over a markdown bullet outline plus a
// center-outward layout with box-drawing connectors.
//
// This package is deliberately self-contained and does not reuse
// internal/board: board models a whole session board (YAML frontmatter, fixed
// `##` sections, raw-line preservation for lenient round-tripping) as a flat,
// section-grouped structure, whereas mindmap needs a pure recursive node tree
// rooted at an optional `# heading` title. The two models overlap only
// superficially (both parse `- ` bullets with status markers); forcing board's
// section machinery onto the mindmap tree would be a worse fit than a small
// dedicated model. Phase 2/3 will bridge a board into this tree at the view
// boundary rather than by sharing the parse type.
package mindmap

import (
	"regexp"
	"strings"
)

// Checkbox is the tri-state of a node's leading GFM checkbox: absent (""),
// unchecked (" ") or checked ("x"). It mirrors mm's `Checkbox` type; the richer
// board status set ([>]/[?]) is a phase-3 concern layered on top.
type Checkbox string

const (
	// NoCheckbox is a plain "- " bullet with no "[ ]" marker.
	NoCheckbox Checkbox = ""
	// Unchecked is "- [ ]".
	Unchecked Checkbox = " "
	// Checked is "- [x]".
	Checked Checkbox = "x"
)

// Node is one node in the mindmap tree.
type Node struct {
	Text     string   // node text, with inline markdown markers kept
	Checkbox Checkbox // "", " " or "x"
	Folded   bool     // subtree collapsed (runtime editor state; never serialized)
	Heading  bool     // a `# Title` map root, serialized as a heading line
	// Suffix is a short trailing annotation (e.g. a "[+3]"/"[2 replies]" fold
	// count) that is ALWAYS kept when the node is truncated: the base Text is
	// clipped first, then the suffix appended, so it can never be cut off. Never
	// serialized; used only by the TUI bridge. Empty by default (mm fixtures).
	Suffix   string
	Children []*Node
}

var (
	lineRE    = regexp.MustCompile(`^(\s*)- (?:\[( |[xX])\] )?(.*)$`)
	headingRE = regexp.MustCompile(`^# (.*)$`)
	trailWS   = regexp.MustCompile(`\s+$`)
)

// Parse reads a markdown bullet outline into a forest of nodes, mirroring mm's
// model.parse: 2-space indent nesting, `- [ ]`/`- [x]` checkboxes (GFM `[X]` is
// normalized to lowercase), and a leading `# Title` heading as a map-root node.
// Blank lines and trailing whitespace are normalized away; any unrecognized
// line becomes a root-level node so no content is silently dropped.
func Parse(md string) []*Node {
	var roots []*Node
	type frame struct {
		indent int
		node   *Node
	}
	var stack []frame
	for _, line := range strings.Split(md, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var indent int
		var n *Node
		if m := lineRE.FindStringSubmatch(line); m != nil {
			indent = len(m[1])
			cb := NoCheckbox
			if m[2] != "" {
				cb = Checkbox(strings.ToLower(m[2]))
			}
			n = &Node{Text: trailWS.ReplaceAllString(m[3], ""), Checkbox: cb}
		} else if h := headingRE.FindStringSubmatch(line); h != nil {
			title := trailWS.ReplaceAllString(h[1], "")
			// An empty `# ` heading names nothing: drop it and let the bullets
			// stand at top level (matching mm).
			if title == "" {
				continue
			}
			indent = -1
			n = &Node{Text: title, Heading: true}
		} else {
			indent = 0
			n = &Node{Text: strings.TrimSpace(line)}
		}
		for len(stack) > 0 && stack[len(stack)-1].indent >= indent {
			stack = stack[:len(stack)-1]
		}
		if len(stack) == 0 {
			roots = append(roots, n)
		} else {
			p := stack[len(stack)-1].node
			p.Children = append(p.Children, n)
		}
		stack = append(stack, frame{indent, n})
	}
	return roots
}

// Serialize renders a forest back to a markdown outline. It is the exact inverse
// of Parse for normalized input (Parse then Serialize is byte-identical, and
// round-trips are idempotent).
func Serialize(nodes []*Node) string {
	var b strings.Builder
	serialize(&b, nodes, 0)
	return b.String()
}

func serialize(b *strings.Builder, nodes []*Node, depth int) {
	for _, n := range nodes {
		if n.Heading && depth == 0 {
			b.WriteString("# ")
			b.WriteString(n.Text)
			b.WriteByte('\n')
			serialize(b, n.Children, 0)
			continue
		}
		box := ""
		if n.Checkbox != NoCheckbox {
			box = "[" + string(n.Checkbox) + "] "
		}
		b.WriteString(strings.Repeat("  ", depth))
		b.WriteString("- ")
		b.WriteString(box)
		b.WriteString(n.Text)
		b.WriteByte('\n')
		serialize(b, n.Children, depth+1)
	}
}

// TitleRoot returns the map's real center — a lone `# Title` heading — or nil.
func TitleRoot(roots []*Node) *Node {
	if len(roots) == 1 && roots[0].Heading {
		return roots[0]
	}
	return nil
}
