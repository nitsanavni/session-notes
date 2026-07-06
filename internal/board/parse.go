package board

import (
	"os"
	"regexp"
	"strings"
)

var itemRe = regexp.MustCompile(`^(\s*)- (?:\[([ >xX?])\] )?(.*)$`)

// Parse parses board markdown. It never fails: anything it doesn't understand
// is kept as raw lines and reproduced verbatim by Render.
func Parse(src string) *Board {
	b := &Board{}
	// Normalize line endings but remember nothing else; we re-add trailing \n.
	src = strings.ReplaceAll(src, "\r\n", "\n")
	lines := strings.Split(src, "\n")
	// strings.Split leaves a trailing "" if src ends with \n; drop it so we
	// don't render a spurious blank line (Render always ends with \n).
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}

	i := 0
	// Frontmatter: only if the very first line is exactly "---".
	if len(lines) > 0 && strings.TrimRight(lines[0], " \t") == "---" {
		end := -1
		for j := 1; j < len(lines); j++ {
			if strings.TrimRight(lines[j], " \t") == "---" {
				end = j
				break
			}
		}
		if end > 0 {
			b.hadFrontmatter = true
			for _, l := range lines[1:end] {
				key, val, ok := strings.Cut(l, ":")
				if !ok {
					b.Frontmatter.extra = append(b.Frontmatter.extra, l)
					continue
				}
				val = strings.TrimSpace(val)
				switch strings.TrimSpace(key) {
				case "session":
					b.Frontmatter.Session = val
				case "cwd":
					b.Frontmatter.Cwd = val
				case "started":
					b.Frontmatter.Started = val
				default:
					b.Frontmatter.extra = append(b.Frontmatter.extra, l)
				}
			}
			i = end + 1
		}
	}

	var cur *Section
	for ; i < len(lines); i++ {
		line := lines[i]
		if title, ok := strings.CutPrefix(line, "## "); ok {
			cur = &Section{Title: strings.TrimSpace(title)}
			b.Sections = append(b.Sections, cur)
			continue
		}
		if cur == nil {
			b.preamble = append(b.preamble, line)
			continue
		}
		cur.Items = append(cur.Items, parseLine(line))
	}
	// Fold each section's flat line list into a tree by indentation. This is a
	// pure regrouping: depth-first traversal reproduces the original order, so
	// the round-trip stays lossless.
	for _, s := range b.Sections {
		s.Items = buildTree(s.Items)
	}
	return b
}

// buildTree turns a flat, in-order list of items into a forest using a stack
// keyed on each line's indentation depth. A line becomes a child of the nearest
// preceding line with strictly smaller depth, else a root. Because nodes are
// only ever appended (never inserted mid-list), a depth-first walk of the result
// yields the exact original order.
func buildTree(flat []*Item) []*Item {
	var roots []*Item
	type frame struct {
		item  *Item
		depth int
	}
	var stack []frame
	for _, it := range flat {
		d := it.depth()
		for len(stack) > 0 && stack[len(stack)-1].depth >= d {
			stack = stack[:len(stack)-1]
		}
		if len(stack) == 0 {
			roots = append(roots, it)
		} else {
			parent := stack[len(stack)-1].item
			parent.Children = append(parent.Children, it)
		}
		stack = append(stack, frame{it, d})
	}
	return roots
}

// parseLine turns one line into an Item. Non-bullet lines become raw items.
func parseLine(line string) *Item {
	m := itemRe.FindStringSubmatch(line)
	if m == nil {
		return &Item{parsed: false, raw: line}
	}
	it := &Item{parsed: true, indent: m[1], raw: line}
	switch m[2] {
	case "":
		it.Status = StatusNone
	case " ":
		it.Status = StatusOpen
	case ">":
		it.Status = StatusInProgress
	case "x", "X":
		it.Status = StatusDone
	case "?":
		it.Status = StatusBlocked
	}
	text := m[3]
	if rest, ok := strings.CutPrefix(text, "!! "); ok {
		it.Urgent = true
		text = rest
	} else if text == "!!" {
		it.Urgent = true
		text = ""
	}
	it.Text = text
	return it
}

// Render serializes the board back to markdown. Round-trip safe: unrecognized
// lines come back byte-for-byte.
func (b *Board) Render() string {
	var sb strings.Builder
	if b.hadFrontmatter {
		sb.WriteString("---\n")
		if b.Frontmatter.Session != "" {
			sb.WriteString("session: " + b.Frontmatter.Session + "\n")
		}
		if b.Frontmatter.Cwd != "" {
			sb.WriteString("cwd: " + b.Frontmatter.Cwd + "\n")
		}
		if b.Frontmatter.Started != "" {
			sb.WriteString("started: " + b.Frontmatter.Started + "\n")
		}
		for _, l := range b.Frontmatter.extra {
			sb.WriteString(l + "\n")
		}
		sb.WriteString("---\n")
	}
	for _, l := range b.preamble {
		sb.WriteString(l + "\n")
	}
	for _, s := range b.Sections {
		sb.WriteString("## " + s.Title + "\n")
		renderItems(&sb, s.Items)
	}
	return sb.String()
}

// renderItems writes items depth-first: each item's line, then its children.
func renderItems(sb *strings.Builder, items []*Item) {
	for _, it := range items {
		sb.WriteString(it.render() + "\n")
		renderItems(sb, it.Children)
	}
}

// Load reads and parses a board file.
func Load(path string) (*Board, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	b := Parse(string(data))
	b.Path = path
	return b, nil
}

// Save atomically writes the board to its Path (temp file + rename).
func (b *Board) Save() error {
	return b.SaveTo(b.Path)
}

// SaveTo atomically writes the board to the given path.
func (b *Board) SaveTo(path string) error {
	dir := "."
	if idx := strings.LastIndexByte(path, '/'); idx >= 0 {
		dir = path[:idx]
	}
	tmp, err := os.CreateTemp(dir, ".board-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after successful rename
	if _, err := tmp.WriteString(b.Render()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
