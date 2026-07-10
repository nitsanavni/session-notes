package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/nitsanavni/session-notes/internal/board"
)

// Incremental search, shared by the outline and map views. `/` opens a prompt;
// as the query changes a scrollable, fuzzy-ranked results PANEL updates (best
// match first) WITHOUT revealing or moving anything on the board. Up/Down (and
// ctrl+n/ctrl+p) move the panel selection; Enter jumps to the selected match,
// revealing it (expanding any collapsed section / folded subtree). In results
// mode n/N step matches in document order, keeping the panel's highlighted row
// in sync. Esc cancels and restores the pre-search cursor. Matched characters are
// highlighted in both the panel rows and the board.

// searchScopeExcluded reports whether a section is outside the default search
// scope (the "working set"). Archive and Log are noisy, low-signal sections the
// user rarely means to jump into, so search skips them unless the scope is
// toggled to "everything".
func searchScopeExcluded(title string) bool {
	return title == "Archive" || title == "Log"
}

// searchMatches returns every item that fuzzy-matches query (fzf-style
// subsequence — the same predicate that populates the ranked panel), in DOCUMENT
// order across all sections, replies included. It drives predictable n/N
// board-jumping; the PANEL re-sorts the identical set by score (see searchRanked)
// so the two never disagree on which items match. Empty query matches nothing;
// when all is false the Archive and Log sections are skipped.
func searchMatches(b *board.Board, query string, all bool) []*board.Item {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" || b == nil {
		return nil
	}
	var out []*board.Item
	var walk func(items []*board.Item)
	walk = func(items []*board.Item) {
		for _, it := range items {
			if it.IsItem() {
				if _, _, ok := fuzzyScore(it.DisplayText(), q); ok {
					out = append(out, it)
				}
			}
			walk(it.Children)
		}
	}
	for _, s := range b.Sections {
		if !all && searchScopeExcluded(s.Title) {
			continue
		}
		walk(s.Items)
	}
	return out
}

// searchHitCount sums the literal occurrences of q (a lowercased, trimmed query)
// across the matched items' text — the "term hit" tally, which exceeds the node
// count when a node contains the term more than once. Only counts substring
// occurrences (subsequence-only matches contribute their node but zero literal
// hits), so it is a floor on how many times the exact term appears on the board.
func searchHitCount(results []searchResult, q string) int {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return 0
	}
	total := 0
	for _, r := range results {
		total += strings.Count(strings.ToLower(r.item.DisplayText()), q)
	}
	return total
}

// runResults recomputes the fuzzy-ranked panel for query and clamps the panel
// selection. It never moves the board (no-reveal-until-jump).
func (m *model) runResults(query string) {
	m.searchResults = searchRanked(m.board, query, m.searchScopeAll)
	if m.searchPanelIdx >= len(m.searchResults) {
		m.searchPanelIdx = 0
	}
	if m.searchPanelIdx < 0 {
		m.searchPanelIdx = 0
	}
	m.clampPanelScroll()
}

// startSearch opens the incremental search prompt over whichever view is active.
// It captures the pre-search cursor (outline) or focus key (map) so Esc can
// restore it. The results panel populates immediately, but the board does NOT
// move — nothing is revealed until Enter (or n/N) jumps.
func (m *model) startSearch() {
	m.searchSaveCursor = m.cursor
	m.searchSaveMapKey = m.mapFocusKey
	m.searchSaveMapRoot = m.mapFocusRoot
	m.searchActive = false
	m.searchIdx = 0
	m.searchPanelIdx = 0
	m.searchPanelTop = 0
	m.clearTempUnwrap()
	m.prevMode = m.mode
	m.mode = modeSearch
	m.input.Placeholder = "search"
	m.input.Width = max(20, m.width-14)
	// Pre-fill the last searched term (recall). It shows selected/replaceable:
	// the first editing keystroke wipes it, Enter reuses it as-is.
	m.input.SetValue(m.searchLastTerm)
	m.input.CursorEnd()
	m.searchPrefilled = m.searchLastTerm != ""
	m.input.Focus()
	if m.searchPrefilled {
		// Populate the panel for the recalled term (still no board reveal).
		m.runResults(m.searchLastTerm)
		if len(m.searchResults) > 0 {
			m.setSearchStatus(len(m.searchResults))
		}
	}
}

