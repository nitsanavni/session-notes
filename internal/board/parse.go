package board

import (
	"os"
	"regexp"
	"strings"
)

var itemRe = regexp.MustCompile(`^(\s*)- (?:\[([ >xX?])\] )?(.*)$`)

// anchorRe matches a trailing block-ref anchor " ^<id>" (Obsidian-style) at the
// very end of a line: some visible text, whitespace, a caret, then the id. The
// id alphabet is base36 (assignment) but parsing tolerates any [A-Za-z0-9]
// hand-written id. A caret anywhere else in the line is plain text.
var anchorRe = regexp.MustCompile(`^(.*\S)\s+\^([A-Za-z0-9]+)$`)

// splitAnchor separates a trailing " ^id" anchor from text, returning the text
// without it and the id ("" when there is no anchor).
func splitAnchor(text string) (rest, id string) {
	if m := anchorRe.FindStringSubmatch(text); m != nil {
		return m[1], m[2]
	}
	return text, ""
}

// StripRawAnchor removes a trailing " ^id" node anchor from a source line,
// leaving everything else (indent, bullet, marker, text) intact. It is the
// anchor-insensitive form of a raw line: two lines that differ only by their
// node id compare equal after stripping. A line with no anchor is unchanged.
func StripRawAnchor(line string) string {
	rest, id := splitAnchor(line)
	if id == "" {
		return line
	}
	return rest
}

// SplitRawAnchor separates a source line's trailing " ^id" node anchor from the
// rest of the line, returning the line without the anchor and the id ("" when
// there is no anchor). Unlike splitAnchor it operates on a raw source line
// (indent + bullet + text), so callers that report line-level diffs (watch
// --json) can surface a clean line plus its stable node id.
func SplitRawAnchor(line string) (rest, id string) {
	return splitAnchor(line)
}

