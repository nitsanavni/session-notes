package board

import (
	"fmt"
	"strings"
)

// Scope carve-out: a subtree rooted at a node id becomes a Claude session's (or
// sub-agent's) whole world. Addressing resolves within the root only, and edits
// that would land outside it are refused. This is the single enforcement point
// (op resolution) the CLI `edit --root` and `watch --node` build on.

// ResolveRoot resolves rootID to the section and/or item it addresses. An empty
// rootID is the whole board (sec and item both nil, ok true). A rootID that
// matches nothing is ok false.
func (b *Board) ResolveRoot(rootID string) (sec *Section, item *Item, ok bool) {
	if rootID == "" {
		return nil, nil, true
	}
	if s := b.SectionByID(rootID); s != nil {
		return s, nil, true
	}
	if it := b.FindByID(rootID); it != nil {
		return nil, it, true
	}
	return nil, nil, false
}

// InScope reports whether item it lies within the subtree rooted at rootID.
// Empty rootID scopes the whole board. A section root contains every item under
// that section at any depth; an item root contains itself and its descendants.
func (b *Board) InScope(rootID string, it *Item) bool {
	if rootID == "" {
		return true
	}
	sec, root, ok := b.ResolveRoot(rootID)
	if !ok || it == nil {
		return false
	}
	if sec != nil {
		return sectionContains(sec, it)
	}
	return itemContains(root, it)
}

// sectionContains reports whether target is one of sec's items at any depth.
func sectionContains(sec *Section, target *Item) bool {
	var walk func(items []*Item) bool
	walk = func(items []*Item) bool {
		for _, it := range items {
			if it == target || walk(it.Children) {
				return true
			}
		}
		return false
	}
	return walk(sec.Items)
}

// itemContains reports whether target is root or a descendant of root.
func itemContains(root, target *Item) bool {
	if root == target {
		return true
	}
	var walk func(items []*Item) bool
	walk = func(items []*Item) bool {
		for _, it := range items {
			if it == target || walk(it.Children) {
				return true
			}
		}
		return false
	}
	return walk(root.Children)
}

// SubtreeSource renders the on-disk source lines of the subtree rooted at id
// from content, in document order. ok is false when the node is absent (so a
// watcher can report "node gone"). Empty id returns the whole board body. The
// lines are the verbatim raw lines, so a multiset diff of two SubtreeSource
// results yields exactly the watch +/- output scoped to the subtree.
func SubtreeSource(content, id string) (string, bool) {
	b := Parse(content)
	var lines []string
	var emit func(items []*Item)
	emit = func(items []*Item) {
		for _, it := range items {
			lines = append(lines, it.rawLine())
			emit(it.Children)
		}
	}
	if id == "" {
		for _, s := range b.Sections {
			emit(s.Items)
		}
		return strings.Join(lines, "\n"), true
	}
	if s := b.SectionByID(id); s != nil {
		emit(s.Items)
		return strings.Join(lines, "\n"), true
	}
	if it := b.FindByID(id); it != nil {
		lines = append(lines, it.rawLine())
		emit(it.Children)
		return strings.Join(lines, "\n"), true
	}
	return "", false
}

// rawLine returns the item's on-disk source line: the preserved raw for
// continuation lines, or a re-rendered bullet for parsed items.
func (it *Item) rawLine() string {
	if !it.parsed {
		return it.raw
	}
	return it.render()
}

// errOutsideScope is returned when an op addresses a node outside its --root
// carve-out. It wraps ErrOpInvalid so callers already handling invalid ops keep
// their exit codes.
func errOutsideScope(rootID string) error {
	return fmt.Errorf("%w: target is outside the subtree rooted at %q", ErrOpInvalid, rootID)
}
