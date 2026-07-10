package tui

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/nitsanavni/session-notes/internal/board"
)

var (
	styleTitle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("245"))
	styleSection    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("110"))
	styleSectionSel = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81")).Underline(true)
	styleCursor     = lipgloss.NewStyle().Foreground(lipgloss.Color("231")).Bold(true)
	styleDone       = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	styleUrgent     = lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Bold(true) // brick red, matching the web --urgent (was orange 214)
	stylePin        = lipgloss.NewStyle().Foreground(lipgloss.Color("108"))            // muted teal — calmer than urgent
	styleBlocked    = lipgloss.NewStyle().Foreground(lipgloss.Color("174"))
	styleInProg     = lipgloss.NewStyle().Foreground(lipgloss.Color("150"))
	styleDim        = lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
	styleHelpBar    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	styleStatus     = lipgloss.NewStyle().Foreground(lipgloss.Color("179"))
	styleMarker     = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	styleLink       = lipgloss.NewStyle().Foreground(lipgloss.Color("75")).Underline(true)

	styleAuthorClaude = lipgloss.NewStyle().Foreground(lipgloss.Color("208")) // orange — claude's turn
	styleAuthorUser   = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))  // blue — user's turn

	// styleSearch tints a whole item/node that matches the active search — a
	// subtle green foreground, distinct from the reverse-video cursor bar, applied
	// only when the item is not itself the current selection/focus.
	styleSearch = lipgloss.NewStyle().Foreground(lipgloss.Color("120"))
	// searchMatchFG is the accent foreground for the matched SUBSTRING within an
	// item's text (stronger than the whole-line tint): a bold, underlined bright
	// yellow span layered over whichever base style the segment already carries.
	searchMatchFG = lipgloss.Color("228")
)

// linkDisplayRe matches [[name]] spans for display-only highlighting. It
// mirrors board.ExtractLinks' delimiters but is kept separate: this one runs
// per already-wrapped, plain-text display row, not over raw item text.
var linkDisplayRe = regexp.MustCompile(`\[\[[^\[\]]+\]\]`)

// authorRe matches a leading author token — "claude:" or "user:" — at the very
// start of an item's display text, tolerating a "!! " urgent prepend and/or a
// "HH:MM " log timestamp ahead of it. Only the (claude|user): capture (colon
// included) is tinted; the match's full length tells wrapRows where the token
// ends so the rest of the row renders as a plain sibling segment.
var authorRe = regexp.MustCompile(`^(?:!!\s+)?(?:\d{2}:\d{2}\s+)?(claude|user):`)

// searchHighlight returns the lowercased query whose matches should be
// highlighted in the current render, or "" when nothing is highlighted. While
// the prompt is open (modeSearch) the live input text drives it so highlighting
// tracks the query keystroke-by-keystroke; once the prompt is confirmed the
// stored searchQuery drives it for as long as the persistent results mode stays
// live. Only Esc (or a new `/`) clears searchActive and so ends highlighting;
// ordinary navigation keeps it (see clearSearchOnEsc / handleBoardKey / handleMapKey).
func (m *model) searchHighlight() string {
	if m.mode == modeSearch {
		return strings.ToLower(strings.TrimSpace(m.input.Value()))
	}
	if m.searchActive {
		return strings.ToLower(strings.TrimSpace(m.searchQuery))
	}
	return ""
}

// itemMatchesSearch reports whether it's display text contains the (already
// lowercased) query, mirroring searchMatches' case-insensitive substring test.
func itemMatchesSearch(it *board.Item, query string) bool {
	return query != "" && strings.Contains(strings.ToLower(it.DisplayText()), query)
}

func statusMarker(s board.Status) string {
	switch s {
	case board.StatusOpen:
		return styleMarker.Render("[ ]")
	case board.StatusInProgress:
		return styleInProg.Render("[>]")
	case board.StatusDone:
		return styleDone.Render("[x]")
	case board.StatusBlocked:
		return styleBlocked.Render("[?]")
	default:
		return " " + styleMarker.Render("·") + " "
	}
}

