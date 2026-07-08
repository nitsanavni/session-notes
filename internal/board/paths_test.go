package board

import (
	"strings"
	"testing"
	"time"
)

func TestTemplateDefaultSections(t *testing.T) {
	started := time.Date(2026, 7, 8, 21, 30, 0, 0, time.UTC)
	src := Template("abc-123", "/home/nitsan/code/foo", started)
	b := Parse(src)

	want := []string{
		"Waiting on User",
		"Plan",
		"Threads",
		"Questions",
		"Ideas",
		"Parked",
		"Working Agreements",
		"Log",
	}
	if len(b.Sections) != len(want) {
		t.Fatalf("sections = %d, want %d\n%s", len(b.Sections), len(want), src)
	}
	for i, title := range want {
		if b.Sections[i].Title != title {
			t.Errorf("section %d = %q, want %q", i, b.Sections[i].Title, title)
		}
	}

	// Archive is created on demand by the first archive action, never seeded.
	if b.Section(ArchiveTitle) != nil {
		t.Error("fresh board must not contain an Archive section")
	}

	// Frontmatter and the initial Log entry are preserved.
	for _, line := range []string{
		"session: abc-123\n",
		"cwd: /home/nitsan/code/foo\n",
		"started: 2026-07-08T21:30:00Z\n",
	} {
		if !strings.Contains(src, line) {
			t.Errorf("template missing frontmatter line %q", line)
		}
	}
	if !strings.Contains(src, "\n## Log\n\n- 21:30 start: session started in /home/nitsan/code/foo\n") {
		t.Errorf("template missing blank-separated start log entry:\n%s", src)
	}

	// The template must round-trip losslessly through parse -> render.
	if got := b.Render(); got != src {
		t.Errorf("template does not round-trip\n--- template ---\n%s\n--- rendered ---\n%s", src, got)
	}
}
