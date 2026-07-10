package tui

import (
	"reflect"
	"strings"
	"testing"
)

func TestNoveltyItems(t *testing.T) {
	before := "## Threads\n\n- [ ] auth refactor\n"
	after := "## Threads\n\n- [ ] auth refactor\n  - claude: extracted, tests green\n"
	got := noveltyItems(before, after)
	want := []string{"- claude: extracted, tests green"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("noveltyItems = %q, want %q", got, want)
	}
}

func TestNoveltyItemsIgnoresHeadingsAndBlanks(t *testing.T) {
	before := "## Plan\n\n- [ ] a\n"
	after := "## Plan\n\n- [ ] a\n\n## Ideas\n\n- [ ] fresh idea\n"
	got := noveltyItems(before, after)
	want := []string{"- [ ] fresh idea"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("noveltyItems = %q, want %q", got, want)
	}
}

func TestNoveltyItemsStatusRewrite(t *testing.T) {
	// A status toggle rewrites the raw line: old removed, new added — the new
	// line is the change to flag.
	before := "- [ ] ship it\n"
	after := "- [x] ship it\n"
	got := noveltyItems(before, after)
	want := []string{"- [x] ship it"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("noveltyItems = %q, want %q", got, want)
	}
}

func TestNoveltyNoticeReply(t *testing.T) {
	got := noveltyNotice([]string{"- claude: extracted, tests green"})
	if !strings.Contains(got, "claude replied") {
		t.Fatalf("notice = %q, want it to mention claude replied", got)
	}
	if !strings.Contains(got, "tests green") {
		t.Fatalf("notice = %q, want the reply text", got)
	}
}

func TestNoveltyNoticeCountsExtra(t *testing.T) {
	got := noveltyNotice([]string{"- [ ] one", "- [ ] two", "- [ ] three"})
	if !strings.Contains(got, "(+2 more)") {
		t.Fatalf("notice = %q, want (+2 more)", got)
	}
}

func TestNoveltyNoticeEmpty(t *testing.T) {
	if got := noveltyNotice(nil); got != "" {
		t.Fatalf("notice(nil) = %q, want empty", got)
	}
}