func (m *model) View() string {
	if m.mapView {
		switch m.mode {
		case modeBoard, modeMapAdd, modeMapEdit, modeMapRename, modeMapFeedback, modeInputTitle, modeSearch:
			return m.viewMap()
		}
	}
	switch m.mode {
	case modeDash:
		return m.viewDash()
	case modePicker:
		return m.viewPicker()
	case modeHelp:
		return m.viewHelp()
	case modeHistory:
		return m.viewHistory()
	case modeAddSections:
		return m.viewAddSections()
	case modeLinkPick:
		return m.viewLinkPick()
	}
	return m.viewBoard()
}

func (m *model) viewBoard() string {
	if m.board == nil {
		return "no board\n"
	}
	var lines []string
	cursorLine := 0
	cursorEnd := 0     // exclusive end of the selected block: past its rows + whole subtree
	selHeaderLine := 0 // display row of the active section's header

	// Title text (Title -> Session -> path fallback) is held aside and rendered
	// as a sticky header below, outside the scrollable body — see boardHeader.
	title := m.path
	// A path fallback carries its meaning in the tail (the filename), so it is
	// truncated from the left; a frontmatter title leads with the name and is
	// truncated from the right (dropping the trailing dim Cwd first).
	titleTail := true
	if fm := m.board.Frontmatter; fm.Title != "" {
		// A frontmatter title replaces the session id as the heading, so surface
		// a shortened id (first 8 chars) dimmed alongside it for reference.
		id := shortSessionID(fm.Session)
		if id != "" {
			id = " " + styleDim.Render(id)
		}
		title = fmt.Sprintf("%s%s  %s", fm.Title, id, styleDim.Render(fm.Cwd))
		titleTail = false
	} else if fm.Session != "" {
		title = fmt.Sprintf("%s  %s", fm.Session, styleDim.Render(fm.Cwd))
		titleTail = false
	}

	posIdx := 0
	for si, s := range m.board.Sections {
		st := styleSection
		if si == m.selSec {
			st = styleSectionSel
		}
		// The header is itself a cursor stop; posIdx currently addresses it.
		selectedHeader := posIdx == m.cursor
		prefix := "  "
		if selectedHeader {
			prefix = styleCursor.Render("> ")
			cursorLine = len(lines)
		}
		header := prefix + st.Render("## "+s.Title)
		// Subtle section-index hint (1..9) so number-key jumps are discoverable.
		if si < 9 {
			header += " " + styleDim.Render(fmt.Sprintf("%d", si+1))
		}
		// A collapsed section renders as just its header plus a dim count of
		// the items it hides. Archive keeps its "(N archived)" wording; other
		// sections use a generic "(N items)" hint.
		collapsed := m.sectionCollapsed(s.Title)
		if collapsed {
			hint := fmt.Sprintf("(%d items)", sectionItemCount(s))
			if s.Title == board.ArchiveTitle {
				hint = fmt.Sprintf("(%d archived)", m.board.ArchivedCount())
			}
			header += " " + styleDim.Render(hint)
		}
		// Every section header renders, including empty ones, so they stay
		// visible and reachable. Remember the active header's row for scrolling.
		if si == m.selSec {
			selHeaderLine = len(lines)
		}
		lines = append(lines, header)
		posIdx++
		if collapsed {
			// Keep a blank line below the collapsed header (the section's real
			// trailing blank is hidden along with its items).
			lines = append(lines, "")
			continue
		}
		// Walk the item tree depth-first; children render indented below their
		// parent for a threaded, forum-style look. posIdx tracks navigable items
		// in the same order rebuildPositions uses so the cursor lines up.
		var walk func(items []*board.Item, depth int)
		walk = func(items []*board.Item, depth int) {
			for _, it := range items {
				if !it.IsItem() {
					// Continuation / stray raw lines render verbatim (internal
					// spacing preserved for ASCII drawings) and are clipped, not
					// wrapped, at the viewport edge. They are not nav stops, so
					// they carry no cursor gutter marker.
					if strings.TrimSpace(it.Raw()) == "" {
						lines = append(lines, "")
					} else {
						lines = append(lines, continuationRow(it.Raw(), m.width))
					}
					walk(it.Children, depth+1)
					continue
				}
				selected := posIdx == m.cursor
				if selected {
					cursorLine = len(lines)
				}
				// A wrapped item spans several rows; the cursor line points at the
				// first so scrolling keeps the whole block anchored.
				lines = append(lines, m.renderItem(it, selected, depth)...)
				posIdx++
				walk(it.Children, depth+1)
				if selected {
					// Exclusive end: past the item's rows + its whole subtree.
					cursorEnd = len(lines)
				}
			}
		}
		walk(s.Items, 0)
	}
	// Normalize the unset case: cursor on a section header (including a collapsed
	// one) or no selection leaves cursorEnd at zero.
	if cursorEnd <= cursorLine {
		cursorEnd = cursorLine + 1
	}

	// Footer: input line or key hints, plus status.
	footer := m.viewFooter()
	footerH := lipgloss.Height(footer)

	// Reserve a sticky header: title row + a blank separator (preserving the
	// current rhythm), degrading to just the title row on a tiny viewport.
	headerH := 2
	if m.height < 10 {
		headerH = 1
	}

	// Scroll the body so the cursor stays visible.
	bodyH := m.height - headerH - footerH
	if bodyH < 3 {
		bodyH = 3
	}
	blockH := cursorEnd - cursorLine
	visBlockH := min(blockH, bodyH)
	// 1. cursor row never scrolls off the top
	if cursorLine < m.scroll {
		m.scroll = cursorLine
	}
	// 2. keep the block's fitting portion on screen when it hangs off the bottom
	//    (1-row block reduces to the old cursorLine>=m.scroll+bodyH -> cursorLine-bodyH+1 clamp)
	if cursorLine+visBlockH > m.scroll+bodyH {
		m.scroll = cursorLine + visBlockH - bodyH
	}
	// 3. header preference — reveal the active section's header, but only when the
	//    whole fitting portion of the block still fits below it. A block taller
	//    than the viewport never snaps to the header (this reduces to
	//    cursorLine<=selHeaderLine, true only on the header stop itself).
	if selHeaderLine < m.scroll && cursorLine+visBlockH <= selHeaderLine+bodyH {
		m.scroll = selHeaderLine
	}
	// At the very first item (or an empty board), scroll fully to the top so the
	// title and any empty section headers above it become visible.
	if m.cursor <= 0 {
		m.scroll = 0
	}
	if m.scroll > len(lines)-1 {
		m.scroll = max(0, len(lines)-1)
	}
	end := min(len(lines), m.scroll+bodyH)
	body := strings.Join(lines[m.scroll:end], "\n")
	// Pad so the footer sits at the bottom.
	if pad := bodyH - (end - m.scroll); pad > 0 {
		body += strings.Repeat("\n", pad)
	}

	// Sticky title row, with a right-aligned scroll readout only when the board
	// is taller than the visible body (short board -> title looks as before).
	var readout string
	if len(lines) > bodyH {
		readout = scrollReadout(m.scroll, m.scroll+1, end, len(lines))
	}
	header := boardHeader(title, readout, m.width, titleTail)
	if headerH == 2 {
		header += "\n"
	}
	return header + "\n" + body + "\n" + footer
}

