package main

import (
	"strings"
	"testing"
)

func TestDocTopicsNonEmpty(t *testing.T) {
	for _, name := range docTopicNames() {
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
	want := []string{"protocol", "monitor", "conflicts", "cli"}
	if len(got) != len(want) {
		t.Fatalf("topics = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("topic[%d] = %q, want %q", i, got[i], want[i])
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
