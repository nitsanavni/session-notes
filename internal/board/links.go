package board

import (
	"path/filepath"
	"regexp"
	"strings"
)

// linkRe matches [[name]] wiki-links. The name is anything but square
// brackets, so punctuation immediately touching the delimiters (e.g.
// "see [[foo]]." or "([[bar]])") never leaks into the match.
var linkRe = regexp.MustCompile(`\[\[([^\[\]]+)\]\]`)

// ExtractLinks returns the names of all [[wiki-link]]s found in text, in
// order of appearance. Names are trimmed of surrounding whitespace. Returns
// nil if there are none.
func ExtractLinks(text string) []string {
	matches := linkRe.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, strings.TrimSpace(m[1]))
	}
	return out
}

// NotesDir returns the side-file directory for a board's session:
// <boards-dir>/<session-id>.notes, scoped by the board's frontmatter session
// id.
func NotesDir(b *Board) string {
	return filepath.Join(BoardsDir(), b.Frontmatter.Session+".notes")
}

// ResolveLink resolves a [[name]] wiki-link found on board b to the path of
// its side markdown file: <boards-dir>/<session-id>.notes/name.md.
func ResolveLink(b *Board, name string) string {
	return filepath.Join(NotesDir(b), name+".md")
}