// sectionItemCount counts the parsed items in a section's whole tree (replies
// included) — the number of nav stops its collapsed header hides.
func sectionItemCount(s *board.Section) int {
	var count func(items []*board.Item) int
	count = func(items []*board.Item) int {
		n := 0
		for _, it := range items {
			if it.IsItem() {
				n++
			}
			n += count(it.Children)
		}
		return n
	}
	return count(s.Items)
}

// scrollReadout renders the "▲ 12–28/96 ▼" position indicator embedded, flush
// right, in the sticky title row. The up chevron shows only when scrolled down
// from the top; the down chevron only when rows remain below the window. Both
// slots keep a fixed cell so the readout width doesn't jitter as you scroll.
func scrollReadout(scroll, start, end, total int) string {
	up := " "
	if scroll > 0 {
		up = "▲"
	}
	down := " "
	if end < total {
		down = "▼"
	}
	return styleDim.Render(fmt.Sprintf("%s %d–%d/%d %s", up, start, end, total, down))
}

// shortSessionID returns the first 8 characters of a session id (its whole
// value when shorter), used to tag a titled board's header with a compact,
// dimmed reference to its session. Empty in, empty out.
func shortSessionID(session string) string {
	if len(session) <= 8 {
		return session
	}
	return session[:8]
}

