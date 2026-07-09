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
	Pinned   bool    // leading "!pin" in the text (re-injected on cadence)
	Text     string  // item text with status marker and "!!"/"!pin" stripped
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
	// Title is an optional short human name for the session, shown by the TUI
	// board header and the dashboard/picker in place of the raw session id.
	Title string
	// PinCadence is an optional per-board override (Go duration syntax, e.g.
	// "10m", "90s", "1h") for how often pinned items re-inject even when
	// unchanged. Empty means use the built-in default. Parsed leniently by the
	// prompt-submit hook / `pins` command; garbage falls back to the default.
	PinCadence string
	extra      []string // raw "key: value" lines for keys we don't model
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
	} else if it.Pinned {
		b.WriteString("!pin ")
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

// TogglePinned flips the pinned flag on a recognized item. Pinned items carry a
// leading "!pin" marker and are re-injected into Claude's context on a cadence
// by the prompt-submit hook (see PinnedItems).
func (it *Item) TogglePinned() {
	if it.parsed {
		it.Pinned = !it.Pinned
	}
}

// SetTitle sets the frontmatter title (the empty string clears it). Setting a
// title on a board that had no frontmatter block promotes it to one so the title
// actually renders; clearing on such a board leaves it frontmatter-less.
func (b *Board) SetTitle(title string) {
	b.Frontmatter.Title = title
	if title != "" {
		b.hadFrontmatter = true
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

// FindByRawInSection returns the first item within the section titled
// sectionTitle whose exact source line (Raw) equals raw, searching the whole
// item tree (nested replies included) in document order, or nil. It is used by
// the TUI's rebase to relocate a mutation's target in a freshly-reparsed disk
// tree by its verbatim line — a stable identity across an external edit that did
// not touch that particular line. An empty raw never matches (it would collide
// with blank continuation lines), so callers treat "" as "target gone".
func (b *Board) FindByRawInSection(sectionTitle, raw string) *Item {
	if raw == "" {
		return nil
	}
	s := b.Section(sectionTitle)
	if s == nil {
		return nil
	}
	var found *Item
	var walk func(items []*Item)
	walk = func(items []*Item) {
		for _, it := range items {
			if found != nil {
				return
			}
			if it.Raw() == raw {
				found = it
				return
			}
			walk(it.Children)
		}
	}
	walk(s.Items)
	return found
}

// NormalizeItemText computes an item's merge-matching key: the item text with
// its status marker, "!!" urgency flag, indentation, and surrounding whitespace
// removed. It reuses parseLine so a parsed item and its raw source line
// normalize identically, letting a fuzzy match survive cosmetic drift (a flipped
// checkbox, an added "!!", a re-indent) that FindByRawInSection's verbatim
// identity would miss. Matching is case-sensitive and does NOT collapse internal
// spaces: a genuinely reworded line normalizes differently and should miss.
func NormalizeItemText(rawLine string) string {
	it := parseLine(rawLine)
	if it.parsed {
		return strings.TrimSpace(it.Text)
	}
	return strings.TrimSpace(it.raw)
}

// FindByTextInSection returns the item within the section titled sectionTitle
// whose normalized text (see NormalizeItemText) equals normText, searching the
// whole item tree (nested replies included) in document order, together with the
// COUNT of items that matched. Only recognized bullet items are considered.
//
// Callers MUST treat count != 1 as "no confident match": 0 means the target is
// gone, and >1 means there are indistinguishable twins so acting on either would
// be a guess. It is the fuzzy fallback used by the TUI's rebase when a verbatim
// FindByRawInSection lookup misses because an external write cosmetically altered
// the target line.
func (b *Board) FindByTextInSection(sectionTitle, normText string) (*Item, int) {
	s := b.Section(sectionTitle)
	if s == nil {
		return nil, 0
	}
	var found *Item
	count := 0
	var walk func(items []*Item)
	walk = func(items []*Item) {
		for _, it := range items {
			if it.parsed && NormalizeItemText(it.raw) == normText {
				if found == nil {
					found = it
				}
				count++
			}
			walk(it.Children)
		}
	}
	walk(s.Items)
	return found, count
}

// SectionTitleOf returns the title of the section whose item tree contains
// target (at any depth), or "" if target is not on the board.
func (b *Board) SectionTitleOf(target *Item) string {
	if s := b.sectionOf(target); s != nil {
		return s.Title
	}
	return ""
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

// PinnedItems returns all recognized items that are pinned and not done,
// including nested replies, in document order. Pinned items are re-injected into
// Claude's context on a cadence by the prompt-submit hook (see internal/hooks).
func (b *Board) PinnedItems() []*Item {
	var out []*Item
	var walk func(items []*Item)
	walk = func(items []*Item) {
		for _, it := range items {
			if it.parsed && it.Pinned && it.Status != StatusDone {
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

// InProgressRawLines returns the verbatim source lines of every "[>]" item on
// the board (any depth), in document order. The prompt-submit hook uses these
// as stable per-item identities to age "[>]" items in its wip state file.
func (b *Board) InProgressRawLines() []string {
	var out []string
	var walk func(items []*Item)
	walk = func(items []*Item) {
		for _, it := range items {
			if it.parsed && it.Status == StatusInProgress {
				out = append(out, it.Raw())
			}
			walk(it.Children)
		}
	}
	for _, s := range b.Sections {
		walk(s.Items)
	}
	return out
}

// UnansweredClaudeQuestions returns open ("[ ]") items whose text mentions
// "@claude" but which have no "claude:"-authored reply anywhere in their
// subtree — questions addressed to Claude that Claude has not answered yet.
func (b *Board) UnansweredClaudeQuestions() []*Item {
	var out []*Item
	var hasClaudeReply func(items []*Item) bool
	hasClaudeReply = func(items []*Item) bool {
		for _, it := range items {
			if it.parsed && strings.HasPrefix(strings.TrimSpace(it.Text), "claude:") {
				return true
			}
			if hasClaudeReply(it.Children) {
				return true
			}
		}
		return false
	}
	var walk func(items []*Item)
	walk = func(items []*Item) {
		for _, it := range items {
			if it.parsed && it.Status == StatusOpen && strings.Contains(it.Text, "@claude") && !hasClaudeReply(it.Children) {
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

// WaitingOnUserCount reports the number of top-level recognized items in the
// "Waiting on User" section (the human's attention queue), or 0 if absent.
func (b *Board) WaitingOnUserCount() int {
	s := b.Section("Waiting on User")
	if s == nil {
		return 0
	}
	n := 0
	for _, it := range s.Items {
		if it.parsed {
			n++
		}
	}
	return n
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
//
// When the item is the section's very first bullet — the common case right after
// a new section is created and its first item added — a blank line is kept
// between the "## " header and the item for readability. This is a data-level
// rule (the blank becomes a real, verbatim line), so it survives round-trips and
// stays consistent when a rebase re-applies the add onto a freshly-parsed board.
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
	first := !s.hasParsedItem()
	s.insert(it)
	if first {
		ensureLeadingBlank(s)
	}
	return it
}

// hasParsedItem reports whether the section already contains at least one
// recognized bullet item (blank/continuation lines don't count).
func (s *Section) hasParsedItem() bool {
	for _, it := range s.Items {
		if it.parsed {
			return true
		}
	}
	return false
}

// ensureLeadingBlank prepends a blank line to the section unless it already
// begins with one, so the header and the first item stay visually separated.
func ensureLeadingBlank(s *Section) {
	if n := len(s.Items); n > 0 && !s.Items[0].parsed && strings.TrimSpace(s.Items[0].raw) == "" {
		return
	}
	s.Items = append([]*Item{{raw: ""}}, s.Items...)
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

// ArchiveTitle and LogTitle are the sections with special archive semantics:
// they can never themselves be archived, and Archive is the destination for
// everything the user archives with `d`.
const (
	ArchiveTitle = "Archive"
	LogTitle     = "Log"
)

// ParentOf returns the item whose Children contain target, or nil if target is
// top-level (or not on the board).
func (b *Board) ParentOf(target *Item) *Item {
	var find func(items []*Item) *Item
	find = func(items []*Item) *Item {
		for _, it := range items {
			for _, c := range it.Children {
				if c == target {
					return it
				}
			}
			if p := find(it.Children); p != nil {
				return p
			}
		}
		return nil
	}
	for _, s := range b.Sections {
		if p := find(s.Items); p != nil {
			return p
		}
	}
	return nil
}

// sectionOf returns the section whose item tree contains target (at any depth),
// or nil if target is not on the board.
func (b *Board) sectionOf(target *Item) *Section {
	var contains func(items []*Item) bool
	contains = func(items []*Item) bool {
		for _, it := range items {
			if it == target || contains(it.Children) {
				return true
			}
		}
		return false
	}
	for _, s := range b.Sections {
		if contains(s.Items) {
			return s
		}
	}
	return nil
}

// shiftLeft reduces the leading indentation of it and its whole subtree by n
// characters (clamped at 0). Moving a nested item into another section this way
// re-roots it as a clean top-level entry. For a top-level item (n == 0) it is a
// no-op, so archived content keeps its exact verbatim spacing.
func shiftLeft(it *Item, n int) {
	if n <= 0 {
		return
	}
	if it.parsed {
		if len(it.indent) >= n {
			it.indent = it.indent[n:]
		} else {
			it.indent = ""
		}
	} else {
		lead := len(it.raw) - len(strings.TrimLeft(it.raw, " \t"))
		if lead > n {
			lead = n
		}
		it.raw = it.raw[lead:]
	}
	for _, c := range it.Children {
		shiftLeft(c, n)
	}
}

// archiveSection returns the Archive section, creating it on first use just
// above Log (or at the end of the board if there is no Log).
func (b *Board) archiveSection() *Section {
	return b.AddSection(ArchiveTitle)
}

// ArchiveItem moves target — and its whole subtree (replies + verbatim
// continuation lines) — to the end of the Archive section, preserving its text
// and status exactly. Archive is created on first use. Returns false if target
// is not on the board or already lives in Archive (a no-op).
func (b *Board) ArchiveItem(target *Item) bool {
	if target == nil {
		return false
	}
	if sec := b.sectionOf(target); sec == nil || sec.Title == ArchiveTitle {
		return false
	}
	d := target.depth()
	if !b.Remove(target) {
		return false
	}
	shiftLeft(target, d)
	b.archiveSection().insert(target)
	return true
}

// ArchiveSection moves every item of sec to the end of Archive and removes the
// now-empty section from the board. Blank separator lines are dropped. The
// Archive and Log sections cannot be archived (returns false), nor can a section
// that is not on the board.
func (b *Board) ArchiveSection(sec *Section) bool {
	if sec == nil || sec.Title == ArchiveTitle || sec.Title == LogTitle {
		return false
	}
	found := false
	for _, s := range b.Sections {
		if s == sec {
			found = true
			break
		}
	}
	if !found {
		return false
	}
	arch := b.archiveSection()
	for _, it := range sec.Items {
		if it.IsContinuation() && strings.TrimSpace(it.raw) == "" {
			continue // drop section-separating blank lines
		}
		shiftLeft(it, it.depth())
		arch.insert(it)
	}
	sec.Items = nil
	return b.RemoveSection(sec)
}

// RenameSection changes sec's heading text to newTitle, leaving its contents
// untouched. It refuses (returns false) when sec is not on the board, newTitle
// is empty, or another section already carries newTitle — merging two sections
// under one heading is out of scope. Renaming a section to its own current name
// is a successful no-op.
func (b *Board) RenameSection(sec *Section, newTitle string) bool {
	newTitle = strings.TrimSpace(newTitle)
	if sec == nil || newTitle == "" {
		return false
	}
	found := false
	for _, s := range b.Sections {
		if s == sec {
			found = true
			continue
		}
		if s.Title == newTitle {
			return false // would collide with (merge into) an existing section
		}
	}
	if !found {
		return false
	}
	sec.Title = newTitle
	return true
}

// RemoveSection hard-deletes sec (header + all contents) from the board.
// Returns false if sec is not on the board.
func (b *Board) RemoveSection(sec *Section) bool {
	for i, s := range b.Sections {
		if s == sec {
			b.Sections = append(b.Sections[:i], b.Sections[i+1:]...)
			return true
		}
	}
	return false
}

// ArchivedCount reports the number of top-level archived entries (used by the
// TUI to render the collapsed Archive header, e.g. "(12 archived)").
func (b *Board) ArchivedCount() int {
	s := b.Section(ArchiveTitle)
	if s == nil {
		return 0
	}
	n := 0
	for _, it := range s.Items {
		if it.IsItem() {
			n++
		}
	}
	return n
}
