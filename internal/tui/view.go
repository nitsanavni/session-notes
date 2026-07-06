package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
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
)

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
	}
	return m.viewBoard()
}

func (m *model) viewBoard() string {
	if m.board == nil {
		return "no board\n"
	}
	var lines []string
	cursorLine := 0

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
		lines = append(lines, st.Render("## "+s.Title))
		for _, it := range s.Items {
			if !it.IsItem() {
				// Preserve raw lines in display too, dimmed; skip blank lines'
				// styling but keep spacing.
				if strings.TrimSpace(it.DisplayText()) == "" {
					lines = append(lines, "")
				} else {
					lines = append(lines, "  "+styleDim.Render(it.DisplayText()))
				}
				continue
			}
			selected := posIdx == m.cursor
			if selected {
				cursorLine = len(lines)
			}
			lines = append(lines, m.renderItem(it, selected))
			posIdx++
		}
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

func (m *model) renderItem(it *board.Item, selected bool) string {
	prefix := "  "
	if selected {
		prefix = styleCursor.Render("> ")
	}
	text := it.DisplayText()
	if it.Urgent {
		text = "!! " + text
	}
	var styled string
	switch {
	case it.Status == board.StatusDone: // dim wins over urgent once done
		styled = styleDone.Render(text)
	case it.Urgent:
		styled = styleUrgent.Render(text)
	case selected:
		styled = lipgloss.NewStyle().Bold(true).Render(text)
	default:
		styled = text
	}
	return prefix + statusMarker(it.Status) + " " + styled
}

func (m *model) viewFooter() string {
	switch m.mode {
	case modeInputAdd, modeInputEdit, modeInputLog:
		label := map[mode]string{
			modeInputAdd:  "add",
			modeInputEdit: "edit",
			modeInputLog:  "log",
		}[m.mode]
		return styleStatus.Render(label+": ") + m.input.View()
	}
	hints := "j/k move · tab section · a add · space status · ! urgent · d del · e edit · E editor · L log · r reload · ? help · q quit"
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
  a                   add item to current section
  space               cycle status [ ] -> [>] -> [x]
  !                   toggle urgent (!!)
  d                   delete item
  e                   edit item inline
  E                   open board in $EDITOR
  L                   quick log entry
  r                   reload from disk
  q / esc             quit
  ?                   close help

Statuses: [ ] open · [>] in progress · [x] done · [?] blocked
Urgent (!!) open items are injected into Claude's context on your next prompt.

press any key to return`
	return lipgloss.NewStyle().Padding(1, 2).Render(help)
}
