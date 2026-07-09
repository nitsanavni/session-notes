package mindmap

import "strings"

// Render lays out the forest and returns plain terminal lines, byte-identical to
// mm's `tree` subcommand (`printTree`): a lone `# Title` heading becomes the
// center, otherwise `label` (typically the file's basename) is the file-label
// center. Wide-glyph hole cells are stripped, matching mm's final output.
//
// Styling hooks (cursor inverse, dim done, reload colors) are a phase-2 concern;
// this returns the plain body only.
func Render(label string, roots []*Node, skin Skin) []string {
	rootNode := TitleRoot(roots)
	children := roots
	if rootNode != nil {
		children = rootNode.Children
	}
	lay := LayoutTree(label, rootNode, children, skin)
	out := make([]string, len(lay.Lines))
	for i, l := range lay.Lines {
		out[i] = strings.ReplaceAll(l.Text, "\x00", "")
	}
	return out
}
