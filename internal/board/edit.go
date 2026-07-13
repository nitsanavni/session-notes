package board

import (
	"os"
	"strings"
)

// EditUnderLock runs a locked read-modify-write on the board file at path: it
// acquires the board's exclusive advisory lock (WithLock), reads the current
// file content, hands it to transform, writes the returned content atomically
// (temp file + rename), and — if snapshot is non-empty — copies the freshly
// written content over snapshot before releasing the lock. That snapshot copy
// is the monitor self-edit-suppression hook: a watcher that also takes the lock
// around its compare-and-snapshot cycle then never sees this write as an
// external change.
//
// It is the single serialization point for the edit CLI's writes, reusing the
// same lock and atomic-rename the TUI and hooks use rather than hand-rolling a
// second flock protocol. transform runs entirely inside the lock; returning an
// error from it aborts the write, leaving the board untouched.
func EditUnderLock(path, snapshot string, transform func(content string) (string, error)) error {
	return WithLock(path, func() error {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out, err := transform(string(data))
		if err != nil {
			return err
		}
		if err := writeAtomicRaw(path, out); err != nil {
			return err
		}
		if snapshot != "" {
			if err := writeAtomicRaw(snapshot, out); err != nil {
				return err
			}
		}
		return nil
	})
}

// WatchSnapshotPath is the default monitor snapshot sidecar for a board —
// "<board>.watch". It is a shared convention, not just a watch-CLI default:
// the web server reads this exact path to derive per-item delivery receipts
// (a change is "delivered" once the snapshot includes it, because watch and
// edit --refresh-snapshot only advance the snapshot when the change has been
// printed to the agent).
func WatchSnapshotPath(boardPath string) string {
	return boardPath + ".watch"
}

// FindByQuery returns the first recognized item anywhere on the board (any
// section, any nesting depth, document order) whose source line (Raw) or
// display Text contains query as a substring, along with the total count of
// matching items. A count of 0 means no match; >1 means the caller acted on the
// first of several — useful for a "matched N, using the first" notice. query is
// matched case-sensitively.
func (b *Board) FindByQuery(query string) (*Item, int) {
	var found *Item
	count := 0
	var walk func(items []*Item)
	walk = func(items []*Item) {
		for _, it := range items {
			if it.parsed && (strings.Contains(it.raw, query) || strings.Contains(it.Text, query)) {
				if found == nil {
					found = it
				}
				count++
			}
			walk(it.Children)
		}
	}
	for _, s := range b.Sections {
		walk(s.Items)
	}
	return found, count
}

// FindByQueryScoped is FindByQuery confined to the subtree rooted at rootID
// (empty rootID = whole board). Matches outside the carve-out are invisible, so
// a sub-agent handed --root cannot even address a sibling by substring.
func (b *Board) FindByQueryScoped(query, rootID string) (*Item, int) {
	if rootID == "" {
		return b.FindByQuery(query)
	}
	var found *Item
	count := 0
	var walk func(items []*Item)
	walk = func(items []*Item) {
		for _, it := range items {
			if it.parsed && (strings.Contains(it.raw, query) || strings.Contains(it.Text, query)) {
				if found == nil {
					found = it
				}
				count++
			}
			walk(it.Children)
		}
	}
	if sec, item, ok := b.ResolveRoot(rootID); ok {
		switch {
		case sec != nil:
			walk(sec.Items)
		case item != nil:
			if strings.Contains(item.raw, query) || strings.Contains(item.Text, query) {
				found = item
				count++
			}
			walk(item.Children)
		}
	}
	return found, count
}

// SetStatus sets a recognized item's checkbox state. Plain (raw, non-bullet)
// lines are left untouched.
func (it *Item) SetStatus(s Status) {
	if it.parsed {
		it.Status = s
	}
}
