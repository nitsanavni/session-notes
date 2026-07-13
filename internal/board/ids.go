package board

import (
	"crypto/rand"
)

// idAlphabet is the base36 alphabet (lowercase a-z0-9) node ids are drawn from.
const idAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// idLen is the fixed length of a generated node id. 6 base36 chars ≈ 2.2e9
// combinations — greppable, human-tolerable, collision-checked per board.
const idLen = 6

// genID returns a fresh idLen-char base36 id not present in used, and records it
// in used so a single assignment pass never collides with itself.
func genID(used map[string]bool) string {
	buf := make([]byte, idLen)
	for {
		if _, err := rand.Read(buf); err != nil {
			// crypto/rand should never fail; if it does, fall back to a
			// deterministic-but-unique-enough value rather than panicking mid-save.
			for i := range buf {
				buf[i] = byte(len(used) + i)
			}
		}
		id := make([]byte, idLen)
		for i, b := range buf {
			id[i] = idAlphabet[int(b)%len(idAlphabet)]
		}
		s := string(id)
		if !used[s] {
			used[s] = true
			return s
		}
	}
}

// EnsureIDs assigns a stable node id to every section and recognized item that
// lacks one, collision-checked against the ids already present on the board.
// It is the lazy, write-driven assignment step: parsing never mutates, so a
// board keeps its exact bytes until the first programmatic save runs EnsureIDs.
// Continuation (raw, non-bullet) lines never get ids — only nodes do. Returns
// true if it assigned at least one new id.
func (b *Board) EnsureIDs() bool {
	used := map[string]bool{}
	var collect func(items []*Item)
	collect = func(items []*Item) {
		for _, it := range items {
			if it.parsed && it.ID != "" {
				used[it.ID] = true
			}
			collect(it.Children)
		}
	}
	for _, s := range b.Sections {
		if s.ID != "" {
			used[s.ID] = true
		}
		collect(s.Items)
	}

	changed := false
	var assign func(items []*Item)
	assign = func(items []*Item) {
		for _, it := range items {
			if it.parsed && it.ID == "" {
				it.ID = genID(used)
				changed = true
			}
			assign(it.Children)
		}
	}
	for _, s := range b.Sections {
		if s.ID == "" {
			s.ID = genID(used)
			changed = true
		}
		assign(s.Items)
	}
	return changed
}

// FindByID returns the recognized item anywhere on the board (any section, any
// depth) whose node id equals id, or nil. An empty id never matches.
func (b *Board) FindByID(id string) *Item {
	if id == "" {
		return nil
	}
	var found *Item
	var walk func(items []*Item)
	walk = func(items []*Item) {
		for _, it := range items {
			if found != nil {
				return
			}
			if it.parsed && it.ID == id {
				found = it
				return
			}
			walk(it.Children)
		}
	}
	for _, s := range b.Sections {
		walk(s.Items)
		if found != nil {
			break
		}
	}
	return found
}

// SectionByID returns the section whose heading anchor equals id, or nil. An
// empty id never matches.
func (b *Board) SectionByID(id string) *Section {
	if id == "" {
		return nil
	}
	for _, s := range b.Sections {
		if s.ID == id {
			return s
		}
	}
	return nil
}