// handleSearchKey drives the incremental search prompt. Enter jumps to the
// selected panel row; Esc cancels; Tab toggles scope; Up/Down (ctrl+p/ctrl+n)
// move the panel selection; every other key edits the query and re-ranks the
// panel — without moving the board.
func (m *model) handleSearchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		// Cancel: restore the pre-search cursor / focus and forget the query.
		m.input.Blur()
		m.mode = modeBoard
		m.searchActive = false
		m.searchQuery = ""
		m.searchResults = nil
		m.clearTempUnwrap()
		if m.mapView {
			// Restore the pre-search zoom root too.
			m.mapFocusRoot = m.searchSaveMapRoot
			m.mapFocusKey = m.searchSaveMapKey
			m.mp = nil
			m.ensureMap()
		} else {
			m.cursor = m.searchSaveCursor
			if m.cursor >= 0 && m.cursor < len(m.positions) {
				m.selSec = m.positions[m.cursor].sec
			}
		}
		m.status = ""
		return m, nil
	case "enter":
		// Select: jump to the highlighted panel row (revealing it) and CLOSE search
		// — picking from the results list is a commit, back to normal navigation.
		// Highlights clear; the term + landing position are remembered so n/N
		// recall can resume stepping later.
		m.input.Blur()
		m.mode = modeBoard
		q := strings.TrimSpace(m.input.Value())
		m.searchQuery = q
		m.runResults(q)
		if q == "" || len(m.searchResults) == 0 {
			m.searchActive = false
			m.searchResults = nil
			m.clearTempUnwrap()
			if q != "" {
				m.status = "no matches"
			}
			return m, nil
		}
		m.searchLastTerm = q
		m.jumpToPanel(m.searchPanelIdx) // reveal + sync searchIdx to the target
		total := len(m.searchResults)
		m.setSearchStatus(total) // leave the tally visible (records searchLastIdx)
		// Close: drop out of search back to plain navigation.
		m.searchActive = false
		m.searchQuery = ""
		m.searchResults = nil
		m.clearTempUnwrap()
		return m, nil
	case "tab":
		// Toggle the search scope live and re-rank, without leaving the prompt or
		// moving the board.
		m.searchScopeAll = !m.searchScopeAll
		m.searchPanelIdx = 0
		m.runResults(strings.TrimSpace(m.input.Value()))
		m.setSearchStatus(len(m.searchResults))
		return m, nil
	case "up", "ctrl+p":
		m.movePanel(-1)
		return m, nil
	case "down", "ctrl+n":
		m.movePanel(1)
		return m, nil
	}
	// A recalled term shows selected: the first editing keystroke replaces it
	// wholesale (rune/space types over it, backspace/delete wipes it).
	if m.searchPrefilled {
		m.searchPrefilled = false
		switch msg.Type {
		case tea.KeyRunes, tea.KeySpace, tea.KeyBackspace, tea.KeyDelete:
			m.input.SetValue("")
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	q := strings.TrimSpace(m.input.Value())
	if q != "" {
		m.searchLastTerm = q
	}
	m.searchPanelIdx = 0
	m.searchPanelTop = 0
	m.runResults(q)
	if len(m.searchResults) > 0 {
		m.setSearchStatus(len(m.searchResults))
	} else if q == "" {
		m.status = ""
	} else {
		m.status = "no matches"
	}
	return m, cmd
}

// movePanel moves the panel selection by dir (no board reveal until Enter).
func (m *model) movePanel(dir int) {
	if len(m.searchResults) == 0 {
		return
	}
	m.searchPanelIdx = ((m.searchPanelIdx+dir)%len(m.searchResults) + len(m.searchResults)) % len(m.searchResults)
	m.clampPanelScroll()
	m.setSearchStatus(len(m.searchResults))
}

// jumpToPanel lands the board on the panel row at idx (ranked order), revealing
// it and syncing searchIdx to the same item's document-order position.
func (m *model) jumpToPanel(idx int) {
	if idx < 0 || idx >= len(m.searchResults) {
		return
	}
	res := m.searchResults[idx]
	m.searchPanelIdx = idx
	m.clampPanelScroll()
	m.jumpToMatch(res.item)
	m.tempUnwrapForMatch(res.item, res.ranges)
	// Sync document-order searchIdx for n/N + resume.
	if docs := searchMatches(m.board, m.searchQuery, m.searchScopeAll); len(docs) > 0 {
		for i, it := range docs {
			if it == res.item {
				m.searchIdx = i
				break
			}
		}
	}
}

// searchNext steps to the next (dir=1) or previous (dir=-1) match in DOCUMENT
// order, wrapping, and keeps the panel's highlighted row in sync. Returns false
// when no search is active and there is no last term to recall.
func (m *model) searchNext(dir int) bool {
	if !m.searchActive {
		if m.searchLastTerm == "" {
			return false
		}
		m.searchQuery = m.searchLastTerm
		m.searchActive = true
		m.runResults(m.searchQuery)
		matches := searchMatches(m.board, m.searchQuery, m.searchScopeAll)
		if len(matches) == 0 {
			m.searchActive = false
			m.status = "no matches"
			return true
		}
		base := ((m.searchLastIdx % len(matches)) + len(matches)) % len(matches)
		m.searchIdx = ((base+dir)%len(matches) + len(matches)) % len(matches)
		m.landDocMatch(matches, m.searchIdx)
		return true
	}
	matches := searchMatches(m.board, m.searchQuery, m.searchScopeAll)
	if len(matches) == 0 {
		m.searchActive = false
		m.status = "no matches"
		return true
	}
	m.searchIdx = ((m.searchIdx+dir)%len(matches) + len(matches)) % len(matches)
	m.landDocMatch(matches, m.searchIdx)
	return true
}

// landDocMatch jumps to the document-order match at idx, syncing the panel's
// highlighted row (by item identity) and applying the temporary unwrap so the
// current match's matched text is visible even if truncation would hide it.
func (m *model) landDocMatch(matches []*board.Item, idx int) {
	it := matches[idx]
	m.jumpToMatch(it)
	// Find this item's ranked panel row for highlight sync + its match ranges.
	var ranges [][2]int
	for i, res := range m.searchResults {
		if res.item == it {
			m.searchPanelIdx = i
			ranges = res.ranges
			break
		}
	}
	m.clampPanelScroll()
	m.tempUnwrapForMatch(it, ranges)
	m.setSearchStatus(len(m.searchResults))
}

// toggleSearchScope flips the working-set/everything scope while results mode is
// live (Tab in results mode, mirroring Tab in the prompt), re-ranks, and re-jumps
// to keep the current selection valid. Bound ahead of section-cycling so Tab
// during search always means "scope", never "next section".
func (m *model) toggleSearchScope() {
	m.searchScopeAll = !m.searchScopeAll
	m.runResults(m.searchQuery)
	if len(m.searchResults) == 0 {
		m.searchActive = false
		m.clearTempUnwrap()
		m.status = "no matches"
		return
	}
	if m.searchPanelIdx >= len(m.searchResults) {
		m.searchPanelIdx = 0
	}
	m.jumpToPanel(m.searchPanelIdx)
	m.setSearchStatus(len(m.searchResults))
}

// clearSearchOnEsc implements persistent results mode: after Enter, highlighting
// and the panel stay live through ordinary navigation; only Esc (or a new `/`)
// clears them. Returns true when it consumed an Esc that dropped search.
func (m *model) clearSearchOnEsc(key string) bool {
	if key == "esc" && m.searchActive {
		m.searchActive = false
		m.searchQuery = ""
		m.searchResults = nil
		m.clearTempUnwrap()
		m.status = ""
		return true
	}
	return false
}

// setSearchStatus writes the "k/N matches" tally (1-based panel selection),
// annotated with the active scope. It remembers the document-order index a later
// n/N recall should resume from.
func (m *model) setSearchStatus(total int) {
	m.searchLastIdx = m.searchIdx
	scope := " (working set)"
	if m.searchScopeAll {
		scope = " (all)"
	}
	cur := m.searchPanelIdx
	if total == 0 {
		cur = -1
	}
	hits := ""
	// A term can occur several times inside one node; surface the total literal
	// occurrences when it exceeds the node count so "3/12 matches · 17 hits".
	if h := searchHitCount(m.searchResults, m.searchQueryForStatus()); h > total {
		hits = fmt.Sprintf(" · %d hits", h)
	}
	m.status = fmt.Sprintf("%d/%d matches%s%s · tab scope", cur+1, total, scope, hits)
}

// searchQueryForStatus returns the query the status tally should count against:
// the live prompt text while typing, else the confirmed query in results mode.
func (m *model) searchQueryForStatus() string {
	if m.mode == modeSearch {
		return m.input.Value()
	}
	return m.searchQuery
}

// clampPanelScroll keeps the selected panel row within the visible window.
func (m *model) clampPanelScroll() {
	h := m.panelHeight()
	if m.searchPanelIdx < m.searchPanelTop {
		m.searchPanelTop = m.searchPanelIdx
	}
	if m.searchPanelIdx >= m.searchPanelTop+h {
		m.searchPanelTop = m.searchPanelIdx - h + 1
	}
	if m.searchPanelTop < 0 {
		m.searchPanelTop = 0
	}
}

// panelHeight is the number of result rows the panel shows at once.
func (m *model) panelHeight() int {
	h := m.height / 3
	if h < 3 {
		h = 3
	}
	if h > 10 {
		h = 10
	}
	return h
}

// tempUnwrapForMatch temporarily expands the current match's node when its
// matched substring would be hidden by truncation, so n/N (and Enter) always
// reveal the matched text. Any prior temporary unwrap is restored first. It only
// unwraps when the node is not already user-expanded, and records the key so the
// original (wrapped) state is restored when the match stops being current.
func (m *model) tempUnwrapForMatch(it *board.Item, ranges [][2]int) {
	m.clearTempUnwrap()
	if it == nil || len(ranges) == 0 {
		return
	}
	end := 0
	for _, r := range ranges {
		if r[1] > end {
			end = r[1]
		}
	}
	if it.Urgent {
		end += 3 // "!! " prefix shifts the displayed text right
	} else if it.Pinned {
		end += 5 // "!pin " prefix
	}
	if m.mapView {
		key, ok := m.itemMapKey(it)
		if !ok || end <= mapNodeMaxWidth || m.mapExpanded[key] {
			return
		}
		if m.mapExpanded == nil {
			m.mapExpanded = map[string]bool{}
		}
		m.mapExpanded[key] = true
		m.searchUnwrapKey = key
		m.searchUnwrapMap = true
		m.mp = nil
		return
	}
	raw := it.Raw()
	avail := m.width - m.itemHang(it)
	if end < avail || m.listExpanded[raw] {
		return
	}
	if m.listExpanded == nil {
		m.listExpanded = map[string]bool{}
	}
	m.listExpanded[raw] = true
	m.searchUnwrapKey = raw
	m.searchUnwrapMap = false
}

// clearTempUnwrap restores a node unwrapped only for the current match.
func (m *model) clearTempUnwrap() {
	if m.searchUnwrapKey == "" {
		return
	}
	if m.searchUnwrapMap {
		delete(m.mapExpanded, m.searchUnwrapKey)
		m.mp = nil
	} else {
		delete(m.listExpanded, m.searchUnwrapKey)
	}
	m.searchUnwrapKey = ""
	m.searchUnwrapMap = false
}

// itemHang returns the outline hang column (gutter + indent + marker) for it,
// matching renderItem's layout, so tempUnwrapForMatch can tell whether a matched
// substring falls beyond the truncation width.
func (m *model) itemHang(it *board.Item) int {
	depth := 0
	for _, p := range m.positions {
		if p.item == it {
			depth = p.depth
			break
		}
	}
	return 2 + depth*2 + len("[ ]") + 1
}

// jumpToMatch moves the active view onto item it, revealing it through any
// collapsed section (outline) or folded subtree (map).
func (m *model) jumpToMatch(it *board.Item) {
	if it == nil {
		return
	}
	if m.mapView {
		key, ok := m.itemMapKey(it)
		if !ok {
			return
		}
		m.revealKey(key)
		m.mapFocusKey = key
		if m.mapFocusRoot != "" && !keyWithin(key, m.mapFocusRoot) {
			m.mapFocusRoot = ""
		}
		m.mp = nil
		m.ensureMap()
		return
	}
	// Outline: reveal a collapsed owning section and un-fold any collapsed
	// ancestor items on the path to the match, then land the cursor.
	title := m.board.SectionTitleOf(it)
	changed := false
	if title != "" && m.sectionCollapsed(title) {
		m.setCollapsed(title, false)
		changed = true
	}
	if title != "" {
		for p := m.board.ParentOf(it); p != nil; p = m.board.ParentOf(p) {
			if m.itemFolded(title, p) {
				m.setItemFolded(title, p, false)
				changed = true
			}
		}
	}
	if changed {
		m.rebuildPositions()
	}
	m.setCursorToItem(it)
}
