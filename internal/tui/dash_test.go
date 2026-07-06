package tui

import (
	"testing"
	"time"

	"github.com/nitsanavni/session-notes/internal/board"
)

func mkBoard(session, cwd, title string, threads, log []string) *board.Board {
	src := "---\nsession: " + session + "\ncwd: " + cwd + "\n"
	if title != "" {
		src += "title: " + title + "\n"
	}
	src += "---\n\n## Threads\n"
	for _, t := range threads {
		src += "- [>] " + t + "\n"
	}
	src += "\n## Log\n"
	for _, l := range log {
		src += "- " + l + "\n"
	}
	b := board.Parse(src)
	b.Path = "/boards/" + session + ".md"
	return b
}

func TestBuildDashCardsSortAndFilter(t *testing.T) {
	now := time.Now()
	boards := []*board.Board{
		mkBoard("s-idle", "/proj", "idle one", []string{"thread i"}, []string{"09:00 x: hi"}),
		mkBoard("s-active", "/proj", "active one", []string{"thread a"}, nil),
		mkBoard("s-gone", "/proj", "gone one", nil, nil),
		mkBoard("s-other", "/elsewhere", "other proj", nil, nil),
	}
	live := map[string]time.Time{
		"s-active": now.Add(-30 * time.Second),
		"s-idle":   now.Add(-30 * time.Minute),
		"s-gone":   {}, // no transcript
		"s-other":  now,
	}
	liveFn := func(b *board.Board) time.Time { return live[sessionOf(b)] }

	// Filtered to /proj (all=false): drops s-other.
	cards := buildDashCards(boards, "/proj", false, liveFn)
	if len(cards) != 3 {
		t.Fatalf("got %d cards, want 3 (filtered to /proj)", len(cards))
	}
	gotOrder := []string{cards[0].session, cards[1].session, cards[2].session}
	wantOrder := []string{"s-active", "s-idle", "s-gone"}
	for i := range wantOrder {
		if gotOrder[i] != wantOrder[i] {
			t.Fatalf("order = %v, want %v", gotOrder, wantOrder)
		}
	}

	// Card contents.
	if cards[0].name() != "active one" {
		t.Errorf("name = %q, want title 'active one'", cards[0].name())
	}
	if len(cards[0].threads) != 1 || cards[0].threads[0] != "thread a" {
		t.Errorf("threads = %v", cards[0].threads)
	}
	if cards[1].lastLog != "09:00 x: hi" {
		t.Errorf("lastLog = %q", cards[1].lastLog)
	}
	if !cards[2].mtime.IsZero() {
		t.Errorf("gone card should have zero mtime")
	}

	// all=true includes every project.
	if all := buildDashCards(boards, "/proj", true, liveFn); len(all) != 4 {
		t.Fatalf("all: got %d cards, want 4", len(all))
	}
}

func TestBuildDashCardsThreadCap(t *testing.T) {
	now := time.Now()
	b := mkBoard("s1", "/p", "many", []string{"t1", "t2", "t3", "t4", "t5"}, nil)
	cards := buildDashCards([]*board.Board{b}, "/p", false, func(*board.Board) time.Time { return now })
	if len(cards) != 1 || len(cards[0].threads) != 3 {
		t.Fatalf("thread cap: got %d threads, want 3", len(cards[0].threads))
	}
}

func TestClassify(t *testing.T) {
	now := time.Now()
	if g, _ := classify(now.Add(-10*time.Second), now); g != "●" {
		t.Errorf("recent should be active ●, got %q", g)
	}
	if g, _ := classify(now.Add(-30*time.Minute), now); g != "○" {
		t.Errorf("30m should be idle ○, got %q", g)
	}
	if g, _ := classify(now.Add(-5*time.Hour), now); g != "✗" {
		t.Errorf("5h should be gone ✗, got %q", g)
	}
	if g, _ := classify(time.Time{}, now); g != "✗" {
		t.Errorf("zero mtime should be gone ✗, got %q", g)
	}
}

func TestRelTime(t *testing.T) {
	now := time.Now()
	cases := []struct {
		mtime time.Time
		want  string
	}{
		{now.Add(-16 * time.Second), "16s"},
		{now.Add(-4 * time.Minute), "4m"},
		{now.Add(-3 * time.Hour), "3h"},
		{now.Add(-2 * 24 * time.Hour), "2d"},
		{time.Time{}, "—"},
	}
	for _, c := range cases {
		if got := relTime(c.mtime, now); got != c.want {
			t.Errorf("relTime(%v) = %q, want %q", c.mtime, got, c.want)
		}
	}
}
