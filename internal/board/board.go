// Package board parses and serializes the session-notes markdown board.
//
// Parsing is deliberately lenient and lossless: any line the parser does not
// understand is preserved verbatim so a round-trip (Parse then Render) never
// destroys content. The board is a flat, ordered list of lines grouped into
// sections; items carry structured metadata but always remember their raw form.
package board

import (
	"fmt"
	"strings"
	"time"
)

// Status is the checkbox state of an item.
type Status int

const (
	// StatusNone is a plain "- " bullet with no checkbox (Ideas/Log style).
	StatusNone Status = iota
	// StatusOpen is "- [ ]".
	StatusOpen
	// StatusInProgress is "- [>]".
	StatusInProgress
	// StatusDone is "- [x]".
	StatusDone
	// StatusBlocked is "- [?]".
	StatusBlocked
)

func (s Status) marker() string {
	switch s {
	case StatusOpen:
		return "[ ]"
	case StatusInProgress:
		return "[>]"
	case StatusDone:
		return "[x]"
	case StatusBlocked:
		return "[?]"
	default:
		return ""
	}
}

// Item is a single list line within a section.
//
// Items form a tree: a line indented under another item (2 spaces per level) is
// a child of it, giving forum-style threaded replies. Children render depth-first
// immediately below their parent, so the flat source order is preserved exactly.
type Item struct {
	Status   Status
	Urgent   bool    // leading "!!" in the text
	Text     string  // item text with status marker and "!!" stripped
	Children []*Item // nested items (replies), in document order
	indent   string  // leading whitespace before the bullet
	raw      string  // original line, used when the line is not a recognized item
	parsed   bool    // true if this line was recognized as a structured item
}

// depth reports the item's indentation width in characters, used to reconstruct
// the tree from a flat line list. For recognized items this is the leading
// whitespace before the bullet; for raw lines it is the leading whitespace of
// the original text (blank lines are depth 0).
func (it *Item) depth() int {
	if it.parsed {
		return len(it.indent)
	}
	return len(it.raw) - len(strings.TrimLeft(it.raw, " \t"))
}

// Section is a "## Heading" and the lines beneath it (until the next heading).
type Section struct {
	Title string
	Items []*Item
}

// CanonicalSections are the section types a fresh board ships with, in template
// order. Used by the TUI's "add sections" overlay to offer the canonical set.
var CanonicalSections = []string{"Plan", "Threads", "Questions", "Ideas", "Log"}

// Board is the whole parsed document.
type Board struct {
	Path        string
	Frontmatter Frontmatter
	// preamble holds any raw lines that appear after the frontmatter but before
	// the first "## " heading (blank lines, stray text). Preserved verbatim.
	preamble []string
	Sections []*Section
	// hadFrontmatter records whether the source began with a "---" block.
	hadFrontmatter bool
}

// Frontmatter is the tiny fixed key set at the top of the board. Unknown keys
// are preserved verbatim via extra.
type Frontmatter struct {
	Session string
	Cwd     string
	Started string
	extra   []string // raw "key: value" lines for keys we don't model
}

// DisplayText returns the display text of an item (without marker/urgency), or
// the raw line for unrecognized lines.
func (it *Item) DisplayText() string {
	if !it.parsed {
		return strings.TrimSpace(it.raw)
	}
	return it.Text
}

// IsItem reports whether the line is a recognized bullet item (vs. preserved raw).
func (it *Item) IsItem() bool { return it.parsed }

// Raw returns the item's verbatim source line. For continuation lines (raw,
// non-bullet content indented under a bullet) this preserves internal spacing
// exactly, so ASCII drawings render unmodified rather than being trimmed.
func (it *Item) Raw() string { return it.raw }

// IsContinuation reports whether this item is a continuation line: an
// unrecognized (non-bullet) raw line. When such a line is indented deeper than a
// preceding bullet, the parser attaches it to that bullet as a child, and it
// renders as part of the bullet's visual block rather than as its own nav stop.
func (it *Item) IsContinuation() bool { return !it.parsed }

// Render produces the exact line for this item.
func (it *Item) render() string {
	if !it.parsed {
		return it.raw
	}
	var b strings.Builder
	b.WriteString(it.indent)
	b.WriteString("- ")
	if m := it.Status.marker(); m != "" {
		b.WriteString(m)
		b.WriteString(" ")
	}
	if it.Urgent {
		b.WriteString("!! ")
	}
	b.WriteString(it.Text)
	return b.String()
}

// CycleStatus advances an item's checkbox: [ ] -> [>] -> [x] -> [ ].
// Plain (StatusNone) items are left unchanged.
func (it *Item) CycleStatus() {
	switch it.Status {
	case StatusOpen:
		it.Status = StatusInProgress
	case StatusInProgress:
		it.Status = StatusDone
	case StatusDone:
		it.Status = StatusOpen
	case StatusBlocked:
		it.Status = StatusOpen
	default:
		// StatusNone: leave as-is.
	}
}

// ToggleUrgent flips the urgency flag on a recognized item.
func (it *Item) ToggleUrgent() {
	if it.parsed {
		it.Urgent = !it.Urgent
	}
}

// Section returns the section with the given title, or nil.
func (b *Board) Section(title string) *Section {
	for _, s := range b.Sections {
		if s.Title == title {
			return s
		}
	}
	return nil
}

