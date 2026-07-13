package board

import "fmt"

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

// errOutsideScope is returned when an op addresses a node outside its --root
// carve-out. It wraps ErrOpInvalid so callers already handling invalid ops keep
// their exit codes.
func errOutsideScope(rootID string) error {
	return fmt.Errorf("%w: target is outside the subtree rooted at %q", ErrOpInvalid, rootID)
}
