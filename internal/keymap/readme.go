package keymap

import (
	"fmt"
	"strings"
)

// Markers delimit the generated keybindings table in README.md. Everything
// between them is owned by `session-notes docs keys` / go:generate; edit the
// tables in keymap.go instead.
const (
	BeginMarker = "<!-- BEGIN GENERATED KEYS: edit internal/keymap, run go generate -->"
	EndMarker   = "<!-- END GENERATED KEYS -->"
)

// InjectMarkdown replaces the content between the markers in src with the
// freshly rendered keybindings table, returning the updated document. It errors
// if the markers are missing or out of order.
func InjectMarkdown(src string) (string, error) {
	bi := strings.Index(src, BeginMarker)
	ei := strings.Index(src, EndMarker)
	if bi < 0 || ei < 0 || ei < bi {
		return "", fmt.Errorf("keymap: README markers missing or out of order (%q / %q)", BeginMarker, EndMarker)
	}
	var b strings.Builder
	b.WriteString(src[:bi])
	b.WriteString(BeginMarker)
	b.WriteString("\n\n")
	b.WriteString(Markdown())
	b.WriteString("\n")
	b.WriteString(src[ei:])
	return b.String(), nil
}
