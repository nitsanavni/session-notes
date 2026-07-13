package main

import "testing"

// TestSafeBoardID guards the pull path against a malicious server returning a
// board id that would escape the target directory (finding C).
func TestSafeBoardID(t *testing.T) {
	hostile := []string{
		"../etc/passwd",
		"..",
		".",
		"",
		"a/b",
		"a\\b",
		"sub/../../x",
		"foo/..",
		"..bar/..",
	}
	for _, id := range hostile {
		if err := safeBoardID(id); err == nil {
			t.Errorf("safeBoardID(%q) = nil, want error (path traversal)", id)
		}
	}

	safe := []string{"myboard", "board-1", "a_b.c", "UPPER123"}
	for _, id := range safe {
		if err := safeBoardID(id); err != nil {
			t.Errorf("safeBoardID(%q) = %v, want nil", id, err)
		}
	}
}
