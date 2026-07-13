package board

import (
	"errors"
	"fmt"
	"strings"
)

// The op layer: every board mutation as a named, uniformly-addressed
// operation. The web server and the edit CLI dispatch through Apply, so
// "reply", "archive-section", and friends mean exactly one thing everywhere;
// a new client (or a new op) plugs in here instead of re-deriving semantics.
// The TUI is the deliberate exception: its rebase machinery replays pending
// mutations onto freshly-parsed trees, which needs the finer-grained board
// methods Apply itself is built from.

// Op is one named mutation. Items are addressed EITHER by Section+Raw (the
// verbatim source line, with a normalized-text fallback for cosmetic drift —
// the web client's form) or by Query (substring over the whole board — the
// CLI's form). Section ops address by Section alone.
type Op struct {
	Name    string // add | reply | fork | edit | status | urgent | pin | archive | delete | log | title | add-section | rename-section | archive-section | delete-section
	ID      string // node-id addressing (preferred); falls back to Section+Raw / Query
	Section string
	Raw     string
	Query   string
	Text    string
	Status  Status // for Name == "status"
	Urgent  bool   // for Name == "urgent": the desired state (set, not toggle)
	Pinned  bool   // for Name == "pin": the desired state (set, not toggle)
	Author  string // for Name == "log"; empty means "user"
}

var (
	// ErrOpNotFound: the addressed item or section is gone (board changed?).
	// Web maps it to 409 so the client refetches; the CLI prints and exits 2.
	ErrOpNotFound = errors.New("not found")
	// ErrOpInvalid: the op itself is malformed (unknown name, empty text,
	// duplicate section title, guarded section, …).
	ErrOpInvalid = errors.New("invalid op")
)

