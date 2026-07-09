package board

import "testing"

func TestTitleParse(t *testing.T) {
	src := `---
session: abc-123
cwd: /home/nitsan/code/foo
started: 2026-07-06T21:30:00+03:00
title: auth refactor
---

## Plan
`
	b := Parse(src)
	if b.Frontmatter.Title != "auth refactor" {
		t.Fatalf("Title = %q, want %q", b.Frontmatter.Title, "auth refactor")
	}
}

func TestTitleRoundTrip(t *testing.T) {
	// A board carrying a title must reproduce byte-for-byte (title rendered in
	// canonical position: after started).
	src := `---
session: abc-123
cwd: /home/nitsan/code/foo
started: 2026-07-06T21:30:00+03:00
title: auth refactor
extra-key: kept verbatim
---

## Plan
- [ ] step one
`
	if got := Parse(src).Render(); got != src {
		t.Fatalf("round trip mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, src)
	}
}

func TestTitleSetRoundTrip(t *testing.T) {
	// Setting the title on a title-less board renders it and survives a reparse.
	src := `---
session: abc-123
cwd: /home/nitsan/code/foo
started: 2026-07-06T21:30:00+03:00
---

## Plan
`
	b := Parse(src)
	b.Frontmatter.Title = "quick fix"
	rendered := b.Render()
	if Parse(rendered).Frontmatter.Title != "quick fix" {
		t.Fatalf("title did not survive reparse; rendered:\n%s", rendered)
	}
	// Idempotent thereafter.
	if got := Parse(rendered).Render(); got != rendered {
		t.Fatalf("second round trip not stable:\n%s\nvs\n%s", got, rendered)
	}
}

func TestNoTitleUnchanged(t *testing.T) {
	// Boards without a title are unaffected (no spurious title line).
	src := `---
session: abc-123
cwd: /home/nitsan/code/foo
started: 2026-07-06T21:30:00+03:00
---

## Plan
`
	if got := Parse(src).Render(); got != src {
		t.Fatalf("title-less round trip mismatch:\n%s", got)
	}
}

func TestRenameSection(t *testing.T) {
	b := Parse("## Threads\n- [ ] a\n\n## Ideas\n- [ ] b\n")
	sec := b.Section("Threads")

	if !b.RenameSection(sec, "Discussion") {
		t.Fatalf("rename returned false")
	}
	if b.Section("Discussion") == nil || b.Section("Threads") != nil {
		t.Fatalf("rename did not take: %+v", b.Sections)
	}
	if len(b.Section("Discussion").Items) == 0 || b.Section("Discussion").Items[0].Text != "a" {
		t.Errorf("contents lost")
	}
	// Duplicate name refused.
	if b.RenameSection(b.Section("Discussion"), "Ideas") {
		t.Errorf("rename onto existing section should be refused")
	}
	// Empty name refused; nil refused; not-on-board refused.
	if b.RenameSection(sec, "  ") {
		t.Errorf("empty name should be refused")
	}
	if b.RenameSection(nil, "X") {
		t.Errorf("nil section should be refused")
	}
	if b.RenameSection(&Section{Title: "Ghost"}, "X") {
		t.Errorf("section not on board should be refused")
	}
	// No-op rename to same title succeeds.
	if !b.RenameSection(b.Section("Ideas"), "Ideas") {
		t.Errorf("no-op rename should succeed")
	}
}
