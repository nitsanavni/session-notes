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
	styleDone       = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Strikethrough(true)
	styleUrgent     = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
	styleBlocked    = lipgloss.NewStyle().Foreground(lipgloss.Color("174"))
	styleInProg     = lipgloss.NewStyle().Foreground(lipgloss.Color("150"))
	styleDim        = lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
	styleHelpBar    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	styleStatus     = lipgloss.NewStyle().Foreground(lipgloss.Color("179"))
	styleMarker     = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	styleLink       = lipgloss.NewStyle().Foreground(lipgloss.Color("75")).Underline(true)

	styleAuthorClaude = lipgloss.NewStyle().Foreground(lipgloss.Color("208")) // orange — claude's turn
	styleAuthorUser   = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))  // blue — user's turn
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
		case modeBoard, modeMapAdd, modeMapEdit, modeMapFeedback:
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
		title = fmt.Sprintf("%s  %s", fm.Title, styleDim.Render(fm.Cwd))
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
	}
	// Status/urgency set the base foreground + decoration, independent of
	// selection.
	var style lipgloss.Style
	switch {
	case it.Status == board.StatusDone: // dim + strikethrough wins over urgent once done
		style = styleDone
	case it.Urgent:
		style = styleUrgent
	case depth > 0: // replies read slightly dimmer than top-level items
		style = styleDim
	default:
		style = lipgloss.NewStyle()
	}
	// Selection is a uniform overlay applied after, identical for every status:
	// a loud, theme-adaptive reverse-video bar (a fixed background washes out on
	// light terminals). Normalize the foreground to a bright constant first so
	// the reversed bar is high-contrast on every status — a done item's dim 240
	// would otherwise reverse into a low-contrast bar. Strikethrough from
	// styleDone is an independent SGR attribute and survives alongside Reverse,
	// so a selected done item still reads as done.
	if selected {
		style = style.Foreground(lipgloss.Color("252")).Reverse(true).Bold(true)
	}
	return wrapRows(head, text, hang, m.width, style)
}

// wrapRows word-wraps text to the viewport width and returns one string per
// visual row. The first row carries head (gutter + marker); continuation rows
// are padded with hang spaces so wrapped text lines up under the text start.
// style is applied per row so the whole block reads as one item.
func wrapRows(head, text string, hang, width int, style lipgloss.Style) []string {
	avail := width - hang
	if avail < 1 {
		avail = 1
	}
	rows := strings.Split(ansi.Wrap(text, avail, ""), "\n")
	out := make([]string, 0, len(rows))
	pad := strings.Repeat(" ", hang)
	for i, r := range rows {
		if i == 0 {
			out = append(out, head+renderRow0(r, style))
		} else {
			out = append(out, pad+renderRowWithLinks(r, style))
		}
	}
	return out
}

