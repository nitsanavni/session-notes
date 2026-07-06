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
		"- [ ] a\n- [ ] new step\n\n", // inserted before section-separating blank
		"## Ideas\n- an idea\n",       // plain bullet in Ideas
		"## Brand New\n- [ ] hello\n", // section created
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
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
	// No temp files left behind.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("leftover files in dir: %v", entries)
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