// boardHeader renders the sticky title row: the board title, truncated to fit,
// with the optional scroll readout pinned flush right. Widths are measured with
// lipgloss.Width so the ANSI styling embedded in both strings never throws off
// the layout. Below ~20 columns the readout is dropped and only the title (also
// truncated) remains.
func boardHeader(title, readout string, width int, tail bool) string {
	if width < 1 {
		return styleTitle.Render(title)
	}
	if readout == "" || width < 20 {
		return styleTitle.Render(truncTitle(title, width, tail))
	}
	rw := lipgloss.Width(readout)
	avail := width - rw - 2
	if avail < 1 {
		avail = 1
	}
	t := styleTitle.Render(truncTitle(title, avail, tail))
	gap := width - lipgloss.Width(t) - rw
	if gap < 1 {
		gap = 1
	}
	return t + strings.Repeat(" ", gap) + readout
}

// truncTitle fits a title into w cells. When tail is true (path fallback), it
// keeps the tail — the filename — dropping leading directories behind a leading
// ellipsis; otherwise it keeps the leading name and drops the trailing text.
func truncTitle(s string, w int, tail bool) string {
	if !tail || lipgloss.Width(s) <= w {
		return ansi.Truncate(s, w, "")
	}
	if w <= 1 {
		return "…"
	}
	return "…" + ansi.TruncateLeft(s, lipgloss.Width(s)-(w-1), "")
}

// renderItem renders a bullet as one or more display rows: its text is
// soft-wrapped to the viewport width, and continuation rows are indented so
// they align under where the text starts (not column 0). The item's styling —
// including the selection highlight — is applied to every wrapped row, so the
// cursor covers the whole block. Wrapping is display-only; the stored text and
// file content are untouched.
func (m *model) renderItem(it *board.Item, selected bool, depth int) []string {
	prefix := "  "
	if selected {
		prefix = styleCursor.Render("> ")
	}
	// Indent children under their parent for the threaded, forum-style feel.
	indent := strings.Repeat("  ", depth)
	head := prefix + indent + statusMarker(it.Status) + " "
	// Visible column where the text begins: 2 gutter + 2/level indent + 3 marker
	// + 1 space. Continuation rows hang-indent to this column.
	hang := 2 + depth*2 + len("[ ]") + 1

	text := it.DisplayText()
	if it.Urgent {
		text = "!! " + text
	} else if it.Pinned {
		text = "!pin " + text
	}
	// Status/urgency set the base foreground + decoration, independent of
	// selection.
	var style lipgloss.Style
	switch {
	case it.Status == board.StatusDone: // greyed out wins over urgent once done
		style = styleDone
	case it.Urgent:
		style = styleUrgent
	case it.Pinned: // subtle, calmer than urgent
		style = stylePin
	case depth > 0: // replies read slightly dimmer than top-level items
		style = styleDim
	default:
		style = lipgloss.NewStyle()
	}
	// Selection is a uniform overlay applied after, identical for every status:
	// a loud, theme-adaptive reverse-video bar (a fixed background washes out on
	// light terminals). Normalize the foreground to a bright constant first so
	// the reversed bar is high-contrast on every status — a done item's dim 240
	// would otherwise reverse into a low-contrast bar.
	if selected {
		style = style.Foreground(lipgloss.Color("252")).Reverse(true).Bold(true)
	}
	// Search highlight: a matching item that is not itself the selection gets a
	// subtle whole-line tint (styleSearch); the matched substring is accented on
	// every matching item (selected or not) inside wrapRows.
	query := m.searchHighlight()
	matched := itemMatchesSearch(it, query)
	if matched && !selected {
		style = style.Foreground(styleSearch.GetForeground())
	}
	hlQuery := ""
	if matched {
		hlQuery = query
	}
	return wrapRows(head, text, hang, m.width, style, m.listExpanded[it.Raw()], hlQuery)
}

