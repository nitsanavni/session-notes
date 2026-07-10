package keymap

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestREADMEUpToDate is the drift gate: it re-renders the generated keys table
// and fails if README.md is stale. Fix with `go generate ./...`.
func TestREADMEUpToDate(t *testing.T) {
	path := filepath.Join("..", "..", "README.md")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out, err := InjectMarkdown(string(src))
	if err != nil {
		t.Fatal(err)
	}
	if out != string(src) {
		t.Errorf("README.md keybindings table is stale — run `go generate ./...`")
	}
}

// TestMarkdownWellFormed sanity-checks the rendered table: a header, a divider,
// and one row per TUI outline binding.
func TestMarkdownWellFormed(t *testing.T) {
	md := Markdown()
	lines := strings.Split(strings.TrimRight(md, "\n"), "\n")
	want := 2 // header + divider
	for _, b := range Outline {
		if b.TUI {
			want++
		}
	}
	if len(lines) != want {
		t.Errorf("markdown rows = %d, want %d", len(lines), want)
	}
}

// docTokens is the set of handler key tokens the keymap documents. Each binding
// Keys string is split on its separators and every alias for each token is
// added, so a handler `case "down"` (documented as "↓") is recognized.
func docTokens() map[string]bool {
	aliases := map[string][]string{
		"↓": {"down"}, "↑": {"up"}, "→": {"right"}, "←": {"left"},
		"space": {" "}, "shift-tab": {"shift+tab"},
		"1 … 9": {"1", "2", "3", "4", "5", "6", "7", "8", "9"},
		"h":     {"left"}, "j": {"down"}, "k": {"up"}, "l": {"right"},
		"arrows": {"up", "down", "left", "right"},
		"m / q":  {"m", "q"},
	}
	set := map[string]bool{}
	add := func(tok string) {
		tok = strings.TrimSpace(tok)
		// Strip a trailing parenthetical qualifier, e.g. "tab (in search)".
		if i := strings.Index(tok, "("); i >= 0 {
			tok = strings.TrimSpace(tok[:i])
		}
		if tok == "" {
			return
		}
		set[tok] = true
		for _, a := range aliases[tok] {
			set[a] = true
		}
	}
	for _, tbl := range [][]Binding{Outline, Map} {
		for _, b := range tbl {
			for _, part := range regexp.MustCompile(`\s*/\s*|,\s*`).Split(b.Keys, -1) {
				add(part)
			}
			// Also add the whole Keys (covers multi-word tokens like "1 … 9").
			add(b.Keys)
		}
	}
	return set
}

// caseRe pulls the quoted labels out of a Go `case "x", "y":` line.
var caseRe = regexp.MustCompile(`case\s+((?:"[^"]*"\s*,?\s*)+):`)

// handlerKeys extracts the key tokens dispatched in a Go handler function body,
// bounded by funcName's `func ... {` and the next top-level `}`.
func handlerKeys(t *testing.T, file, funcName string) []string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "tui", file))
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	i := strings.Index(s, funcName)
	if i < 0 {
		t.Fatalf("%s not found in %s", funcName, file)
	}
	s = s[i:]
	// Bound to this function body: cut at the next top-level func declaration.
	if e := strings.Index(s[1:], "\nfunc "); e >= 0 {
		s = s[:e+1]
	}
	var keys []string
	for _, m := range caseRe.FindAllStringSubmatch(s, -1) {
		for _, q := range regexp.MustCompile(`"([^"]*)"`).FindAllStringSubmatch(m[1], -1) {
			keys = append(keys, q[1])
		}
		// Stop once we leave the dispatch switch (heuristic: the `?`/help case
		// is the last documented arm in both handlers).
	}
	return keys
}

// TestHandlersDocumented is the reality guard: every key dispatched in the TUI
// board and map handlers must be documented in the keymap tables (or be a
// universal, intentionally-undocumented key like ctrl+c). Fails listing any
// drift so a newly-handled key can't silently escape all help surfaces.
func TestHandlersDocumented(t *testing.T) {
	// Universal keys we deliberately keep out of the help tables.
	universal := map[string]bool{"ctrl+c": true, "esc": true}
	doc := docTokens()
	check := func(file, fn string) {
		for _, k := range handlerKeys(t, file, fn) {
			if universal[k] || doc[k] {
				continue
			}
			t.Errorf("%s: key %q is handled but not documented in the keymap tables", fn, k)
		}
	}
	check("tui.go", "func (m *model) handleBoardKey")
	check("mapview.go", "func (m *model) handleMapKey")
}
