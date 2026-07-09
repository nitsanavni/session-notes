package mindmap

import (
	"strings"
	"testing"
)

// TestTruncateDisplayWidth checks display-width-aware truncation: the result
// never exceeds the cap, appends a "…" only when it actually cuts, and never
// splits a wide (2-col) glyph across the boundary.
func TestTruncateDisplayWidth(t *testing.T) {
	cases := []struct {
		name    string
		s       string
		max     int
		wantW   int // expected display width of the result
		wantCut bool
	}{
		{"fits ascii", "hello", 40, 5, false},
		{"cut ascii", "the quick brown fox jumps", 10, 10, true},
		{"exact fit", "exactly-ten", 11, 11, false},
		{"cjk no split", "日本語日本語日本語", 8, 7, true},   // 3 wide glyphs (6) + …
		{"combining accents", "café", 10, 4, false}, // é = e + combining, width 4
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := truncateDisplay(c.s, c.max)
			if w := dispWidth(got); w != c.wantW {
				t.Errorf("dispWidth(%q) = %d, want %d", got, w, c.wantW)
			}
			if dispWidth(got) > c.max {
				t.Errorf("result %q exceeds max %d", got, c.max)
			}
			if cut := strings.HasSuffix(got, "…"); cut != c.wantCut {
				t.Errorf("cut = %v, want %v (%q)", cut, c.wantCut, got)
			}
		})
	}
}

// TestWrapDisplayWidth checks word-wrapping keeps every line within the cap and
// hard-breaks a single over-long word.
func TestWrapDisplayWidth(t *testing.T) {
	lines := wrapDisplay("the quick brown fox jumps over the lazy dog", 12)
	if len(lines) < 2 {
		t.Fatalf("expected multiple wrapped lines, got %v", lines)
	}
	for _, l := range lines {
		if dispWidth(l) > 12 {
			t.Errorf("line %q exceeds 12", l)
		}
	}
	// A word longer than the cap is hard-broken, still respecting the cap.
	long := wrapDisplay("supercalifragilisticexpialidocious", 10)
	for _, l := range long {
		if dispWidth(l) > 10 {
			t.Errorf("hard-break line %q exceeds 10", l)
		}
	}
	if strings.Join(long, "") != "supercalifragilisticexpialidocious" {
		t.Errorf("hard-break lost characters: %v", long)
	}
}

// TestLayoutTruncationOptIn asserts MaxWidth caps every placement's width, while
// the zero Options leaves text full-length (mm-identical).
func TestLayoutTruncationOptIn(t *testing.T) {
	src := "# Root\n- a short one\n- this is a very long node that would sprawl the map horizontally without truncation\n"
	roots := Parse(src)
	rootNode := TitleRoot(roots)

	full := LayoutTree("f", rootNode, rootNode.Children, Rounded)
	widest := 0
	for _, p := range full.Placements {
		if p.Width > widest {
			widest = p.Width
		}
	}
	if widest <= 40 {
		t.Fatalf("expected a full-length (>40) node without truncation, widest=%d", widest)
	}

	capped := LayoutTreeOpts("f", rootNode, rootNode.Children, Rounded, Options{MaxWidth: 40})
	for _, p := range capped.Placements {
		if p.Width > 40 {
			t.Errorf("placement %q width %d exceeds cap 40", p.Text, p.Width)
		}
	}
}

// TestLayoutExpandMultiLine asserts an expanded node becomes a multi-row block,
// connectors attach at its vertical center, and no two placements share a cell.
func TestLayoutExpandMultiLine(t *testing.T) {
	src := "# Root\n- parent node here\n  - a fairly long child that will wrap across several lines when expanded\n"
	roots := Parse(src)
	rootNode := TitleRoot(roots)
	parent := rootNode.Children[0]
	child := parent.Children[0]

	opts := Options{MaxWidth: 20, Expanded: map[*Node]bool{child: true}}
	lay := LayoutTreeOpts("f", rootNode, rootNode.Children, Rounded, opts)

	var cp, pp *Placement
	for _, p := range lay.Placements {
		if p.Node == child {
			cp = p
		}
		if p.Node == parent {
			pp = p
		}
	}
	if cp == nil || pp == nil {
		t.Fatal("missing placements")
	}
	if cp.Height < 2 {
		t.Fatalf("expanded child height = %d, want >= 2", cp.Height)
	}
	if len(cp.Lines) != cp.Height {
		t.Errorf("Lines count %d != Height %d", len(cp.Lines), cp.Height)
	}
	// Every wrapped line stays within the cap.
	for _, l := range cp.Lines {
		if dispWidth(l) > 20 {
			t.Errorf("wrapped line %q exceeds cap", l)
		}
	}
	// The connector must attach at the child's vertical center row: with a single
	// child, the parent's row equals the child's center row.
	if pp.Row != cp.Row {
		t.Errorf("parent row %d != child center row %d (connector off-center)", pp.Row, cp.Row)
	}
	// The child's block top is Row-(Height-1)/2; its span must contain Row.
	top := cp.Row - (cp.Height-1)/2
	if cp.Row < top || cp.Row > top+cp.Height-1 {
		t.Errorf("center row %d outside block [%d,%d]", cp.Row, top, top+cp.Height-1)
	}

	// No two placements share a text cell across all their rows.
	type cell struct{ r, c int }
	occ := map[cell]*Placement{}
	for _, p := range lay.Placements {
		ptop := p.Row - (p.Height-1)/2
		for li, line := range p.Lines {
			col := p.Col
			for _, ch := range line {
				cw := charW(ch)
				if cw == 0 {
					continue
				}
				for k := 0; k < cw; k++ {
					key := cell{ptop + li, col + k}
					if prev, ok := occ[key]; ok {
						t.Errorf("cell %v shared by %q and %q", key, prev.Text, p.Text)
					}
					occ[key] = p
				}
				col += cw
			}
		}
	}
}