// renderRow0 renders the first wrapped row of an item. If the row opens with an
// author token — "claude:" or "user:", tolerating a "!! " urgent prepend or a
// "HH:MM " log timestamp ahead of it — that token (colon included) is tinted in
// the author color as its own sibling segment; any prefix before it and the
// remainder render in the base style (with [[links]] highlighted). The tint
// style is derived from the base style so it inherits the selection background
// and any strikethrough. Continuation rows never carry an author token, so they
// call renderRowWithLinks directly.
func renderRow0(row string, style lipgloss.Style) string {
	loc := authorRe.FindStringSubmatchIndex(row)
	if loc == nil {
		return renderRowWithLinks(row, style)
	}
	tokStart, tokEnd := loc[2], loc[3]
	authorStyle := style.Foreground(styleAuthorUser.GetForeground())
	if strings.HasPrefix(row[tokStart:tokEnd], "claude") {
		authorStyle = style.Foreground(styleAuthorClaude.GetForeground())
	}
	var b strings.Builder
	if tokStart > 0 {
		b.WriteString(style.Render(row[:tokStart]))
	}
	b.WriteString(authorStyle.Render(row[tokStart:tokEnd]))
	if tokEnd < len(row) {
		b.WriteString(renderRowWithLinks(row[tokEnd:], style))
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
func renderRowWithLinks(row string, style lipgloss.Style) string {
	locs := linkDisplayRe.FindAllStringIndex(row, -1)
	if locs == nil {
		return style.Render(row)
	}
	linkStyle := style.Foreground(styleLink.GetForeground()).Underline(true)
	var b strings.Builder
	last := 0
	for _, loc := range locs {
		if loc[0] > last {
			b.WriteString(style.Render(row[last:loc[0]]))
		}
		b.WriteString(linkStyle.Render(row[loc[0]:loc[1]]))
		last = loc[1]
	}
	if last < len(row) {
		b.WriteString(style.Render(row[last:]))
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
	case modeInputAdd, modeInputEdit, modeInputLog, modeInputReply, modeInputCustomSection:
		label := map[mode]string{
			modeInputAdd:           "add",
			modeInputEdit:          "edit",
			modeInputLog:           "log",
			modeInputReply:         "reply",
			modeInputCustomSection: "section",
		}[m.mode]
		// Reply mode's human message is always authored "user:", so tint its
		// label user-blue; add/edit/log/section keep the neutral status color.
		labelStyle := styleStatus
		if m.mode == modeInputReply {
			labelStyle = styleAuthorUser
		}
		return labelStyle.Render(label+": ") + m.input.View()
	}
	hints := "j/k move · tab section · 1-9 jump · a add · A section · R reply · F fork · space status · ! urgent · d archive · D delete · enter collapse · e edit · E editor · o open link · m map · u undo · ctrl+r redo · L log · r reload · B boards · ? help · q quit"
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
		{"a", "add item to current section"},
		{"A", "add sections (multi-select overlay)"},
		{"R", "reply in thread (sibling on a reply; starts the thread on an item)"},
		{"F", "fork a sub-thread under the message at the cursor"},
		{"space", "cycle status [ ] -> [>] -> [x]"},
		{"!", "toggle urgent (!!)"},
		{"d", "archive item (or whole section) into ## Archive"},
		{"D", "hard-delete item (or whole section) from the file"},
		{"enter / l", "collapse / expand the section under the cursor (on its header)"},
		{"e", "edit item inline (the bullet line only)"},
		{"E", "open board in $EDITOR"},
		{"o", "open item's [[linked note]] in $EDITOR (chooser if several)"},
		{"m", "toggle the mindmap view (hjkl move · enter fold · a/e/space/D edit)"},
		{"enter (map)", "cycle fold: collapsed -> default -> replies-shown -> collapsed"},
		{"! (map)", "map nav surprised you? note it — saved to <board>.feedback.jsonl"},
		{"L", "quick log entry"},
		{"u", "undo last change"},
		{"ctrl+r", "redo"},
		{"r", "reload from disk"},
		{"B", "back to board picker"},
		{"q / esc", "quit"},
		{"?", "close help"},
	}
	var b strings.Builder
	b.WriteString(styleTitle.Render("session-notes — keys") + "\n\n")
	for _, k := range keys {
		b.WriteString(fmt.Sprintf("  %s%s\n", styleKey.Render(fmt.Sprintf("%-20s", k[0])), k[1]))
	}
	b.WriteString("\n" + styleSection.Render("Statuses") + ": " +
		styleMarker.Render("[ ]") + " open · " + styleInProg.Render("[>]") + " in progress · " +
		styleDone.Render("[x]") + " done · " + styleBlocked.Render("[?]") + " blocked\n")
	notes := []string{
		"Section headers are cursor stops: " + styleKey.Render("d") + " there archives the whole section (its items",
		"move to ## Archive), " + styleKey.Render("D") + " hard-deletes it. Archive and Log can't be archived.",
		"Any section collapses to its header plus a hidden-item count; " + styleKey.Render("enter") + "/" + styleKey.Render("l") + " on a",
		"header toggles it. Archive starts collapsed; other sections start expanded.",
		"Replies nest under an item as indented \"- author: text\" sub-bullets.",
		"In the map, " + styleKey.Render("enter") + " cycles a node's fold: each press reveals more (default view,",
		"then the full reply thread) until all is shown, then one more collapses it fully",
		"to a single \"[+N]\" (or \"[N replies]\") summary of everything hidden.",
		"Continuation lines (indented text under a bullet, not \"- \") render verbatim as",
		"part of that bullet's block — a single cursor stop. Author them with " + styleKey.Render("E") + " ($EDITOR).",
		"Long lines soft-wrap; ASCII-art continuation lines clip at the edge, not wrap.",
		styleUrgent.Render("Urgent (!!)") + " open items are injected into Claude's context on your next prompt.",
		styleLink.Render("[[name]]") + " in an item's text links to a side notes file; " + styleKey.Render("o") + " opens it in",
		"$EDITOR, creating it (with a \"# name\" heading) if missing.",
		"Undo/redo track the last 100 changes; an external edit is kept in history too,",
		"so undo steps back over it rather than clobbering the other party's write.",
	}
	for _, n := range notes {
		b.WriteString(styleDim.Render("") + n + "\n")
	}
	b.WriteString("\n" + styleHelpBar.Render("press any key to return"))
	return lipgloss.NewStyle().Padding(1, 2).Render(b.String())
}

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
	b.WriteString("\n" + styleHelpBar.Render("j/k move · enter open · esc cancel"))
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
	b.WriteString("\n" + styleHelpBar.Render("j/k move · space/x toggle · enter add · esc cancel"))
	return lipgloss.NewStyle().Padding(1, 2).Render(b.String())
}
