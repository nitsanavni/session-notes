package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/nitsanavni/session-notes/internal/board"
)

// Incremental substring search, shared by the outline and map views. `/` opens a
// prompt; as the query changes the cursor/focus jumps to the first match. Enter
// confirms (leaving n/N to step next/previous, wrapping), Esc cancels and
// restores the pre-search cursor. Matches hidden behind a collapsed section
// (outline) or a folded subtree (map) are revealed on the way in, reusing the
// same auto-expand machinery as ordinary navigation.

// searchScopeExcluded reports whether a section is outside the default search
// scope (the "working set"). Archive and Log are noisy, low-signal sections the
// user rarely means to jump into, so search skips them unless the scope is
// toggled to "everything".
func searchScopeExcluded(title string) bool {
	return title == "Archive" || title == "Log"
}

// searchMatches returns every item whose display text contains query as a
// case-insensitive substring, in document order across all sections, replies
// included. An empty (or whitespace-only) query matches nothing. When all is
// false (the default working-set scope) the Archive and Log sections are
// skipped so a search never drags the cursor — or the Log ring — into them.
func searchMatches(b *board.Board, query string, all bool) []*board.Item {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" || b == nil {
		return nil
	}
	var out []*board.Item
	var walk func(items []*board.Item)
	walk = func(items []*board.Item) {
		for _, it := range items {
			if it.IsItem() && strings.Contains(strings.ToLower(it.DisplayText()), q) {
				out = append(out, it)
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

// startSearch opens the incremental search prompt over whichever view is active.
// It captures the pre-search cursor (outline) or focus key (map) so Esc can
// restore it.
func (m *model) startSearch() {
	m.searchSaveCursor = m.cursor
	m.searchSaveMapKey = m.mapFocusKey
	m.searchSaveMapRoot = m.mapFocusRoot
	m.searchActive = false
	m.searchIdx = 0
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
		// Live-preview the recalled term straight away so its matches highlight
		// and the view jumps to the first hit before any keystroke.
		matches := searchMatches(m.board, m.searchLastTerm, m.searchScopeAll)
		if len(matches) > 0 {
			m.searchIdx = 0
			m.jumpToMatch(matches[0])
			m.setSearchStatus(len(matches))
		}
	}
}

// handleSearchKey drives the incremental search prompt. Every keystroke that is
// not enter/esc feeds the text input, then re-runs the match and jumps to the
// first hit so the view tracks the query live.
func (m *model) handleSearchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		// Cancel: restore the pre-search cursor / focus and forget the query.
		m.input.Blur()
		m.mode = modeBoard
		m.searchActive = false
		m.searchQuery = ""
		if m.mapView {
			// Restore the pre-search zoom root too: jumpToMatch may have cleared
			// mapFocusRoot for a match outside the focused subtree.
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
		// Confirm: keep the query live for n/N and report the tally.
		m.input.Blur()
		m.mode = modeBoard
		q := strings.TrimSpace(m.input.Value())
		m.searchQuery = q
		matches := searchMatches(m.board, q, m.searchScopeAll)
		if q == "" || len(matches) == 0 {
			m.searchActive = false
			if q != "" {
				m.status = "no matches"
			}
			return m, nil
		}
		m.searchActive = true
		m.searchLastTerm = q
		m.setSearchStatus(len(matches))
		return m, nil
	case "tab":
		// Toggle the search scope (working set <-> everything) live, without
		// leaving the prompt, and re-run the current query so the tally + jump
		// update immediately. Tab has no other meaning inside the search prompt.
		m.searchScopeAll = !m.searchScopeAll
		q := strings.TrimSpace(m.input.Value())
		matches := searchMatches(m.board, q, m.searchScopeAll)
		if len(matches) > 0 {
			m.searchIdx = 0
			m.jumpToMatch(matches[0])
		}
		m.setSearchStatus(len(matches))
		return m, nil
	}
	// A recalled term shows selected: the first editing keystroke replaces it
	// wholesale (rune/space types over it, backspace/delete wipes it). Cursor
	// moves just deselect without clearing.
	if m.searchPrefilled {
		m.searchPrefilled = false
		switch msg.Type {
		case tea.KeyRunes, tea.KeySpace, tea.KeyBackspace, tea.KeyDelete:
			m.input.SetValue("")
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	// Live: jump to the first match for the current query.
	q := strings.TrimSpace(m.input.Value())
	if q != "" {
		m.searchLastTerm = q
	}
	matches := searchMatches(m.board, q, m.searchScopeAll)
	if len(matches) > 0 {
		m.searchIdx = 0
		m.jumpToMatch(matches[0])
		m.setSearchStatus(len(matches))
	} else if q == "" {
		m.status = ""
	} else {
		m.status = "no matches"
	}
	return m, cmd
}

// searchNext steps to the next (dir=1) or previous (dir=-1) match, wrapping. It
// recomputes the match set from the stored query each time so it stays correct
// across board edits made between steps. Returns false when no search is active.
func (m *model) searchNext(dir int) bool {
	if !m.searchActive {
		// Re-activate the last term when there's no live search (n/N recall).
		if m.searchLastTerm == "" {
			return false
		}
		m.searchQuery = m.searchLastTerm
		m.searchActive = true
		matches := searchMatches(m.board, m.searchQuery, m.searchScopeAll)
		if len(matches) == 0 {
			m.searchActive = false
			m.status = "no matches"
			return true
		}
		// Resume from where the last search session left off: step one from the
		// remembered index (n -> next, N -> previous), rather than jumping to the top.
		base := ((m.searchLastIdx % len(matches)) + len(matches)) % len(matches)
		m.searchIdx = ((base+dir)%len(matches) + len(matches)) % len(matches)
		m.jumpToMatch(matches[m.searchIdx])
		m.setSearchStatus(len(matches))
		return true
	}
	matches := searchMatches(m.board, m.searchQuery, m.searchScopeAll)
	if len(matches) == 0 {
		m.searchActive = false
		m.status = "no matches"
		return true
	}
	m.searchIdx = ((m.searchIdx+dir)%len(matches) + len(matches)) % len(matches)
	m.jumpToMatch(matches[m.searchIdx])
	m.setSearchStatus(len(matches))
	return true
}

// clearSearchOnEsc implements persistent results mode. After `enter` confirms a
// search, the match highlighting and tally stay live through ordinary navigation
// (moves, folds, edits) — `n`/`N` step matches, `/` reopens the prompt — and
// ONLY Esc (or a new `/`) clears them. When search is active and Esc is pressed,
// it clears searchActive + the stored query and returns true so the caller
// consumes the key: Esc drops the search instead of quitting / focusing out.
// Every other key is a no-op here, so nothing else silently exits search. A
// no-op while the prompt is open (modeSearch never reaches the board/map key
// handlers); the prompt's own Esc is handled in handleSearchKey.
func (m *model) clearSearchOnEsc(key string) bool {
	if key == "esc" && m.searchActive {
		m.searchActive = false
		m.searchQuery = ""
		m.status = ""
		return true
	}
	return false
}

// setSearchStatus writes the "k/N matches" tally (1-based current index),
// annotated with the active scope so the user can see whether Archive/Log are
// in play (default working set stays unlabelled to keep the common case quiet).
func (m *model) setSearchStatus(total int) {
	m.searchLastIdx = m.searchIdx // remember where a later n/N recall should resume
	scope := ""
	if m.searchScopeAll {
		scope = " (all)"
	} else {
		scope = " (working set)"
	}
	m.status = fmt.Sprintf("%d/%d matches%s · tab scope", m.searchIdx+1, total, scope)
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
	// Outline: reveal a collapsed owning section, then land the cursor.
	if title := m.board.SectionTitleOf(it); title != "" && m.sectionCollapsed(title) {
		m.setCollapsed(title, false)
		m.rebuildPositions()
	}
	m.setCursorToItem(it)
}
