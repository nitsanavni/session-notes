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
	return b
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
		for _, it := range s.Items {
			sb.WriteString(it.render() + "\n")
		}
	}
	return sb.String()
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
