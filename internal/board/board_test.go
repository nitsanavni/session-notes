package board

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRoundTripLossless(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{"canonical board", `---
session: abc-123
cwd: /home/nitsan/code/foo
started: 2026-07-06T21:30:00+03:00
---

## Plan
- [ ] step one

## Threads
- [>] auth refactor — extracting middleware
- [x] fix flaky test

## Questions
- [ ] !! drop the legacy endpoint? @user

## Ideas
- cache invalidation could be event-driven

## Log
- 21:30 start: session started in /home/nitsan/code/foo
- 21:42 claude: finished thread "fix flaky test"
`},
		{"weird lines preserved", `---
session: x
custom-key: kept verbatim
---
stray preamble text

## Plan
- [ ] normal item
  some continuation line that is not a bullet
	tab-indented raw line
- [?] blocked item
* asterisk bullet (not ours)
> a blockquote

## Nested
- [ ] parent
  - [x] indented child
- plain bullet no checkbox

random trailing text
`},
		{"no frontmatter", `## Plan
- [ ] a
- [X] uppercase X kept as [x]? no — see below
`},
		{"empty file", ""},
		{"only preamble", "just some text\nno sections at all\n"},
		{"blank lines between sections", `## A
- [ ] one


## B

- [>] two
`},
		{"no trailing newline", "## Plan\n- [ ] a"},
		{"urgent variants", `## Q
- [ ] !! urgent open
- [x] !! urgent done
- !! plain urgent
- text with !! in the middle stays
`},
		{"threaded replies", `## Questions
- [ ] drop the legacy endpoint? @user
  - user: yes let's drop it
  - claude: done in abc123
- [>] next thread
`},
		{"reply with checkbox", `## Threads
- [>] parent thread
  - [ ] claude: sub-task to do
  - user: looks good
`},
		{"deep nesting", `## Threads
- [ ] a
  - b: one
    - c: two
      - d: three
- [ ] e
`},
		{"nested with blank between threads", `## Questions
- [ ] q1
  - user: r1
  - claude: r2

- [ ] q2
  - user: r3
`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Parse(tc.src).Render()
			want := tc.src
			// Render normalizes a missing trailing newline; also "[X]" is
			// normalized to "[x]". Those are the only permitted deltas.
			if want != "" && !strings.HasSuffix(want, "\n") {
				want += "\n"
			}
			want = strings.ReplaceAll(want, "[X]", "[x]")
			if got != want {
				t.Errorf("round trip mismatch\n--- want ---\n%q\n--- got ---\n%q", want, got)
			}
		})
	}
}

func TestParseItems(t *testing.T) {
	b := Parse(`## Threads
- [ ] open one
- [>] in progress
- [x] done thing
- [?] blocked thing
- plain bullet
- [ ] !! urgent thing @user
not an item
`)
	s := b.Section("Threads")
	if s == nil {
		t.Fatal("missing Threads section")
	}
	wants := []struct {
		status Status
		urgent bool
		text   string
		isItem bool
	}{
		{StatusOpen, false, "open one", true},
		{StatusInProgress, false, "in progress", true},
		{StatusDone, false, "done thing", true},
		{StatusBlocked, false, "blocked thing", true},
		{StatusNone, false, "plain bullet", true},
		{StatusOpen, true, "urgent thing @user", true},
		{StatusNone, false, "not an item", false},
	}
	if len(s.Items) != len(wants) {
		t.Fatalf("got %d items, want %d", len(s.Items), len(wants))
	}
	for i, w := range wants {
		it := s.Items[i]
		if it.IsItem() != w.isItem {
			t.Errorf("item %d: IsItem=%v want %v", i, it.IsItem(), w.isItem)
			continue
		}
		if !w.isItem {
			continue
		}
		if it.Status != w.status || it.Urgent != w.urgent || it.Text != w.text {
			t.Errorf("item %d: got (%v, %v, %q), want (%v, %v, %q)",
				i, it.Status, it.Urgent, it.Text, w.status, w.urgent, w.text)
		}
	}
}

func TestCycleStatus(t *testing.T) {
	cases := []struct {
		from, to Status
	}{
		{StatusOpen, StatusInProgress},
		{StatusInProgress, StatusDone},
		{StatusDone, StatusOpen},
		{StatusBlocked, StatusOpen},
		{StatusNone, StatusNone},
	}
	for _, tc := range cases {
		it := &Item{parsed: true, Status: tc.from, Text: "x"}
		it.CycleStatus()
		if it.Status != tc.to {
			t.Errorf("cycle from %v: got %v, want %v", tc.from, it.Status, tc.to)
		}
	}
	// And the rendered line reflects the new status.
	b := Parse("## Plan\n- [ ] step\n")
	b.Section("Plan").Items[0].CycleStatus()
	if got, want := b.Render(), "## Plan\n- [>] step\n"; got != want {
		t.Errorf("render after cycle: got %q, want %q", got, want)
	}
}

