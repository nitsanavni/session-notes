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
)

// linkDisplayRe matches [[name]] spans for display-only highlighting. It
// mirrors board.ExtractLinks' delimiters but is kept separate: this one runs
// per already-wrapped, plain-text display row, not over raw item text.
var linkDisplayRe = regexp.MustCompile(`\[\[[^\[\]]+\]\]`)

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
	switch m.mode {
	case modePicker:
		return m.viewPicker()
	case modeHelp:
		return m.viewHelp()
	case modeAddSections:
		return m.viewAddSections()
	}
	return m.viewBoard()
}

func (m *model) viewBoard() string {
	if m.board == nil {
		return "no board\n"
	}
	var lines []string
	cursorLine := 0
	selHeaderLine := 0 // display row of the active section's header

	header := m.path
	if fm := m.board.Frontmatter; fm.Session != "" {
		header = fmt.Sprintf("%s  %s", fm.Session, styleDim.Render(fm.Cwd))
	}
	lines = append(lines, styleTitle.Render(header), "")

	posIdx := 0
	for si, s := range m.board.Sections {
		st := styleSection
		if si == m.selSec {
			st = styleSectionSel
		}
		header := st.Render("## " + s.Title)
		// Subtle section-index hint (1..9) so number-key jumps are discoverable.
		if si < 9 {
			header += " " + styleDim.Render(fmt.Sprintf("%d", si+1))
		}
		// Every section header renders, including empty ones, so they stay
		// visible and reachable. Remember the active header's row for scrolling.
		if si == m.selSec {
			selHeaderLine = len(lines)
		}
		lines = append(lines, header)
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
			}
		}
		walk(s.Items, 0)
	}

	// Footer: input line or key hints, plus status.
	footer := m.viewFooter()
	footerH := lipgloss.Height(footer)

	// Scroll the body so the cursor stays visible.
	bodyH := m.height - footerH
	if bodyH < 3 {
		bodyH = 3
	}
	if cursorLine < m.scroll {
		m.scroll = cursorLine
	}
	if cursorLine >= m.scroll+bodyH {
		m.scroll = cursorLine - bodyH + 1
	}
	// Prefer showing the active section's header when doing so still keeps the
	// cursor on screen — this reveals empty sections you tab onto without ever
	// pinning a section taller than the viewport to its header.
	if selHeaderLine < m.scroll && cursorLine < selHeaderLine+bodyH {
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
	return body + "\n" + footer
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
	var style lipgloss.Style
	switch {
	case it.Status == board.StatusDone: // dim wins over urgent once done
		style = styleDone
	case it.Urgent:
		style = styleUrgent
	case selected:
		style = lipgloss.NewStyle().Bold(true)
	case depth > 0: // replies read slightly dimmer than top-level items
		style = styleDim
	default:
		style = lipgloss.NewStyle()
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
		rendered := renderRowWithLinks(r, style)
		if i == 0 {
			out = append(out, head+rendered)
		} else {
			out = append(out, pad+rendered)
		}
	}
	return out
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
		return styleStatus.Render(label+": ") + m.input.View()
	}
	hints := "j/k move · tab section · 1-9 jump · a add · A section · R reply · space status · ! urgent · d del · e edit · E editor · o open link · L log · r reload · ? help · q quit"
	line := styleHelpBar.Render(hints)
	if m.status != "" {
		line = styleStatus.Render(m.status) + "  " + line
	}
	return line
}

func (m *model) viewHelp() string {
	help := `session-notes — keys

  j / k, up / down    move cursor
  tab / shift-tab     next / previous section
  1 - 9               jump to the Nth section
  a                   add item to current section
  A                   add sections (multi-select overlay)
  R                   reply to item (threaded sub-bullet)
  space               cycle status [ ] -> [>] -> [x]
  !                   toggle urgent (!!)
  d                   delete item (with its continuation + replies)
  e                   edit item inline (the bullet line only)
  E                   open board in $EDITOR
  o                   open item's first [[linked note]] in $EDITOR
  L                   quick log entry
  r                   reload from disk
  q / esc             quit
  ?                   close help

Statuses: [ ] open · [>] in progress · [x] done · [?] blocked
Replies nest under an item as indented "- author: text" sub-bullets.
Continuation lines (indented text under a bullet, not "- ") render verbatim as
part of that bullet's block — a single cursor stop. Author them with E ($EDITOR).
Long lines soft-wrap; ASCII-art continuation lines clip at the edge, not wrap.
Urgent (!!) open items are injected into Claude's context on your next prompt.
[[name]] in an item's text links to a side notes file; o opens the first
link's file in $EDITOR, creating it (with a "# name" heading) if missing.

press any key to return`
	return lipgloss.NewStyle().Padding(1, 2).Render(help)
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