// StripAnchors removes the trailing node anchor from every line of content —
// the whole-document form of StripRawAnchor, used by surfaces that diff raw
// board text (web delivery receipts) so the one-time id-assignment churn does
// not read as a change on every line.
func StripAnchors(content string) string {
	lines := strings.Split(content, "\n")
	for i, l := range lines {
		lines[i] = StripRawAnchor(l)
	}
	return strings.Join(lines, "\n")
}

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
				case "title":
					b.Frontmatter.Title = val
				case "pin-cadence":
					b.Frontmatter.PinCadence = val
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
			t, id := splitAnchor(strings.TrimSpace(title))
			cur = &Section{Title: t, ID: id}
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
	// A trailing " ^id" anchor is the item's stable node id; strip it (like the
	// leading markers) so it survives save round-trips via render() but never
	// pollutes the display Text. Stripped before the leading markers since it is
	// at the opposite end of the line.
	text, it.ID = splitAnchor(text)
	// Leading item markers are mutually exclusive and analogous: "!!" is urgent
	// (inject once), "!pin" is pinned (re-inject on cadence). Strip whichever is
	// present so the marker survives save round-trips via render() but never
	// pollutes the display Text.
	switch {
	case strings.HasPrefix(text, "!! "):
		it.Urgent = true
		text = text[len("!! "):]
	case text == "!!":
		it.Urgent = true
		text = ""
	case strings.HasPrefix(text, "!pin "):
		it.Pinned = true
		text = text[len("!pin "):]
	case text == "!pin":
		it.Pinned = true
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
		if b.Frontmatter.Title != "" {
			sb.WriteString("title: " + b.Frontmatter.Title + "\n")
		}
		if b.Frontmatter.PinCadence != "" {
			sb.WriteString("pin-cadence: " + b.Frontmatter.PinCadence + "\n")
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
		sb.WriteString("## " + s.Title)
		if s.ID != "" {
			sb.WriteString(" ^" + s.ID)
		}
		sb.WriteString("\n")
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

// Save atomically writes the board to its Path (temp file + rename), holding the
// board's exclusive advisory lock for the duration of the write.
func (b *Board) Save() error {
	return b.SaveTo(b.Path)
}

// SaveTo atomically writes the board to the given path under the board lock
// (WithLock). This last-writer-wins path is used by the hooks and by TUI
// mutations that do not rebase; the TUI's text-entry mutations use SaveRebasing
// to merge concurrent external writes instead of clobbering them. The write is
// journaled (author SaveAuthor) so it appears in the board's shared history;
// a first write (no file yet) is not — there is no state to undo back to.
func (b *Board) SaveTo(path string) error {
	return WithLock(path, func() error {
		before, rerr := os.ReadFile(path)
		out := b.Render()
		if err := writeAtomicRaw(path, out); err != nil {
			return err
		}
		if rerr == nil {
			journalRecord(path, string(before), out, SaveAuthor)
		}
		return nil
	})
}

// writeAtomic writes the board to path via a temp file + rename, WITHOUT taking
// the lock. Callers must already hold WithLock(path) (SaveTo and SaveRebasing
// do). Splitting the raw write out lets SaveRebasing read, merge, and write all
// within a single lock acquisition without self-deadlocking on a nested lock.
func (b *Board) writeAtomic(path string) error {
	return writeAtomicRaw(path, b.Render())
}

// writeAtomicRaw writes content to path via a temp file + rename, WITHOUT taking
// the lock. Callers must already hold WithLock(path). It is the shared
// last-step of every board write (board renders, and the raw content the edit
// CLI's replace escape hatch produces).
func writeAtomicRaw(path, content string) error {
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
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// RebaseResult reports the outcome of SaveRebasing.
type RebaseResult struct {
	// Board is the board actually written to disk: the receiver when there was
	// no concurrent external change, or the freshly-reparsed disk board with the
	// pending mutation re-applied when there was.
	Board *Board
	// Content is the exact markdown written to disk (use it as the new
	// last-known-disk state so a subsequent SaveRebasing does not see a phantom
	// external change).
	Content string
	// Rebased is true when an external write was detected and merged.
	Rebased bool
	// DiskPrev is the external disk content that was rebased onto (the state
	// after the external write but before the merge). Empty unless Rebased. Used
	// by callers to record the external edit in undo history.
	DiskPrev string
}

// SaveRebasing atomically persists b to path, merging any concurrent external
// write instead of clobbering it. The whole read → merge → write happens inside
// one WithLock(path), so no external writer can interleave.
//
// lastDisk is the content the caller believes is currently on disk (what it last
// loaded or saved). Under the lock SaveRebasing re-reads disk:
//   - if disk is missing or equals lastDisk, there was no external change: b is
//     written as-is (b already carries the caller's mutation);
//   - otherwise disk is reparsed into a fresh board, reapply(fresh) re-applies
//     the caller's one pending mutation onto that fresh tree, and the merged
//     result is written. The caller's stale in-memory tree is discarded in
//     favour of RebaseResult.Board.
//
// reapply must be idempotent w.r.t. the fresh tree and must not lose the user's
// text (locate the target by its raw source line; if it vanished, append the
// text as a new item — see the tui package's applyOp).
func (b *Board) SaveRebasing(path, lastDisk string, reapply func(fresh *Board)) (RebaseResult, error) {
	var res RebaseResult
	err := WithLock(path, func() error {
		data, rerr := os.ReadFile(path)
		if rerr != nil && !os.IsNotExist(rerr) {
			return rerr
		}
		disk := string(data)
		if os.IsNotExist(rerr) || disk == lastDisk {
			res.Board = b
			res.Content = b.Render()
			if err := b.writeAtomic(path); err != nil {
				return err
			}
			if !os.IsNotExist(rerr) {
				journalRecord(path, disk, res.Content, SaveAuthor)
			}
			return nil
		}
		fresh := Parse(disk)
		fresh.Path = path
		if reapply != nil {
			reapply(fresh)
		}
		res.Board = fresh
		res.Content = fresh.Render()
		res.Rebased = true
		res.DiskPrev = disk
		if err := fresh.writeAtomic(path); err != nil {
			return err
		}
		journalRecord(path, disk, res.Content, SaveAuthor)
		return nil
	})
	return res, err
}