// wrapRows renders an item's text as display rows. By default the text is a
// single line, truncated with an ellipsis at the viewport width; when wrap is
// set (the `w` toggle) it is soft-wrapped into multiple rows. The first row
// carries head (gutter + marker); continuation rows are padded with hang spaces
// so wrapped text lines up under the text start. style is applied per row so the
// whole block reads as one item.
func wrapRows(head, text string, hang, width int, style lipgloss.Style, wrap bool, query string) []string {
	avail := width - hang
	if avail < 1 {
		avail = 1
	}
	var rows []string
	if wrap {
		rows = strings.Split(ansi.Wrap(text, avail, ""), "\n")
	} else {
		rows = []string{ansi.Truncate(text, avail, "…")}
	}
	out := make([]string, 0, len(rows))
	pad := strings.Repeat(" ", hang)
	for i, r := range rows {
		if i == 0 {
			out = append(out, head+renderRow0(r, style, query))
		} else {
			out = append(out, pad+renderRowWithLinks(r, style, query))
		}
	}
	return out
}

// highlightSegment renders a plain-text segment in style, accenting every
// occurrence of query (already lowercased; "" means no highlight) with a bold,
// underlined bright-yellow span derived from style so it inherits any selection
// background. Occurrences are found case-insensitively over the segment's own
// bytes; the display text is ASCII-oriented so byte offsets track columns.
func highlightSegment(seg string, style lipgloss.Style, query string) string {
	if query == "" || !strings.Contains(strings.ToLower(seg), query) {
		return style.Render(seg)
	}
	matchStyle := style.Foreground(searchMatchFG).Bold(true).Underline(true)
	low := strings.ToLower(seg)
	var b strings.Builder
	i := 0
	for i < len(seg) {
		j := strings.Index(low[i:], query)
		if j < 0 {
			b.WriteString(style.Render(seg[i:]))
			break
		}
		start := i + j
		end := start + len(query)
		if start > i {
			b.WriteString(style.Render(seg[i:start]))
		}
		b.WriteString(matchStyle.Render(seg[start:end]))
		i = end
	}
	return b.String()
}

// renderRow0 renders the first wrapped row of an item. If the row opens with an
// author token — "claude:" or "user:", tolerating a "!! " urgent prepend or a
// "HH:MM " log timestamp ahead of it — that token (colon included) is tinted in
// the author color as its own sibling segment; any prefix before it and the
// remainder render in the base style (with [[links]] highlighted). The tint
// style is derived from the base style so it inherits the selection background
// and any dim. Continuation rows never carry an author token, so they
// call renderRowWithLinks directly.
func renderRow0(row string, style lipgloss.Style, query string) string {
	loc := authorRe.FindStringSubmatchIndex(row)
	if loc == nil {
		return renderRowWithLinks(row, style, query)
	}
	tokStart, tokEnd := loc[2], loc[3]
	authorStyle := style.Foreground(styleAuthorUser.GetForeground())
	if strings.HasPrefix(row[tokStart:tokEnd], "claude") {
		authorStyle = style.Foreground(styleAuthorClaude.GetForeground())
	}
	var b strings.Builder
	if tokStart > 0 {
		b.WriteString(highlightSegment(row[:tokStart], style, query))
	}
	b.WriteString(highlightSegment(row[tokStart:tokEnd], authorStyle, query))
	if tokEnd < len(row) {
		b.WriteString(renderRowWithLinks(row[tokEnd:], style, query))
	}
	return b.String()
}

