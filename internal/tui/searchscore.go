package tui

import (
	"sort"
	"strings"

	"github.com/nitsanavni/session-notes/internal/board"
)

// Fuzzy search scoring, shared by the outline and map result panels (and the web
// port in board.html). It ranks matches fzf-style: an exact substring beats a
// word-boundary subsequence beats a loose subsequence; within a tier, more
// contiguous runs and an earlier first-match position score higher. The matched
// character ranges come back so both the panel rows and the board highlights can
// underline exactly the characters that matched.

// searchResult is one ranked panel row: the matched item, its section label, and
// the byte ranges (over the item's DisplayText) that matched, for highlighting.
type searchResult struct {
	item    *board.Item
	section string
	working bool // section is in the working set (not Archive/Log)
	score   int
	ranges  [][2]int
}

// isWordChar reports whether b is part of a "word" for boundary detection.
func isWordChar(b byte) bool {
	return b == '_' ||
		(b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9')
}

// fuzzyScore scores query against text (fzf-lite). It returns the score, the
// matched byte ranges (coalesced), and ok=false when text does not even contain
// query as a subsequence. Higher scores rank first. query must already be
// lowercased and non-empty; text is matched case-insensitively.
func fuzzyScore(text, query string) (int, [][2]int, bool) {
	if query == "" {
		return 0, nil, false
	}
	low := strings.ToLower(text)

	// Tier 2: exact substring. Fully contiguous; earlier position wins.
	if idx := strings.Index(low, query); idx >= 0 {
		start := idx
		// Prefer the earliest word-boundary occurrence for a small bonus.
		boundary := start == 0 || !isWordChar(low[start-1])
		score := 200000 + (len(query)-1)*1000 - start*4
		if boundary {
			score += 5000
		}
		return score, [][2]int{{start, start + len(query)}}, true
	}

	// Subsequence: greedy leftmost match of every query char.
	pos := make([]int, 0, len(query))
	qi := 0
	for i := 0; i < len(low) && qi < len(query); i++ {
		if low[i] == query[qi] {
			pos = append(pos, i)
			qi++
		}
	}
	if qi < len(query) {
		return 0, nil, false // not even a subsequence
	}

	// Contiguity: adjacent matched positions. Boundary bonus: matched chars that
	// start a word. The first matched char at a boundary lifts the whole match
	// into the word-boundary tier.
	contig := 0
	boundaryBonus := 0
	for i, p := range pos {
		if i > 0 && p == pos[i-1]+1 {
			contig++
		}
		if p == 0 || !isWordChar(low[p-1]) {
			boundaryBonus++
		}
	}
	firstBoundary := pos[0] == 0 || !isWordChar(low[pos[0]-1])

	tier := 0 // loose subsequence
	if firstBoundary {
		tier = 1 // word-boundary subsequence
	}
	score := tier*100000 + contig*1000 + boundaryBonus*300 - pos[0]*4

	// Coalesce matched positions into ranges for highlighting.
	var ranges [][2]int
	rs, re := pos[0], pos[0]+1
	for _, p := range pos[1:] {
		if p == re {
			re = p + 1
		} else {
			ranges = append(ranges, [2]int{rs, re})
			rs, re = p, p+1
		}
	}
	ranges = append(ranges, [2]int{rs, re})
	return score, ranges, true
}

// searchRanked returns every item that fuzzy-matches query, best-first. It honors
// the same scope rule as searchMatches (Archive/Log skipped unless all), but when
// all is set the working-set sections still rank above Archive/Log. Ties break on
// document order for stability. An empty/whitespace query returns nil.
func searchRanked(b *board.Board, query string, all bool) []searchResult {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" || b == nil {
		return nil
	}
	var out []searchResult
	for _, s := range b.Sections {
		if !all && searchScopeExcluded(s.Title) {
			continue
		}
		working := !searchScopeExcluded(s.Title)
		var walk func(items []*board.Item)
		walk = func(items []*board.Item) {
			for _, it := range items {
				if it.IsItem() {
					if score, ranges, ok := fuzzyScore(it.DisplayText(), q); ok {
						out = append(out, searchResult{
							item:    it,
							section: s.Title,
							working: working,
							score:   score,
							ranges:  ranges,
						})
					}
				}
				walk(it.Children)
			}
		}
		walk(s.Items)
	}
	// Stable document-order tiebreak: SliceStable preserves append order.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].working != out[j].working {
			return out[i].working // working sections first
		}
		return out[i].score > out[j].score
	})
	return out
}
