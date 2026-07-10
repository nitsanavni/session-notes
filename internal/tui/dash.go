package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/nitsanavni/session-notes/internal/board"
)

// Liveness classification thresholds. The dashboard renders a session as
// active (●) when its transcript changed within activeWindow, idle (○) within
// idleWindow, and gone (✗) otherwise (including when there is no transcript).
const (
	activeWindow = 2 * time.Minute
	idleWindow   = 2 * time.Hour
)

// dashCard is one board's row on the dashboard: its identity plus a compact
// snapshot of what matters at a glance.
type dashCard struct {
	path    string
	session string    // full session id (board filename without .md)
	title   string    // optional frontmatter title
	cwd     string    // frontmatter cwd
	mtime   time.Time // transcript mtime (zero => no known activity)
	threads []string  // up to 3 in-progress [>] thread texts
	urgent  []string  // open !! item texts
	lastLog string    // last Log line text
}

// name is the card's display label: the title if set, else a short session id.
func (c dashCard) name() string {
	if c.title != "" {
		return c.title
	}
	return shortSession(c.session)
}

// shortSession abbreviates a session id (usually a uuid) to its first segment.
func shortSession(s string) string {
	if i := strings.IndexByte(s, '-'); i > 0 {
		return s[:i]
	}
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// buildDashCards filters boards to the current cwd (unless all), extracts each
// card's snapshot, and sorts them: most-recently-active first (transcript mtime
// descending), boards with no transcript last, ties broken by session id. The
// liveness lookup is injected so this core logic is testable without touching
// the filesystem. "Active first" falls out of the mtime-descending order, since
// activity is defined by a recent transcript mtime.
func buildDashCards(boards []*board.Board, cwd string, all bool, live func(*board.Board) time.Time) []dashCard {
	var cards []dashCard
	for _, b := range boards {
		if !all && b.Frontmatter.Cwd != cwd {
			continue
		}
		c := dashCard{
			path:    b.Path,
			session: sessionOf(b),
			title:   b.Frontmatter.Title,
			cwd:     b.Frontmatter.Cwd,
			mtime:   live(b),
		}
		if s := b.Section("Threads"); s != nil {
			for _, it := range s.Items {
				if it.IsItem() && it.Status == board.StatusInProgress {
					c.threads = append(c.threads, it.DisplayText())
					if len(c.threads) == 3 {
						break
					}
				}
			}
		}
		for _, it := range b.UrgentOpenItems() {
			c.urgent = append(c.urgent, it.DisplayText())
		}
		if s := b.Section("Log"); s != nil {
			for i := len(s.Items) - 1; i >= 0; i-- {
				if s.Items[i].IsItem() {
					c.lastLog = s.Items[i].DisplayText()
					break
				}
			}
		}
		cards = append(cards, c)
	}
	sort.SliceStable(cards, func(i, j int) bool {
		a, b := cards[i].mtime, cards[j].mtime
		if a.Equal(b) {
			return cards[i].session < cards[j].session
		}
		// Zero time (no transcript) sinks to the bottom.
		if a.IsZero() || b.IsZero() {
			return b.IsZero()
		}
		return a.After(b)
	})
	return cards
}

// sessionOf returns the board's session id, preferring the frontmatter value
// and falling back to the file name so cards always have a stable identity.
func sessionOf(b *board.Board) string {
	if b.Frontmatter.Session != "" {
		return b.Frontmatter.Session
	}
	return strings.TrimSuffix(filepath.Base(b.Path), ".md")
}

// loadAllBoards loads every board file in the boards directory.
func loadAllBoards() []*board.Board {
	dir := board.BoardsDir()
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []*board.Board
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".md") {
			continue
		}
		if b, err := board.Load(filepath.Join(dir, f.Name())); err == nil {
			out = append(out, b)
		}
	}
	return out
}

// scanDashCards loads all boards and builds the (filtered, sorted) card list,
// using real transcript-mtime liveness.
func scanDashCards(all bool, cwd string) []dashCard {
	return buildDashCards(loadAllBoards(), cwd, all, func(b *board.Board) time.Time {
		return board.Liveness(b.Frontmatter.Cwd, sessionOf(b))
	})
}

func (m *model) rescanDash() {
	m.cards = scanDashCards(m.dashAll, m.dashCwd)
	if m.dashCur >= len(m.cards) {
		m.dashCur = max(0, len(m.cards)-1)
	}
	if m.dashCur < 0 {
		m.dashCur = 0
	}
}

