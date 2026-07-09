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

// searchMatches returns every item whose display text contains query as a
// case-insensitive substring, in document order across all sections, replies
// included. An empty (or whitespace-only) query matches nothing.
func searchMatches(b *board.Board, query string) []*board.Item {
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
	m.searchActive = false
	m.searchIdx = 0
	m.prevMode = m.mode
	m.mode = modeSearch
	m.input.Placeholder = "search"
	m.input.Width = max(20, m.width-14)
	m.input.SetValue("")
	m.input.Focus()
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
		matches := searchMatches(m.board, q)
		if q == "" || len(matches) == 0 {
			m.searchActive = false
			if q != "" {
				m.status = "no matches"
			}
			return m, nil
		}
		m.searchActive = true
		m.setSearchStatus(len(matches))
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	// Live: jump to the first match for the current query.
	q := strings.TrimSpace(m.input.Value())
	matches := searchMatches(m.board, q)
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
		return false
	}
	matches := searchMatches(m.board, m.searchQuery)
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

// setSearchStatus writes the "k/N matches" tally (1-based current index).
func (m *model) setSearchStatus(total int) {
	m.status = fmt.Sprintf("%d/%d matches", m.searchIdx+1, total)
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