func TestUrgentDetection(t *testing.T) {
	b := Parse(`## Questions
- [ ] !! drop legacy endpoint? @user
- [x] !! already answered
- [>] !! working on it
- [ ] not urgent
- !! plain urgent idea

## Plan
- [?] !! blocked urgent
`)
	urgent := b.UrgentOpenItems()
	var texts []string
	for _, it := range urgent {
		texts = append(texts, it.Text)
	}
	want := []string{
		"drop legacy endpoint? @user",
		"working on it",
		"plain urgent idea",
		"blocked urgent",
	}
	if len(texts) != len(want) {
		t.Fatalf("got %d urgent items %v, want %d", len(texts), texts, len(want))
	}
	for i := range want {
		if texts[i] != want[i] {
			t.Errorf("urgent[%d] = %q, want %q", i, texts[i], want[i])
		}
	}

	// Toggling urgency off, or checking the box, acknowledges the item.
	b.Section("Questions").Items[0].ToggleUrgent()
	b.Section("Questions").Items[2].Status = StatusDone
	if got := len(b.UrgentOpenItems()); got != 2 {
		t.Errorf("after acknowledge: %d urgent, want 2", got)
	}
	if strings.Contains(b.Render(), "!! drop legacy") {
		t.Error("!! not removed from rendered text after ToggleUrgent")
	}
}