// UrgentOpenItems returns all recognized items that are urgent and not done,
// including nested replies, in document order.
func (b *Board) UrgentOpenItems() []*Item {
	var out []*Item
	var walk func(items []*Item)
	walk = func(items []*Item) {
		for _, it := range items {
			if it.parsed && it.Urgent && it.Status != StatusDone {
				out = append(out, it)
			}
			walk(it.Children)
		}
	}
	for _, s := range b.Sections {
		walk(s.Items)
	}
	return out
}

// AppendLog appends a log line "- HH:MM author: text" to the Log section,
// creating the section if it does not exist.
func (b *Board) AppendLog(author, text string) {
	b.AppendLogAt(time.Now(), author, text)
}

// AppendLogAt is AppendLog with an explicit timestamp (for tests).
func (b *Board) AppendLogAt(t time.Time, author, text string) {
	line := fmt.Sprintf("- %02d:%02d %s: %s", t.Hour(), t.Minute(), author, text)
	s := b.Section("Log")
	if s == nil {
		s = &Section{Title: "Log"}
		b.Sections = append(b.Sections, s)
	}
	s.insert(parseLine(line))
}

// insert appends an item to the section, but before any trailing blank lines so
// that section-separating whitespace stays at the end of the section.
func (s *Section) insert(it *Item) {
	i := len(s.Items)
	for i > 0 && !s.Items[i-1].parsed && strings.TrimSpace(s.Items[i-1].raw) == "" {
		i--
	}
	s.Items = append(s.Items, nil)
	copy(s.Items[i+1:], s.Items[i:])
	s.Items[i] = it
}

// AddItem appends a new open item to the named section (created if missing).
func (b *Board) AddItem(sectionTitle, text string) *Item {
	s := b.Section(sectionTitle)
	if s == nil {
		s = &Section{Title: sectionTitle}
		b.Sections = append(b.Sections, s)
	}
	status := StatusOpen
	// Ideas and Log conventionally use plain bullets.
	if sectionTitle == "Ideas" || sectionTitle == "Log" {
		status = StatusNone
	}
	it := &Item{parsed: true, Status: status, Text: text}
	s.insert(it)
	return it
}

// AddSection appends a new empty section with the given title and returns it.
// If a section with that title already exists it is returned unchanged (no
// duplicate). New sections are added at the end of the board, except that a
// "Log" section, if present, is kept last: new sections are inserted just before
// it. Blank-line spacing between sections is kept consistent with the template
// (every section but the last ends with a trailing blank line).
func (b *Board) AddSection(title string) *Section {
	if s := b.Section(title); s != nil {
		return s
	}
	sec := &Section{Title: title}
	// Insert before Log if Log exists and we are not adding Log itself.
	idx := len(b.Sections)
	if title != "Log" {
		for i, s := range b.Sections {
			if s.Title == "Log" {
				idx = i
				break
			}
		}
	}
	// The section that will precede the new one must end with a blank line so
	// the two are visually separated.
	if idx > 0 {
		ensureTrailingBlank(b.Sections[idx-1])
	}
	// The new section needs its own trailing blank line only if another section
	// will follow it (i.e. it is not becoming the last section).
	if idx < len(b.Sections) {
		sec.Items = append(sec.Items, &Item{raw: ""})
	}
	b.Sections = append(b.Sections, nil)
	copy(b.Sections[idx+1:], b.Sections[idx:])
	b.Sections[idx] = sec
	return sec
}

// ensureTrailingBlank appends a blank raw line to a section unless it already
// ends with one.
func ensureTrailingBlank(s *Section) {
	if n := len(s.Items); n > 0 && !s.Items[n-1].parsed && strings.TrimSpace(s.Items[n-1].raw) == "" {
		return
	}
	s.Items = append(s.Items, &Item{raw: ""})
}

// DeleteItem removes the item at index i from the section's top-level items.
// Returns false if out of range. Any children of the removed item go with it.
func (s *Section) DeleteItem(i int) bool {
	if i < 0 || i >= len(s.Items) {
		return false
	}
	s.Items = append(s.Items[:i], s.Items[i+1:]...)
	return true
}

// AddReply appends a forum-style reply as the last child of parent and returns
// it. The reply is a plain bullet (no checkbox) indented one level (2 spaces)
// deeper than its parent; text is typically "author: message". Nesting is
// arbitrary — replying to a reply just nests one level further.
func (b *Board) AddReply(parent *Item, text string) *Item {
	child := &Item{parsed: true, Status: StatusNone, Text: text, indent: parent.indent + "  "}
	parent.Children = append(parent.Children, child)
	return child
}

// Remove deletes target from anywhere in the board — a top-level item or a
// nested reply — taking its whole subtree with it. Returns false if not found.
func (b *Board) Remove(target *Item) bool {
	for _, s := range b.Sections {
		if items, ok := removeItem(s.Items, target); ok {
			s.Items = items
			return true
		}
	}
	return false
}

// removeItem returns items with target removed (searching descendants), and
// whether it was found. Only the slice at the level where target lives is
// rebuilt; other levels are mutated in place via their Children field.
func removeItem(items []*Item, target *Item) ([]*Item, bool) {
	for i, it := range items {
		if it == target {
			return append(items[:i:i], items[i+1:]...), true
		}
		if newCh, ok := removeItem(it.Children, target); ok {
			it.Children = newCh
			return items, true
		}
	}
	return items, false
}
