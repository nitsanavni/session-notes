package mindmap

import (
	"regexp"
	"strings"
)

// Arm bits for connector junctions: which stubs meet at a cell.
const (
	up    = 1
	down  = 2
	left  = 4
	right = 8
)

var markerRE = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`\*\*(.+?)\*\*`), "$1"},
	{regexp.MustCompile(`\*(.+?)\*`), "$1"},
	{regexp.MustCompile(`~~(.+?)~~`), "$1"},
}

// stripMarkers removes inline markdown emphasis markers, leaving visible text.
func stripMarkers(text string) string {
	for _, m := range markerRE {
		text = m.re.ReplaceAllString(text, m.repl)
	}
	return text
}

// charW is the terminal cell width of one rune: 0 for combining marks, 2 for
// East-Asian wide / emoji, else 1. Ported verbatim from mm's style.charW.
func charW(c rune) int {
	if c >= 0x0300 && c <= 0x036f {
		return 0 // combining marks
	}
	if (c >= 0x1100 && c <= 0x115f) || // hangul jamo
		(c >= 0x2e80 && c <= 0x303e) || // CJK radicals, punctuation
		(c >= 0x3041 && c <= 0x33ff) || // kana, CJK symbols
		(c >= 0x3400 && c <= 0x4dbf) || // CJK ext A
		(c >= 0x4e00 && c <= 0x9fff) || // CJK unified
		(c >= 0xa000 && c <= 0xa4cf) || // yi
		(c >= 0xac00 && c <= 0xd7a3) || // hangul syllables
		(c >= 0xf900 && c <= 0xfaff) || // CJK compat
		(c >= 0xfe30 && c <= 0xfe4f) || // CJK compat forms
		(c >= 0xff00 && c <= 0xff60) || // fullwidth forms
		(c >= 0xffe0 && c <= 0xffe6) ||
		(c >= 0x1f300 && c <= 0x1faff) || // emoji
		(c >= 0x20000 && c <= 0x3fffd) { // CJK ext B+
		return 2
	}
	return 1
}

// dispWidth is the display width of a plain (ANSI-free) string.
//
// mm computes this over Intl.Segmenter grapheme clusters so a ZWJ/VS16 emoji
// sequence counts as one 2-col glyph. Go's stdlib has no grapheme segmenter, so
// this sums per-code-point charW, matching mm's no-Segmenter fallback path. The
// two agree for every string that is not a ZWJ-joined or VS16-forced emoji
// cluster (combining marks are width 0, so "café" = 4 either way). Fixtures
// deliberately avoid ZWJ/VS16 clusters; see width_test.go and the golden test
// comment for the documented divergence.
func dispWidth(text string) int {
	w := 0
	for _, c := range text {
		w += charW(c)
	}
	return w
}

// truncateDisplay clips s to at most max display columns, appending a "…"
// (width 1) when it actually cuts. Display-width aware: wide (CJK/emoji) glyphs
// count as 2 and are never split across the boundary. max<=0 means no limit.
func truncateDisplay(s string, max int) string {
	if max <= 0 || dispWidth(s) <= max {
		return s
	}
	budget := max - 1 // reserve one column for the ellipsis
	var b strings.Builder
	w := 0
	for _, r := range s {
		cw := charW(r)
		if w+cw > budget {
			break
		}
		b.WriteRune(r)
		w += cw
	}
	b.WriteRune('…')
	return b.String()
}

// hardChunks splits a single word into pieces each at most max display columns
// wide (for words longer than the wrap width).
func hardChunks(word string, max int) []string {
	var chunks []string
	var b strings.Builder
	w := 0
	for _, r := range word {
		cw := charW(r)
		if w+cw > max && w > 0 {
			chunks = append(chunks, b.String())
			b.Reset()
			w = 0
		}
		b.WriteRune(r)
		w += cw
	}
	if b.Len() > 0 {
		chunks = append(chunks, b.String())
	}
	return chunks
}

// wrapDisplay word-wraps s to lines each at most max display columns wide,
// hard-breaking any single word longer than max. Runs of whitespace collapse to
// a single space. Always returns at least one line. max<=0 returns s unwrapped.
func wrapDisplay(s string, max int) []string {
	if max <= 0 {
		return []string{s}
	}
	var lines []string
	cur := ""
	curW := 0
	flush := func() {
		if cur != "" {
			lines = append(lines, cur)
			cur, curW = "", 0
		}
	}
	for _, word := range strings.Fields(s) {
		ww := dispWidth(word)
		if ww > max {
			flush()
			chunks := hardChunks(word, max)
			// keep the trailing partial chunk open so following words can pack in
			for i, ch := range chunks {
				if i < len(chunks)-1 {
					lines = append(lines, ch)
				} else {
					cur, curW = ch, dispWidth(ch)
				}
			}
			continue
		}
		add := ww
		if cur != "" {
			add++ // the joining space
		}
		if curW+add > max {
			flush()
			cur, curW = word, ww
		} else {
			if cur != "" {
				cur += " "
				curW++
			}
			cur += word
			curW += ww
		}
	}
	flush()
	if len(lines) == 0 {
		lines = []string{""}
	}
	return lines
}