// enterDash switches the model back to the dashboard from a board view: it
// closes the board's file watcher, re-establishes the boards-dir watcher, and
// rescans. The perpetual tick chain started in Init keeps running, so no new
// tick is issued here.
func (m *model) enterDash() tea.Cmd {
	if m.watch != nil {
		m.watch.close()
		m.watch = nil
	}
	m.mode = modeDash
	m.cameFromDash = false
	m.status = ""
	m.rescanDash()
	m.watch, _ = newDirWatcher(board.BoardsDir())
	if m.watch != nil {
		return m.watch.wait()
	}
	return nil
}

// dashTickMsg drives the periodic dashboard refresh.
type dashTickMsg struct{}

func dashTick() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return dashTickMsg{} })
}

func (m *model) handleDashKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.status = ""
	switch msg.String() {
	case "q", "esc", "ctrl+c":
		return m, tea.Quit
	case "j", "down":
		if m.dashCur < len(m.cards)-1 {
			m.dashCur++
		}
	case "k", "up":
		if m.dashCur > 0 {
			m.dashCur--
		}
	case "r":
		m.rescanDash()
	case "enter", "l", "right":
		if m.dashCur < 0 || m.dashCur >= len(m.cards) {
			return m, nil
		}
		path := m.cards[m.dashCur].path
		m.cameFromDash = true
		if err := m.openBoard(path); err != nil { // closes dir watcher, opens file watcher
			m.cameFromDash = false
			m.status = err.Error()
			return m, nil
		}
		if m.watch != nil {
			return m, m.watch.wait()
		}
		return m, nil
	}
	return m, nil
}

// --- dashboard view ---

var (
	styleDot    = lipgloss.NewStyle().Foreground(lipgloss.Color("150")).Bold(true) // active
	styleDotOff = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))            // idle/gone
	styleName   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("252"))
)

// classify returns the liveness glyph and its style for a transcript mtime,
// relative to now. The thresholds live here in the TUI, not in the board layer.
func classify(mtime, now time.Time) (glyph string, style lipgloss.Style) {
	if mtime.IsZero() {
		return "✗", styleDotOff
	}
	age := now.Sub(mtime)
	switch {
	case age < activeWindow:
		return "●", styleDot
	case age < idleWindow:
		return "○", styleDotOff
	default:
		return "✗", styleDotOff
	}
}

// relTime renders a compact activity age: "16s", "4m", "3h", "2d", or "—" when
// there is no known activity.
func relTime(mtime, now time.Time) string {
	if mtime.IsZero() {
		return "—"
	}
	d := now.Sub(mtime)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours())/24)
	}
}

func (m *model) viewDash() string {
	now := time.Now()
	var b strings.Builder

	scope := "this project"
	if m.dashAll {
		scope = "all projects"
	}
	head := styleTitle.Render("session dashboard") + "  " +
		styleDim.Render(fmt.Sprintf("%s · %d live", scope, len(m.cards)))
	b.WriteString(head + "\n\n")

	if len(m.cards) == 0 {
		b.WriteString(styleDim.Render("  no boards for "+m.dashCwd) + "\n")
		if !m.dashAll {
			b.WriteString(styleDim.Render("  (run with --all to see every project)") + "\n")
		}
	}

	for i, c := range m.cards {
		glyph, gs := classify(c.mtime, now)
		selected := i == m.dashCur
		cursor := "  "
		nameStyle := styleName
		if selected {
			cursor = styleCursor.Render("> ")
			nameStyle = styleName.Underline(true)
		}
		title := fmt.Sprintf("%s%s %s  %s",
			cursor, gs.Render(glyph), nameStyle.Render(c.name()),
			styleDim.Render(relTime(c.mtime, now)))
		b.WriteString(title + "\n")

		wThread := max(10, m.width-12)
		wLog := max(10, m.width-8)
		for _, t := range c.threads {
			b.WriteString("      " + styleInProg.Render("[>] ") + styleDim.Render(truncate(t, wThread)) + "\n")
		}
		for _, u := range c.urgent {
			b.WriteString("      " + styleUrgent.Render("!! "+truncate(u, wThread)) + "\n")
		}
		if c.lastLog != "" {
			b.WriteString("      " + styleDim.Render(truncate(c.lastLog, wLog)) + "\n")
		}
		b.WriteString("\n")
	}

	footer := styleHelpBar.Render("j/k/arrows move · enter open · r rescan · q quit")
	if m.updateHint != "" {
		footer += "\n" + styleDim.Render(m.updateHint)
	}
	return b.String() + footer
}
