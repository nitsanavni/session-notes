package mindmap

import "testing"

func TestDispWidth(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"abc", 3},
		{"", 0},
		{"日本語", 6},   // CJK: 2 cells each
		{"café", 4},  // precomposed é: 1 cell
		{"café", 4}, // e + combining acute: combining mark is width 0
		{"한국어", 6},   // hangul syllables: 2 cells each
		{"😀", 2},     // single-code-point emoji: 2 cells
	}
	for _, c := range cases {
		if got := dispWidth(c.in); got != c.want {
			t.Errorf("dispWidth(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestStripMarkers(t *testing.T) {
	cases := map[string]string{
		"**bold**":   "bold",
		"*italic*":   "italic",
		"~~strike~~": "strike",
		"plain":      "plain",
		"a **b** c":  "a b c",
		"no*match":   "no*match",
	}
	for in, want := range cases {
		if got := stripMarkers(in); got != want {
			t.Errorf("stripMarkers(%q) = %q, want %q", in, got, want)
		}
	}
}