// renderRowWithLinks renders one already-wrapped, plain-text row, styling
// [[wiki-link]] spans in a distinct link color while the rest of the row
// keeps the caller's style. Wrapping happens on plain text first (see
// wrapRows) specifically so this can render each segment as its own
// self-contained styled string — concatenating sibling Render calls, never
// nesting one inside another, which would let an inner reset code clobber the
// outer style for the remainder of the row.
func renderRowWithLinks(row string, style lipgloss.Style, query string) string {
	locs := linkDisplayRe.FindAllStringIndex(row, -1)
	if locs == nil {
		return highlightSegment(row, style, query)
	}
	linkStyle := style.Foreground(styleLink.GetForeground()).Underline(true)
	var b strings.Builder
	last := 0
	for _, loc := range locs {
		if loc[0] > last {
			b.WriteString(highlightSegment(row[last:loc[0]], style, query))
		}
		b.WriteString(highlightSegment(row[loc[0]:loc[1]], linkStyle, query))
		last = loc[1]
	}
	if last < len(row) {
		b.WriteString(highlightSegment(row[last:], style, query))
	}
	return b.String()
}

// continuationRow renders a continuation (or stray raw) line for display:
// verbatim after a two-column gutter, horizontally clipped to the viewport
// width so long ASCII-art lines never wrap or break the layout.
func continuationRow(raw string, width int) string {
	const gutter = "  "
	avail := width - len(gutter)
	if avail < 1 {
		avail = 1
	}
	return gutter + styleDim.Render(ansi.Truncate(raw, avail, ""))
}

func (m *model) viewFooter() string {
	switch m.mode {
	case modeInputAdd, modeInputEdit, modeInputLog, modeInputReply, modeInputCustomSection, modeInputRename, modeInputTitle:
		label := map[mode]string{
			modeInputAdd:           "add",
			modeInputEdit:          "edit",
			modeInputLog:           "log",
			modeInputReply:         "reply",
			modeInputCustomSection: "section",
			modeInputRename:        "rename section",
			modeInputTitle:         "title",
		}[m.mode]
		// Reply mode's human message is always authored "user:", so tint its
		// label user-blue; add/edit/log/section keep the neutral status color.
		labelStyle := styleStatus
		if m.mode == modeInputReply {
			labelStyle = styleAuthorUser
		}
		return labelStyle.Render(label+": ") + m.input.View()
	}
	if m.mode == modeSearch {
		return styleStatus.Render("search: ") + m.input.View()
	}
	hints := "j/k/arrows move · g/G top/bottom · tab section · 1-9 jump · / search · n/N next/prev · a add · A section · R reply · F fork · space status · b blocked · ! urgent · p pin · d archive · D delete · enter collapse · z zoom · w wrap · e edit/rename section · E editor · T title · o open link · y copy path · m map · u undo · ctrl+r redo · L log · H history · r reload · B boards · ? help · q quit"
	line := styleHelpBar.Render(hints)
	if m.status != "" {
		line = styleStatus.Render(m.status) + "  " + line
	}
	return line
}

