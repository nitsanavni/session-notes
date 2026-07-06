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
type Item struct {
	Status Status
	Urgent bool   // leading "!!" in the text
	Text   string // item text with status marker and "!!" stripped
	indent string // leading whitespace before the bullet
	raw    string // original line, used when the line is not a recognized item
	parsed bool   // true if this line was recognized as a structured item
}

// Section is a "## Heading" and the lines beneath it (until the next heading).
type Section struct {
	Title string
	Items []*Item
}

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

// UrgentOpenItems returns all recognized items that are urgent and not done.
func (b *Board) UrgentOpenItems() []*Item {
	var out []*Item
	for _, s := range b.Sections {
		for _, it := range s.Items {
			if it.parsed && it.Urgent && it.Status != StatusDone {
				out = append(out, it)
			}
		}
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

// DeleteItem removes the item at index i from the section. Returns false if out
// of range.
func (s *Section) DeleteItem(i int) bool {
	if i < 0 || i >= len(s.Items) {
		return false
	}
	s.Items = append(s.Items[:i], s.Items[i+1:]...)
	return true
}