func TestAppendLog(t *testing.T) {
	ts := time.Date(2026, 7, 6, 9, 5, 0, 0, time.UTC)

	t.Run("existing section with trailing blank", func(t *testing.T) {
		b := Parse("## Log\n- 08:00 start: hi\n\n## Ideas\n- x\n")
		b.AppendLogAt(ts, "claude", "did a thing")
		want := "## Log\n- 08:00 start: hi\n- 09:05 claude: did a thing\n\n## Ideas\n- x\n"
		if got := b.Render(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("missing section is created", func(t *testing.T) {
		b := Parse("## Plan\n- [ ] a\n")
		b.AppendLogAt(ts, "end", "session ended")
		got := b.Render()
		if !strings.Contains(got, "## Log\n- 09:05 end: session ended\n") {
			t.Errorf("Log section not created properly:\n%s", got)
		}
	})

	t.Run("zero-padded time", func(t *testing.T) {
		b := Parse("## Log\n")
		b.AppendLogAt(time.Date(2026, 1, 1, 7, 3, 0, 0, time.UTC), "u", "t")
		if !strings.Contains(b.Render(), "- 07:03 u: t") {
			t.Errorf("time not zero padded: %q", b.Render())
		}
	})
}

func TestAddItem(t *testing.T) {
	b := Parse("## Plan\n- [ ] a\n\n## Ideas\n\n## Log\n")
	b.AddItem("Plan", "new step")
	b.AddItem("Ideas", "an idea")
	b.AddItem("Brand New", "hello")
	got := b.Render()
	for _, want := range []string{
		"- [ ] a\n- [ ] new step\n\n",   // inserted before section-separating blank
		"## Ideas\n\n- an idea\n",       // first item: blank line kept after header
		"## Brand New\n\n- [ ] hello\n", // section created, first item spaced off header
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// TestAddItemFirstItemBlankLine locks the readability rule: after a new section
// is created and its first item added, a blank line separates the "## " header
// from that item. Subsequent items pack directly under it, and the resulting
// format is stable under a Parse→Render round-trip.
func TestAddItemFirstItemBlankLine(t *testing.T) {
	t.Run("new section then first item", func(t *testing.T) {
		b := Parse("## Plan\n- [ ] a\n")
		b.AddSection("Ideas")
		b.AddItem("Ideas", "first thought")
		want := "## Plan\n- [ ] a\n\n## Ideas\n\n- first thought\n"
		if got := b.Render(); got != want {
			t.Errorf("\n--- got ---\n%q\n--- want ---\n%q", got, want)
		}
	})

	t.Run("second item packs under the first", func(t *testing.T) {
		b := Parse("## Plan\n- [ ] a\n")
		b.AddSection("Ideas")
		b.AddItem("Ideas", "one")
		b.AddItem("Ideas", "two")
		want := "## Plan\n- [ ] a\n\n## Ideas\n\n- one\n- two\n"
		if got := b.Render(); got != want {
			t.Errorf("\n--- got ---\n%q\n--- want ---\n%q", got, want)
		}
	})

	t.Run("blank-separated format is idempotent", func(t *testing.T) {
		b := Parse("## Plan\n- [ ] a\n")
		b.AddSection("Ideas")
		b.AddItem("Ideas", "first thought")
		once := b.Render()
		twice := Parse(once).Render()
		if once != twice {
			t.Errorf("format not stable across round-trip\n--- once ---\n%q\n--- twice ---\n%q", once, twice)
		}
	})
}

func TestAddSection(t *testing.T) {
	cases := []struct {
		name  string
		src   string
		title string
		want  string
	}{
		{
			name:  "insert before Log",
			src:   "## Plan\n- [ ] a\n\n## Log\n- 08:00 start: hi\n",
			title: "Ideas",
			want:  "## Plan\n- [ ] a\n\n## Ideas\n\n## Log\n- 08:00 start: hi\n",
		},
		{
			name:  "append at end when no Log",
			src:   "## Plan\n- [ ] a\n",
			title: "Ideas",
			want:  "## Plan\n- [ ] a\n\n## Ideas\n",
		},
		{
			name:  "adding Log goes at the very end",
			src:   "## Plan\n- [ ] a\n",
			title: "Log",
			want:  "## Plan\n- [ ] a\n\n## Log\n",
		},
		{
			name:  "empty board",
			src:   "",
			title: "Plan",
			want:  "## Plan\n",
		},
		{
			name:  "custom title before Log among many",
			src:   "## Plan\n\n## Threads\n\n## Log\n- 08:00 start: hi\n",
			title: "Design",
			want:  "## Plan\n\n## Threads\n\n## Design\n\n## Log\n- 08:00 start: hi\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := Parse(tc.src)
			b.AddSection(tc.title)
			if got := b.Render(); got != tc.want {
				t.Errorf("AddSection(%q)\n--- got ---\n%q\n--- want ---\n%q", tc.title, got, tc.want)
			}
		})
	}
}

func TestAddSectionExistingIsNoOp(t *testing.T) {
	b := Parse("## Plan\n- [ ] a\n\n## Log\n- 08:00 start: hi\n")
	before := b.Render()
	s := b.AddSection("Plan")
	if s == nil || s.Title != "Plan" {
		t.Fatalf("AddSection returned %+v", s)
	}
	if got := b.Render(); got != before {
		t.Errorf("adding existing section changed board:\n%q", got)
	}
	if n := len(b.Sections); n != 2 {
		t.Errorf("got %d sections, want 2 (no duplicate)", n)
	}
}

func TestFrontmatter(t *testing.T) {
	b := Parse(`---
session: abc
cwd: /tmp/x
started: 2026-07-06T10:00:00Z
extra: kept
---
## Plan
`)
	fm := b.Frontmatter
	if fm.Session != "abc" || fm.Cwd != "/tmp/x" || fm.Started != "2026-07-06T10:00:00Z" {
		t.Errorf("frontmatter = %+v", fm)
	}
	if !strings.Contains(b.Render(), "extra: kept") {
		t.Error("unknown frontmatter key dropped")
	}
}

func TestAtomicSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "b.md")
	src := "## Plan\n- [ ] a\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	b.Section("Plan").Items[0].CycleStatus()
	if err := b.Save(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "## Plan\n- [>] a\n" {
		t.Errorf("saved content: %q", data)
	}
	// No temp files left behind. The board and its sidecar lock file are the only
	// expected entries (the lock is created by WithLock and intentionally kept).
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	want := map[string]bool{"b.md": true, "b.md.lock": true}
	for _, n := range names {
		if !want[n] {
			t.Errorf("unexpected leftover file %q (all: %v)", n, names)
		}
	}
	if len(names) != len(want) {
		t.Errorf("dir entries = %v, want board + lock only", names)
	}
}

func TestDeleteItem(t *testing.T) {
	b := Parse("## Plan\n- [ ] a\n- [ ] b\n")
	s := b.Section("Plan")
	if !s.DeleteItem(0) {
		t.Fatal("delete failed")
	}
	if got := b.Render(); got != "## Plan\n- [ ] b\n" {
		t.Errorf("got %q", got)
	}
	if s.DeleteItem(5) {
		t.Error("out-of-range delete should return false")
	}
}

func TestParseTree(t *testing.T) {
	b := Parse(`## Questions
- [ ] drop the legacy endpoint? @user
  - user: yes let's drop it
  - claude: done in abc123
    - user: thanks
- [>] unrelated
`)
	s := b.Section("Questions")
	if len(s.Items) != 2 {
		t.Fatalf("got %d top-level items, want 2", len(s.Items))
	}
	parent := s.Items[0]
	if parent.Text != "drop the legacy endpoint? @user" {
		t.Fatalf("parent text = %q", parent.Text)
	}
	if len(parent.Children) != 2 {
		t.Fatalf("parent has %d children, want 2", len(parent.Children))
	}
	if got := parent.Children[0].Text; got != "user: yes let's drop it" {
		t.Errorf("child[0] = %q", got)
	}
	claude := parent.Children[1]
	if claude.Text != "claude: done in abc123" {
		t.Errorf("child[1] = %q", claude.Text)
	}
	if len(claude.Children) != 1 || claude.Children[0].Text != "user: thanks" {
		t.Errorf("grandchild wrong: %+v", claude.Children)
	}
	if s.Items[1].Text != "unrelated" {
		t.Errorf("second top-level = %q", s.Items[1].Text)
	}
}

func TestAddReply(t *testing.T) {
	t.Run("into empty thread", func(t *testing.T) {
		b := Parse("## Questions\n- [ ] drop legacy endpoint? @user\n")
		parent := b.Section("Questions").Items[0]
		b.AddReply(parent, "claude: done in abc123")
		want := "## Questions\n- [ ] drop legacy endpoint? @user\n  - claude: done in abc123\n"
		if got := b.Render(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("into existing thread appends last", func(t *testing.T) {
		b := Parse("## Questions\n- [ ] q? @user\n  - user: first\n")
		parent := b.Section("Questions").Items[0]
		b.AddReply(parent, "claude: second")
		want := "## Questions\n- [ ] q? @user\n  - user: first\n  - claude: second\n"
		if got := b.Render(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("reply to a reply nests deeper", func(t *testing.T) {
		b := Parse("## Q\n- [ ] q\n  - user: r1\n")
		reply := b.Section("Q").Items[0].Children[0]
		b.AddReply(reply, "claude: r2")
		want := "## Q\n- [ ] q\n  - user: r1\n    - claude: r2\n"
		if got := b.Render(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("reply before section-separating blank", func(t *testing.T) {
		b := Parse("## Q\n- [ ] q\n\n## Log\n")
		parent := b.Section("Q").Items[0]
		b.AddReply(parent, "user: hi")
		want := "## Q\n- [ ] q\n  - user: hi\n\n## Log\n"
		if got := b.Render(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestRemoveSubtree(t *testing.T) {
	t.Run("delete parent removes children", func(t *testing.T) {
		b := Parse(`## Q
- [ ] keep me
- [ ] delete me
  - user: reply one
  - claude: reply two
    - user: nested
- [ ] keep me too
`)
		s := b.Section("Q")
		target := s.Items[1]
		if !b.Remove(target) {
			t.Fatal("remove returned false")
		}
		want := "## Q\n- [ ] keep me\n- [ ] keep me too\n"
		if got := b.Render(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("delete nested reply keeps siblings and parent", func(t *testing.T) {
		b := Parse("## Q\n- [ ] q\n  - user: a\n  - claude: b\n")
		parent := b.Section("Q").Items[0]
		if !b.Remove(parent.Children[0]) {
			t.Fatal("remove returned false")
		}
		want := "## Q\n- [ ] q\n  - claude: b\n"
		if got := b.Render(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("not found", func(t *testing.T) {
		b := Parse("## Q\n- [ ] q\n")
		if b.Remove(&Item{parsed: true, Text: "ghost"}) {
			t.Error("removing a foreign item should return false")
		}
	})
}

// TestContinuationLinesAttach verifies that lines indented deeper than a bullet
// but not starting with "- " become continuation children of that bullet (raw,
// verbatim) rather than separate top-level items. This is what lets the TUI
// treat a bullet + its continuation block as one cursor stop.
func TestContinuationLinesAttach(t *testing.T) {
	cases := []struct {
		name      string
		src       string
		wantTop   int      // top-level items in the section
		wantCont  []string // verbatim continuation lines under items[0]
		bulletTxt string   // parsed text of items[0]
	}{
		{
			name:      "plain continuation under bullet",
			src:       "## Threads\n- [>] parent\n  continuation one\n  continuation two\n- [x] sibling\n",
			wantTop:   2,
			bulletTxt: "parent",
			wantCont:  []string{"  continuation one", "  continuation two"},
		},
		{
			name: "ascii-art block is verbatim",
			src: "## Threads\n- [>] pipeline\n" +
				"      +-----+     +-----+\n" +
				"      | src | --> | dst |\n" +
				"      +-----+     +-----+\n" +
				"- [x] next\n",
			wantTop:   2,
			bulletTxt: "pipeline",
			wantCont: []string{
				"      +-----+     +-----+",
				"      | src | --> | dst |",
				"      +-----+     +-----+",
			},
		},
		{
			name:      "continuation then reply both attach",
			src:       "## Q\n- [ ] q\n  a note line\n  - user: a reply\n- [ ] other\n",
			wantTop:   2,
			bulletTxt: "q",
			wantCont:  []string{"  a note line"}, // the "- user:" is a parsed reply, not continuation
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := Parse(tc.src)
			s := b.Sections[0]
			if len(s.Items) != tc.wantTop {
				t.Fatalf("top-level items = %d, want %d", len(s.Items), tc.wantTop)
			}
			parent := s.Items[0]
			if parent.Text != tc.bulletTxt {
				t.Fatalf("items[0].Text = %q, want %q", parent.Text, tc.bulletTxt)
			}
			var gotCont []string
			for _, c := range parent.Children {
				if c.IsContinuation() {
					gotCont = append(gotCont, c.Raw())
				}
			}
			if len(gotCont) != len(tc.wantCont) {
				t.Fatalf("got %d continuation lines %q, want %q", len(gotCont), gotCont, tc.wantCont)
			}
			for i := range tc.wantCont {
				if gotCont[i] != tc.wantCont[i] {
					t.Errorf("continuation[%d] = %q, want %q", i, gotCont[i], tc.wantCont[i])
				}
			}
			// Round-trip must be byte-identical (ASCII art untouched).
			if got := b.Render(); got != tc.src {
				t.Errorf("round trip changed content\n--- want ---\n%q\n--- got ---\n%q", tc.src, got)
			}
		})
	}
}

// TestDeleteWithContinuation checks that deleting a bullet takes its whole block
// — continuation lines and reply subtree — with it, leaving siblings intact.
func TestDeleteWithContinuation(t *testing.T) {
	src := "## Threads\n" +
		"- [ ] keep\n" +
		"- [>] draw\n" +
		"      +---+\n" +
		"      | o |\n" +
		"      +---+\n" +
		"  a trailing note\n" +
		"  - user: nice\n" +
		"- [x] keep too\n"
	b := Parse(src)
	s := b.Section("Threads")
	target := s.Items[1] // the "draw" bullet with continuation + reply
	if target.Text != "draw" {
		t.Fatalf("target text = %q", target.Text)
	}
	if !b.Remove(target) {
		t.Fatal("remove returned false")
	}
	want := "## Threads\n- [ ] keep\n- [x] keep too\n"
	if got := b.Render(); got != want {
		t.Errorf("after delete\n--- got ---\n%q\n--- want ---\n%q", got, want)
	}
}

func TestArchiveItem(t *testing.T) {
	cases := []struct {
		name    string
		src     string
		pick    func(b *Board) *Item // choose the item to archive
		ok      bool
		want    string
		wantCnt int
	}{
		{
			name: "top-level item with subtree and continuations, first use creates Archive above Log",
			src: "## Threads\n" +
				"- [ ] keep\n" +
				"- [>] draw\n" +
				"      +---+\n" +
				"      | o |\n" +
				"      +---+\n" +
				"  a trailing note\n" +
				"  - user: nice\n" +
				"\n" +
				"## Log\n" +
				"- 08:00 start: hi\n",
			pick: func(b *Board) *Item { return b.Section("Threads").Items[1] },
			ok:   true,
			want: "## Threads\n" +
				"- [ ] keep\n" +
				"\n" +
				"## Archive\n" +
				"- [>] draw\n" +
				"      +---+\n" +
				"      | o |\n" +
				"      +---+\n" +
				"  a trailing note\n" +
				"  - user: nice\n" +
				"\n" +
				"## Log\n" +
				"- 08:00 start: hi\n",
			wantCnt: 1,
		},
		{
			name: "archive from nested position re-roots into Archive",
			src: "## Q\n" +
				"- [ ] parent\n" +
				"  - [>] child to archive\n" +
				"    - user: grandchild\n" +
				"  - claude: sibling stays\n",
			pick: func(b *Board) *Item { return b.Section("Q").Items[0].Children[0] },
			ok:   true,
			want: "## Q\n" +
				"- [ ] parent\n" +
				"  - claude: sibling stays\n" +
				"\n" +
				"## Archive\n" +
				"- [>] child to archive\n" +
				"  - user: grandchild\n",
			wantCnt: 1,
		},
		{
			name:    "no Log: Archive appended at end",
			src:     "## Plan\n- [ ] a\n- [ ] b\n",
			pick:    func(b *Board) *Item { return b.Section("Plan").Items[0] },
			ok:      true,
			want:    "## Plan\n- [ ] b\n\n## Archive\n- [ ] a\n",
			wantCnt: 1,
		},
		{
			name:    "second archive appends to end of existing Archive",
			src:     "## Plan\n- [ ] a\n- [ ] b\n\n## Archive\n- [x] old\n",
			pick:    func(b *Board) *Item { return b.Section("Plan").Items[1] },
			ok:      true,
			want:    "## Plan\n- [ ] a\n\n## Archive\n- [x] old\n- [ ] b\n",
			wantCnt: 2,
		},
		{
			name:    "item already in Archive is a no-op",
			src:     "## Archive\n- [x] already\n",
			pick:    func(b *Board) *Item { return b.Section("Archive").Items[0] },
			ok:      false,
			want:    "## Archive\n- [x] already\n",
			wantCnt: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := Parse(tc.src)
			if got := b.ArchiveItem(tc.pick(b)); got != tc.ok {
				t.Fatalf("ArchiveItem returned %v, want %v", got, tc.ok)
			}
			if got := b.Render(); got != tc.want {
				t.Errorf("after archive\n--- got ---\n%q\n--- want ---\n%q", got, tc.want)
			}
			if got := b.ArchivedCount(); got != tc.wantCnt {
				t.Errorf("ArchivedCount = %d, want %d", got, tc.wantCnt)
			}
		})
	}
}

func TestArchiveItemNotFound(t *testing.T) {
	b := Parse("## Plan\n- [ ] a\n")
	if b.ArchiveItem(&Item{parsed: true, Text: "ghost"}) {
		t.Error("archiving a foreign item should return false")
	}
	if b.ArchiveItem(nil) {
		t.Error("archiving nil should return false")
	}
}

// TestArchiveRoundTripVerbatim confirms the archived block (ASCII art +
// continuation lines) survives a Render→Parse→Render cycle byte-for-byte.
func TestArchiveRoundTripVerbatim(t *testing.T) {
	src := "## Threads\n" +
		"- [>] pipeline\n" +
		"      +-----+     +-----+\n" +
		"      | src | --> | dst |\n" +
		"      +-----+     +-----+\n" +
		"  the left box is the ingest stage\n"
	b := Parse(src)
	if !b.ArchiveItem(b.Section("Threads").Items[0]) {
		t.Fatal("archive failed")
	}
	once := b.Render()
	twice := Parse(once).Render()
	if once != twice {
		t.Errorf("archived content not stable across round-trip\n--- once ---\n%q\n--- twice ---\n%q", once, twice)
	}
	if !strings.Contains(once, "## Archive\n- [>] pipeline\n      +-----+     +-----+\n") {
		t.Errorf("verbatim ASCII art not preserved in Archive:\n%s", once)
	}
}

func TestArchiveSection(t *testing.T) {
	t.Run("moves items and removes section", func(t *testing.T) {
		src := "## Plan\n- [ ] a\n\n## Threads\n- [>] t1\n  - user: r\n- [x] t2\n\n## Log\n- 08:00 start: hi\n"
		b := Parse(src)
		if !b.ArchiveSection(b.Section("Threads")) {
			t.Fatal("ArchiveSection returned false")
		}
		if b.Section("Threads") != nil {
			t.Error("Threads section still present after archive")
		}
		want := "## Plan\n- [ ] a\n\n## Archive\n- [>] t1\n  - user: r\n- [x] t2\n\n## Log\n- 08:00 start: hi\n"
		if got := b.Render(); got != want {
			t.Errorf("\n--- got ---\n%q\n--- want ---\n%q", got, want)
		}
		if got := b.ArchivedCount(); got != 2 {
			t.Errorf("ArchivedCount = %d, want 2", got)
		}
	})

	t.Run("Archive and Log are un-archivable", func(t *testing.T) {
		b := Parse("## Archive\n- [x] a\n\n## Log\n- 08:00 start: hi\n")
		before := b.Render()
		if b.ArchiveSection(b.Section("Archive")) {
			t.Error("Archive should not be archivable")
		}
		if b.ArchiveSection(b.Section("Log")) {
			t.Error("Log should not be archivable")
		}
		if got := b.Render(); got != before {
			t.Errorf("un-archivable sections changed board:\n%q", got)
		}
	})

	t.Run("section not on board", func(t *testing.T) {
		b := Parse("## Plan\n- [ ] a\n")
		if b.ArchiveSection(&Section{Title: "Ghost"}) {
			t.Error("archiving a foreign section should return false")
		}
	})
}

func TestRemoveSection(t *testing.T) {
	b := Parse("## Plan\n- [ ] a\n\n## Threads\n- [>] t\n\n## Log\n- 08:00 start: hi\n")
	if !b.RemoveSection(b.Section("Threads")) {
		t.Fatal("RemoveSection returned false")
	}
	want := "## Plan\n- [ ] a\n\n## Log\n- 08:00 start: hi\n"
	if got := b.Render(); got != want {
		t.Errorf("\n--- got ---\n%q\n--- want ---\n%q", got, want)
	}
	if b.RemoveSection(&Section{Title: "Ghost"}) {
		t.Error("removing a foreign section should return false")
	}
}

func TestUrgentIncludesChildren(t *testing.T) {
	b := Parse(`## Questions
- [ ] parent not urgent
  - [ ] !! urgent reply
  - [x] !! urgent but done reply
    - !! deep urgent claude: text
`)
	urgent := b.UrgentOpenItems()
	var texts []string
	for _, it := range urgent {
		texts = append(texts, it.Text)
	}
	want := []string{"urgent reply", "deep urgent claude: text"}
	if len(texts) != len(want) {
		t.Fatalf("got %d urgent %v, want %d", len(texts), texts, len(want))
	}
	for i := range want {
		if texts[i] != want[i] {
			t.Errorf("urgent[%d] = %q, want %q", i, texts[i], want[i])
		}
	}
}

func TestNormalizeItemText(t *testing.T) {
	cases := []struct{ in, want string }{
		{"- [ ] hello world", "hello world"},
		{"- [x] hello world", "hello world"},    // marker drift normalizes away
		{"  - [>] hello world", "hello world"},  // indentation stripped
		{"- [ ] !! hello world", "hello world"}, // urgency stripped
		{"   - hello world  ", "hello world"},   // surrounding whitespace trimmed
		{"not a bullet", "not a bullet"},        // raw line falls back to trimmed raw
	}
	for _, c := range cases {
		if got := NormalizeItemText(c.in); got != c.want {
			t.Errorf("NormalizeItemText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// Case-sensitive and internal spaces preserved: a reworded/re-cased line
	// must normalize differently so it does NOT fuzzy-match.
	if NormalizeItemText("- [ ] Hello  World") == NormalizeItemText("- [ ] hello world") {
		t.Error("normalization must be case- and internal-space-sensitive")
	}
}

func TestFindByTextInSection(t *testing.T) {
	b := Parse(`## Threads
- [ ] alpha
- [x] beta
- [ ] !! urgent task
  - [ ] nested reply
- [ ] dup
- [ ] dup
`)
	// Exact-raw lookup still wins for a verbatim line, and misses on drift.
	if b.FindByRawInSection("Threads", "- [x] beta") == nil {
		t.Error("exact raw match should still work")
	}
	if b.FindByRawInSection("Threads", "- [ ] beta") != nil {
		t.Error("exact raw must not match a marker-drifted line")
	}

	// Marker drift ([ ] -> [x]) matches by normalized text, n == 1.
	if it, n := b.FindByTextInSection("Threads", NormalizeItemText("- [ ] beta")); n != 1 || it == nil || it.DisplayText() != "beta" {
		t.Errorf("marker drift: it=%v n=%d", it, n)
	}
	// "!!" urgency added on disk still matches the un-urgent search key.
	if it, n := b.FindByTextInSection("Threads", NormalizeItemText("- [ ] urgent task")); n != 1 || it == nil {
		t.Errorf("urgency drift: it=%v n=%d", it, n)
	}
	// Indentation drift: a nested reply matches a top-level-looking key.
	if it, n := b.FindByTextInSection("Threads", NormalizeItemText("- [ ] nested reply")); n != 1 || it == nil {
		t.Errorf("indent drift: it=%v n=%d", it, n)
	}
	// Two identical lines are ambiguous twins: n == 2 (caller treats as no match).
	if _, n := b.FindByTextInSection("Threads", NormalizeItemText("- [ ] dup")); n != 2 {
		t.Errorf("twin count = %d, want 2", n)
	}
	// A genuinely reworded line misses: n == 0.
	if _, n := b.FindByTextInSection("Threads", NormalizeItemText("- [ ] alpha reworded")); n != 0 {
		t.Errorf("reworded count = %d, want 0", n)
	}
	// Unknown section: n == 0.
	if _, n := b.FindByTextInSection("Nope", "alpha"); n != 0 {
		t.Errorf("missing section count = %d, want 0", n)
	}
}

func TestPinnedParseRoundTripAndStrip(t *testing.T) {
	src := `## Working Agreements
- !pin always run gofmt before committing
- [ ] !pin verify the build @claude
- !! urgent not pinned
- plain item

## Plan
- [>] !pin wip pinned
`
	b := Parse(src)
	// Round-trip must preserve the !pin marker byte-for-byte (marker survives
	// through Text-stripping and render, exactly like !!).
	if got := b.Render(); got != src {
		t.Fatalf("round-trip mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, src)
	}

	wa := b.Section("Working Agreements")
	// Marker stripped from display Text.
	if wa.Items[0].Text != "always run gofmt before committing" {
		t.Errorf("Text = %q, marker not stripped", wa.Items[0].Text)
	}
	if !wa.Items[0].Pinned || wa.Items[0].Urgent {
		t.Errorf("item0 Pinned=%v Urgent=%v, want pinned only", wa.Items[0].Pinned, wa.Items[0].Urgent)
	}
	// "!!" is urgent, not pinned (not conflated).
	if wa.Items[2].Pinned || !wa.Items[2].Urgent {
		t.Errorf("!! item: Pinned=%v Urgent=%v", wa.Items[2].Pinned, wa.Items[2].Urgent)
	}
	// Bare "!pin" (no text) parses as pinned with empty text.
	bare := parseLine("- !pin")
	if bare.Text != "" || !bare.Pinned {
		t.Errorf("bare !pin: Text=%q Pinned=%v", bare.Text, bare.Pinned)
	}
}

func TestPinnedItems(t *testing.T) {
	b := Parse(`## Working Agreements
- !pin agreement one
- [ ] !pin agreement two @claude
- [x] !pin done pinned
- !! urgent
- plain

## Plan
- [>] !pin wip pinned
`)
	pinned := b.PinnedItems()
	var texts []string
	for _, it := range pinned {
		texts = append(texts, it.Text)
	}
	want := []string{"agreement one", "agreement two @claude", "wip pinned"}
	if len(texts) != len(want) {
		t.Fatalf("got %d pinned %v, want %d %v", len(texts), texts, len(want), want)
	}
	for i := range want {
		if texts[i] != want[i] {
			t.Errorf("pinned[%d] = %q, want %q", i, texts[i], want[i])
		}
	}
}

func TestTogglePinned(t *testing.T) {
	b := Parse(`## Working Agreements
- [ ] agreement one
- !pin agreement two
`)
	items := b.Section("Working Agreements").Items
	// Pin an unpinned item: marker appears, flag set, one item still.
	items[0].TogglePinned()
	if !items[0].Pinned {
		t.Fatal("TogglePinned did not set Pinned")
	}
	if !strings.Contains(b.Render(), "- [ ] !pin agreement one") {
		t.Errorf("!pin not added to render: %q", b.Render())
	}
	// Unpin a pinned item: marker removed, flag cleared.
	items[1].TogglePinned()
	if items[1].Pinned {
		t.Fatal("TogglePinned did not clear Pinned")
	}
	if strings.Contains(b.Render(), "!pin agreement two") {
		t.Errorf("!pin not removed from render: %q", b.Render())
	}
	if got := len(b.PinnedItems()); got != 1 {
		t.Errorf("after toggles: %d pinned, want 1", got)
	}
}

func TestCoherenceQueries(t *testing.T) {
	b := Parse(`## Waiting on User
- [ ] approve the plan
- [ ] pick a color

## Threads
- [>] refactor auth
- [>] migrate db
- [ ] not wip

## Questions
- [ ] which db? @claude
  - claude: postgres
- [ ] deploy target? @claude
- [x] done question @claude
- [ ] no mention here
`)
	if got := len(b.InProgressRawLines()); got != 2 {
		t.Errorf("InProgressRawLines = %d, want 2", got)
	}
	unanswered := b.UnansweredClaudeQuestions()
	if len(unanswered) != 1 || unanswered[0].Text != "deploy target? @claude" {
		t.Errorf("UnansweredClaudeQuestions = %v, want [deploy target?]", unanswered)
	}
	if got := b.WaitingOnUserCount(); got != 2 {
		t.Errorf("WaitingOnUserCount = %d, want 2", got)
	}
}