func (m *model) viewHelp() string {
	styleKey := lipgloss.NewStyle().Foreground(lipgloss.Color("81")).Bold(true)
	keys := [][2]string{
		{"j / k, up / down", "move cursor"},
		{"tab / shift-tab", "next / previous section"},
		{"1 - 9", "jump to the Nth section"},
		{"/", "incremental search (both views); n/N next/prev, resuming the last term after Esc; tab toggles scope (working set vs Archive+Log)"},
		{"a", "add item to current section"},
		{"A", "add sections (multi-select overlay)"},
		{"R", "reply in thread (sibling on a reply; starts the thread on an item)"},
		{"F", "fork a sub-thread under the message at the cursor"},
		{"space", "cycle status [ ] -> [>] -> [x]"},
		{"b", "toggle blocked [?]"},
		{"g / G", "jump to first / last visible stop"},
		{"!", "toggle urgent (!!)"},
		{"p", "toggle pin (!pin) — re-injected into Claude's context on a cadence"},
		{"d", "archive item (or whole section) into ## Archive"},
		{"D", "hard-delete item (or whole section) from the file"},
		{"enter", "collapse / expand the section under the cursor (on its header)"},
		{"l", "on a section: expand + land on first item; on an item: descend to first child"},
		{"h", "on an item: step out to the parent (top-level item -> section header)"},
		{"z", "focus-fold (zoom): collapse every other section; z again restores the folds"},
		{"w", "wrap the item at the cursor (toggle single-line <-> full multi-line)"},
		{"e", "edit item inline (the bullet line only); on a section header, rename it"},
		{"T", "edit the board title (frontmatter title:; empty clears it)"},
		{"E", "open board in $EDITOR"},
		{"o", "open item's [[linked note]] in $EDITOR (chooser if several)"},
		{"y", "copy board file path to clipboard (shown in status)"},
		{"m", "toggle the mindmap view (hjkl move · enter fold · a/e/space/D edit)"},
		{"e (map)", "edit item · rename section · edit board title (on the center)"},
		{"a (map)", "add child/sibling · add a section (on the center)"},
		{"d (map)", "archive the focused item or whole section into ## Archive"},
		{"f / b (map)", "focus into subtree (breadcrumbs) / focus out one level (also esc)"},
		{"o (map)", "open the focused item's [[linked note]] (chooser if several)"},
		{"enter (map)", "cycle fold: collapsed -> default -> replies-shown -> collapsed"},
		{"! (map)", "toggle urgent (!!) on the focused item"},
		{"D (map)", "hard-delete the focused item or whole section"},
		{"S (map)", "map nav surprised you? note it — saved to <board>.feedback.jsonl (dev)"},
		{"L", "quick log entry"},
		{"H", "history: shared edit journal (who changed what), read-only"},
		{"u", "undo last change (shared journal timeline)"},
		{"ctrl+r", "redo"},
		{"r", "reload from disk"},
		{"B", "back to board picker"},
		{"q / esc", "quit"},
		{"?", "close help"},
	}
	var lines []string
	lines = append(lines, styleTitle.Render("session-notes — keys"), "")
	for _, k := range keys {
		lines = append(lines, fmt.Sprintf("  %s%s", styleKey.Render(fmt.Sprintf("%-20s", k[0])), k[1]))
	}
	lines = append(lines, "", styleSection.Render("Statuses")+": "+
		styleMarker.Render("[ ]")+" open · "+styleInProg.Render("[>]")+" in progress · "+
		styleDone.Render("[x]")+" done · "+styleBlocked.Render("[?]")+" blocked")
	notes := []string{
		"Section headers are cursor stops: " + styleKey.Render("d") + " there archives the whole section (its items",
		"move to ## Archive), " + styleKey.Render("D") + " hard-deletes it. Archive and Log can't be archived.",
		"Any section collapses to its header plus a hidden-item count; " + styleKey.Render("enter") + "/" + styleKey.Render("l") + " on a",
		"header toggles it. Archive starts collapsed; other sections start expanded.",
		"Replies nest under an item as indented \"- author: text\" sub-bullets.",
		"In the map, " + styleKey.Render("enter") + " cycles a node's fold: each press reveals more (default view,",
		"then the full reply thread) until all is shown, then one more collapses it fully",
		"to a single \"[+N]\" summary of everything hidden.",
		"Continuation lines (indented text under a bullet, not \"- \") render verbatim as",
		"part of that bullet's block — a single cursor stop. Author them with " + styleKey.Render("E") + " ($EDITOR).",
		"Long items show as one truncated line; " + styleKey.Render("w") + " wraps the one at the cursor to",
		"its full height. ASCII-art continuation lines clip at the edge, never wrap.",
		styleUrgent.Render("Urgent (!!)") + " open items are injected into Claude's context on your next prompt.",
		stylePin.Render("Pinned (!pin)") + " items are re-injected into Claude's context on a cadence (p toggles).",
		styleLink.Render("[[name]]") + " in an item's text links to a side notes file; " + styleKey.Render("o") + " opens it in",
		"$EDITOR, creating it (with a \"# name\" heading) if missing.",
		"Undo/redo track the last 100 changes; an external edit is kept in history too,",
		"so undo steps back over it rather than clobbering the other party's write.",
	}
	lines = append(lines, "")
	for _, n := range notes {
		lines = append(lines, styleDim.Render("")+n)
	}

	// Scroll window: reserve rows for the title padding (2) and footer (2), so
	// the whole list stays reachable on short terminals (j/k/arrows scroll).
	bodyH := max(1, m.height-4)
	maxScroll := max(0, len(lines)-bodyH)
	if m.helpScroll > maxScroll {
		m.helpScroll = maxScroll
	}
	start := m.helpScroll
	end := min(len(lines), start+bodyH)
	var b strings.Builder
	b.WriteString(strings.Join(lines[start:end], "\n"))
	bar := "j/k/arrows scroll · g/G top/bottom · any other key returns"
	if maxScroll > 0 {
		bar = scrollReadout(m.helpScroll, start+1, end, len(lines)) + "  " + bar
	}
	b.WriteString("\n\n" + styleHelpBar.Render(bar))
	return lipgloss.NewStyle().Padding(1, 2).Render(b.String())
}

