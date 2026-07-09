package mindmap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

//go:generate ./testdata/gen.sh

// TestGoldenAgainstMM checks that Render is byte-identical to mm's `tree`
// subcommand for the captured fixtures in testdata/. The .out files are
// produced by testdata/gen.sh running the real mm; tests never invoke bun.
//
// Every fixture in the set renders byte-identical to mm. The one known
// divergence from mm — grapheme-cluster width for ZWJ/VS16 emoji sequences (see
// dispWidth in width.go) — is deliberately kept out of the fixtures, since Go's
// stdlib has no grapheme segmenter and mm's own no-Segmenter fallback would
// diverge identically. Single-code-point CJK/emoji and combining-mark accents
// (unicode.md) are covered and match.
func TestGoldenAgainstMM(t *testing.T) {
	fixtures, err := filepath.Glob("testdata/*.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(fixtures) == 0 {
		t.Fatal("no fixtures found")
	}
	for _, md := range fixtures {
		md := md
		name := strings.TrimSuffix(filepath.Base(md), ".md")
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(md)
			if err != nil {
				t.Fatal(err)
			}
			wantBytes, err := os.ReadFile(strings.TrimSuffix(md, ".md") + ".out")
			if err != nil {
				t.Fatalf("missing golden (run testdata/gen.sh): %v", err)
			}
			// mm's printTree console.logs each line; the file is the lines
			// joined by newlines plus one trailing newline.
			want := strings.TrimSuffix(string(wantBytes), "\n")
			got := strings.Join(Render(filepath.Base(md), Parse(string(src)), Rounded), "\n")
			if got != want {
				t.Errorf("render mismatch for %s\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
			}
		})
	}
}

func TestRoundTripLossless(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{"heading and bullets", "# Root\n- a\n- b\n  - c\n"},
		{"checkboxes", "- [ ] open\n- [x] done\n- plain\n"},
		{"deep nesting", "- a\n  - b\n    - c\n      - d\n"},
		{"no heading", "- one\n- two\n"},
		{"unicode", "# 中心\n- 日本語\n- café\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			once := Serialize(Parse(c.src))
			if once != c.src {
				t.Errorf("parse/serialize not byte-identical\n got: %q\nwant: %q", once, c.src)
			}
			twice := Serialize(Parse(once))
			if twice != once {
				t.Errorf("round-trip not idempotent\n got: %q\nwant: %q", twice, once)
			}
		})
	}
}

func TestParseNormalization(t *testing.T) {
	// GFM [X] lowercases; blank lines and trailing whitespace drop.
	roots := Parse("# Title  \n\n- [X] done  \n\n- plain\n")
	if len(roots) != 1 || !roots[0].Heading || roots[0].Text != "Title" {
		t.Fatalf("unexpected roots: %+v", roots)
	}
	kids := roots[0].Children
	if len(kids) != 2 {
		t.Fatalf("want 2 children, got %d", len(kids))
	}
	if kids[0].Checkbox != Checked || kids[0].Text != "done" {
		t.Errorf("checkbox normalization: %+v", kids[0])
	}
}

func TestEmptyHeadingDropped(t *testing.T) {
	roots := Parse("# \n- a\n- b\n")
	if len(roots) != 2 {
		t.Fatalf("empty heading should be dropped, leaving 2 roots, got %d", len(roots))
	}
}

// TestLayoutNoOverlap verifies that no two placements occupy the same cell and
// that placement text never collides with a connector cell — the core layout
// invariant (the map is a grid where every glyph owns its cell).
func TestLayoutNoOverlap(t *testing.T) {
	fixtures, _ := filepath.Glob("testdata/*.md")
	for _, md := range fixtures {
		md := md
		t.Run(filepath.Base(md), func(t *testing.T) {
			src, _ := os.ReadFile(md)
			roots := Parse(string(src))
			rootNode := TitleRoot(roots)
			children := roots
			if rootNode != nil {
				children = rootNode.Children
			}
			lay := LayoutTree(filepath.Base(md), rootNode, children, Rounded)

			// Occupied text cells across all placements must be disjoint.
			type cell struct{ r, c int }
			occupied := map[cell]*Placement{}
			for _, p := range lay.Placements {
				col := p.Col
				for _, ch := range p.Text {
					cw := charW(ch)
					if cw == 0 {
						continue
					}
					for k := 0; k < cw; k++ {
						key := cell{p.Row, col + k}
						if prev, ok := occupied[key]; ok {
							t.Errorf("cell %v shared by %q and %q", key, prev.Text, p.Text)
						}
						occupied[key] = p
					}
					col += cw
				}
			}
		})
	}
}

// TestConnectorsJoin checks that every connector cell resolves to a real skin
// glyph (a defined junction or the dash) and that a child dash actually abuts
// its junction column — i.e. connectors join rather than float.
func TestConnectorsJoin(t *testing.T) {
	roots := Parse("# C\n- a\n- b\n  - b1\n  - b2\n")
	lay := LayoutTree("C", TitleRoot(roots), TitleRoot(roots).Children, Rounded)
	// Every non-space, non-hole, non-placement rune in a line must be a known
	// connector glyph.
	glyphs := map[rune]bool{Rounded.Dash: true}
	for _, g := range Rounded.Junction {
		glyphs[g] = true
	}
	placementRunes := map[[2]int]bool{}
	for _, p := range lay.Placements {
		c := p.Col
		for _, ch := range p.Text {
			cw := charW(ch)
			if cw == 0 {
				continue
			}
			placementRunes[[2]int{p.Row, c}] = true
			c += cw
		}
	}
	for _, l := range lay.Lines {
		c := lay.MinCol
		for _, ch := range l.Text {
			if ch != ' ' && ch != 0 && !placementRunes[[2]int{l.Row, c}] {
				if !glyphs[ch] {
					t.Errorf("row %d col %d: %q is not a connector glyph", l.Row, c, string(ch))
				}
			}
			c++
		}
	}
}
