package board

import (
	"os"
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

// ResolveLink resolves a [[name]] wiki-link found on board b. A plain name is
// a side note: <boards-dir>/<session-id>.notes/name.md. A path-like name (has
// a slash, or starts with ./ ../ ~ /) is a file path: absolute and ~ names as
// themselves, relative names against the board's frontmatter cwd (where
// Claude runs). Path links keep an existing extension; extensionless ones get
// ".md" only in the side-notes case.
func ResolveLink(b *Board, name string) string {
	if IsPathLink(name) {
		p := name
		if strings.HasPrefix(p, "~/") || p == "~" {
			if home, err := os.UserHomeDir(); err == nil {
				p = filepath.Join(home, strings.TrimPrefix(p, "~"))
			}
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(b.Frontmatter.Cwd, p)
		}
		return filepath.Clean(p)
	}
	return filepath.Join(NotesDir(b), name+".md")
}

// IsPathLink reports whether a [[link]] name addresses a file path rather
// than a session side note.
func IsPathLink(name string) bool {
	return strings.ContainsRune(name, '/') || strings.HasPrefix(name, "~")
}
