package main

import (
	"strings"
	"testing"

	"github.com/nitsanavni/session-notes/internal/hooks"
)

func TestDocTopicsNonEmpty(t *testing.T) {
	for _, name := range docTopicNames() {
		// blurb is dynamic (from hooks.Blurb), not a docTopics map entry.
		if name == "blurb" {
			continue
		}
		body, ok := docTopics[name]
		if !ok {
			t.Errorf("listed topic %q has no body", name)
			continue
		}
		if strings.TrimSpace(body) == "" {
			t.Errorf("topic %q body is empty", name)
		}
	}
}

func TestDocTopicNamesOrder(t *testing.T) {
	got := docTopicNames()
	want := []string{"protocol", "monitor", "conflicts", "cli", "subtree", "headless", "blurb"}
	if len(got) != len(want) {
		t.Fatalf("topics = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("topic[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// runDocs blurb prints the session-start blurb with the placeholder path.
func TestRunDocsBlurb(t *testing.T) {
	if code := runDocs([]string{"blurb"}); code != 0 {
		t.Errorf("blurb exit = %d, want 0", code)
	}
	blurb := hooks.Blurb("<board-path>")
	for _, want := range []string{"<board-path>", "Session board:", "session-notes docs <topic>"} {
		if !strings.Contains(blurb, want) {
			t.Errorf("blurb missing %q", want)
		}
	}
}

// runDocs returns 0 for a known topic and for the bare list, 2 for unknown.
func TestRunDocsExitCodes(t *testing.T) {
	if code := runDocs([]string{"protocol"}); code != 0 {
		t.Errorf("known topic exit = %d, want 0", code)
	}
	if code := runDocs(nil); code != 0 {
		t.Errorf("bare list exit = %d, want 0", code)
	}
	if code := runDocs([]string{"nope"}); code != 2 {
		t.Errorf("unknown topic exit = %d, want 2", code)
	}
}