// Apply executes op against b (in memory — the caller owns the locked write
// around it). note is a non-fatal advisory to surface to the user, e.g.
// "3 items match; using the first" for Query addressing.
func Apply(b *Board, op Op) (note string, err error) {
	locate := func() (*Item, error) {
		if op.ID != "" {
			if it := b.FindByID(op.ID); it != nil {
				return it, nil
			}
			return nil, fmt.Errorf("%w: no item with id %q", ErrOpNotFound, op.ID)
		}
		if op.Query != "" {
			it, n := b.FindByQuery(op.Query)
			if n == 0 {
				return nil, fmt.Errorf("%w: no item matching %q", ErrOpNotFound, op.Query)
			}
			if n > 1 {
				note = fmt.Sprintf("%d items match %q; using the first", n, op.Query)
			}
			return it, nil
		}
		if it := b.FindByRawInSection(op.Section, op.Raw); it != nil {
			return it, nil
		}
		if it, n := b.FindByTextInSection(op.Section, NormalizeItemText(op.Raw)); n == 1 {
			return it, nil
		}
		return nil, fmt.Errorf("%w: item not found (board changed?)", ErrOpNotFound)
	}
	needText := func() error {
		if strings.TrimSpace(op.Text) == "" {
			return fmt.Errorf("%w: empty text", ErrOpInvalid)
		}
		return nil
	}

	switch op.Name {
	case "add":
		if b.Section(op.Section) == nil {
			return "", fmt.Errorf("%w: no section %q; sections: %s", ErrOpNotFound, op.Section, strings.Join(SectionTitles(b), ", "))
		}
		if err := needText(); err != nil {
			return "", err
		}
		// A leading status marker in the text ("[>] foo") sets the item's
		// status instead of ending up as literal text after the default one.
		it := b.AddItem(op.Section, op.Text)
		if st, remainder, ok := LeadingStatus(op.Text); ok {
			it.Status = st
			it.Text = remainder
		}
	case "reply":
		// In-thread semantics everywhere: flat conversations (ReplyFlat).
		it, lerr := locate()
		if lerr != nil {
			return note, lerr
		}
		if err := needText(); err != nil {
			return note, err
		}
		b.ReplyFlat(it, op.Text)
	case "fork":
		it, lerr := locate()
		if lerr != nil {
			return note, lerr
		}
		if err := needText(); err != nil {
			return note, err
		}
		b.AddReply(it, op.Text)
	case "edit":
		it, lerr := locate()
		if lerr != nil {
			return note, lerr
		}
		if err := needText(); err != nil {
			return note, err
		}
		it.Text = op.Text
	case "status":
		it, lerr := locate()
		if lerr != nil {
			return note, lerr
		}
		it.SetStatus(op.Status)
	case "urgent":
		it, lerr := locate()
		if lerr != nil {
			return note, lerr
		}
		if it.Urgent != op.Urgent {
			it.ToggleUrgent()
		}
	case "pin":
		it, lerr := locate()
		if lerr != nil {
			return note, lerr
		}
		if it.Pinned != op.Pinned {
			it.TogglePinned()
		}
	case "archive":
		it, lerr := locate()
		if lerr != nil {
			return note, lerr
		}
		if !b.ArchiveItem(it) {
			return note, fmt.Errorf("%w: item not found (board changed?)", ErrOpNotFound)
		}
	case "delete":
		it, lerr := locate()
		if lerr != nil {
			return note, lerr
		}
		if !b.Remove(it) {
			return note, fmt.Errorf("%w: item not found (board changed?)", ErrOpNotFound)
		}
	case "log":
		if err := needText(); err != nil {
			return "", err
		}
		author := op.Author
		if author == "" {
			author = "user"
		}
		b.AppendLog(author, op.Text)
	case "title":
		b.SetTitle(op.Text)
	case "add-section":
		title := strings.TrimSpace(op.Text)
		if title == "" {
			return "", fmt.Errorf("%w: empty section title", ErrOpInvalid)
		}
		if b.Section(title) != nil {
			return "", fmt.Errorf("%w: section %q already exists", ErrOpInvalid, title)
		}
		b.AddSection(title)
	case "rename-section":
		sec := b.Section(op.Section)
		if sec == nil {
			return "", fmt.Errorf("%w: no section %q", ErrOpNotFound, op.Section)
		}
		if !b.RenameSection(sec, strings.TrimSpace(op.Text)) {
			return "", fmt.Errorf("%w: cannot rename to %q (empty or duplicate title)", ErrOpInvalid, op.Text)
		}
	case "archive-section":
		sec := b.Section(op.Section)
		if sec == nil {
			return "", fmt.Errorf("%w: no section %q", ErrOpNotFound, op.Section)
		}
		if !b.ArchiveSection(sec) {
			return "", fmt.Errorf("%w: the %s section cannot be archived", ErrOpInvalid, op.Section)
		}
	case "delete-section":
		sec := b.Section(op.Section)
		if sec == nil {
			return "", fmt.Errorf("%w: no section %q", ErrOpNotFound, op.Section)
		}
		b.RemoveSection(sec)
	default:
		return "", fmt.Errorf("%w: unknown op %q", ErrOpInvalid, op.Name)
	}
	return note, nil
}

// SectionTitles lists b's section headings in order (for error messages).
func SectionTitles(b *Board) []string {
	out := make([]string, 0, len(b.Sections))
	for _, s := range b.Sections {
		out = append(out, s.Title)
	}
	return out
}

// LeadingStatus parses an explicit "[ ]/[>]/[x]/[?]" marker at the start of
// an add's text, returning the status and the text without it.
func LeadingStatus(text string) (Status, string, bool) {
	if len(text) < 4 || text[0] != '[' || text[2] != ']' || text[3] != ' ' {
		return 0, "", false
	}
	var st Status
	switch text[1] {
	case ' ':
		st = StatusOpen
	case '>':
		st = StatusInProgress
	case 'x', 'X':
		st = StatusDone
	case '?':
		st = StatusBlocked
	default:
		return 0, "", false
	}
	return st, text[4:], true
}