// viewHistory renders the read-only shared-journal history overlay (H): the
// board's journaled edits newest-first with when / who / what, mirroring the
// web history modal. Read-only — it never mutates; undo/redo stay on u/ctrl+r.
func (m *model) viewHistory() string {
	var b strings.Builder
	b.WriteString(styleTitle.Render("history — shared edit journal") + "\n")
	b.WriteString(styleDim.Render("newest first · who changed what · read-only") + "\n\n")
	entries := board.History(m.path)
	var lines []string
	if len(entries) == 0 {
		lines = append(lines, styleDim.Render("no journaled edits yet"))
	}
	// Newest first.
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		author := e.Author
		if author == "" {
			author = "user"
		}
		lines = append(lines, styleSection.Render(e.Time)+"  "+styleKey.Render(author))
		added, removed := board.DiffLines(e.Before, e.After)
		for _, l := range removed {
			lines = append(lines, styleDim.Render("  - "+strings.TrimSpace(l)))
		}
		for _, l := range added {
			lines = append(lines, "  + "+strings.TrimSpace(l))
		}
		if len(added) == 0 && len(removed) == 0 {
			lines = append(lines, styleDim.Render("  (no textual change)"))
		}
		lines = append(lines, "")
	}
	// Scroll window: reserve rows for the header (3) and footer (2).
	bodyH := max(1, m.height-6)
	if m.historyScroll > len(lines)-1 {
		m.historyScroll = max(0, len(lines)-1)
	}
	start := m.historyScroll
	end := min(len(lines), start+bodyH)
	for _, l := range lines[start:end] {
		b.WriteString(l + "\n")
	}
	b.WriteString("\n" + styleHelpBar.Render("j/k scroll · any other key returns"))
	return lipgloss.NewStyle().Padding(1, 2).Render(b.String())
}

// styleKey is the accent used for keys/authors in overlays.
var styleKey = lipgloss.NewStyle().Foreground(lipgloss.Color("81")).Bold(true)

func (m *model) viewLinkPick() string {
	var b strings.Builder
	b.WriteString(styleTitle.Render("open link") + "\n")
	b.WriteString(styleDim.Render("several links on this item — pick one") + "\n\n")
	for i, name := range m.linkOpts {
		cursor := "  "
		st := styleLink
		if i == m.linkCur {
			cursor = styleCursor.Render("> ")
			st = st.Bold(true)
		}
		b.WriteString(cursor + st.Render("[["+name+"]]") + "\n")
	}
	b.WriteString("\n" + styleHelpBar.Render("j/k/arrows move · enter open · esc cancel"))
	return lipgloss.NewStyle().Padding(1, 2).Render(b.String())
}

func (m *model) viewAddSections() string {
	var b strings.Builder
	b.WriteString(styleTitle.Render("add sections") + "\n")
	b.WriteString(styleDim.Render("select section types to append to the board") + "\n\n")
	for i, name := range m.addOpts {
		mark := styleMarker.Render("[ ]")
		if m.addSel[i] {
			mark = styleInProg.Render("[x]")
		}
		label := name
		if name == customSectionLabel {
			label = styleDim.Render(name)
		}
		prefix := "  "
		if i == m.addCur {
			prefix = styleCursor.Render("> ")
			label = lipgloss.NewStyle().Bold(true).Render(name)
		}
		b.WriteString(prefix + mark + " " + label + "\n")
	}
	b.WriteString("\n" + styleHelpBar.Render("j/k/arrows move · space/x toggle · enter add · esc cancel"))
	return lipgloss.NewStyle().Padding(1, 2).Render(b.String())
}
