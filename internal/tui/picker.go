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

type pickerEntry struct {
	path    string
	session string
	title   string // frontmatter title, if any
	cwd     string
	mtime   time.Time
	thread  string // first in-progress thread, if any
}

// listBoards scans the boards directory, newest mtime first.
func listBoards() []pickerEntry {
	dir := board.BoardsDir()
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []pickerEntry
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, f.Name())
		info, err := f.Info()
		if err != nil {
			continue
		}
		e := pickerEntry{
			path:    path,
			session: strings.TrimSuffix(f.Name(), ".md"),
			mtime:   info.ModTime(),
		}
		if b, err := board.Load(path); err == nil {
			e.cwd = b.Frontmatter.Cwd
			e.title = b.Frontmatter.Title
			if s := b.Section("Threads"); s != nil {
				for _, it := range s.Items {
					if it.IsItem() && it.Status == board.StatusInProgress {
						e.thread = it.DisplayText()
						break
					}
				}
			}
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].mtime.After(out[j].mtime) })
	return out
}

// FindBoardByCwd returns the board path if exactly one board's frontmatter cwd
// matches the given directory. Old boards accumulate — one per past session —
// so when several match, boards whose session shows no recent transcript
// activity are excluded; an exactly-one live match still resolves directly.
func FindBoardByCwd(cwd string) (string, bool) {
	var matches []pickerEntry
	for _, e := range listBoards() {
		if e.cwd == cwd {
			matches = append(matches, e)
		}
	}
	if len(matches) == 1 {
		return matches[0].path, true
	}
	var live []pickerEntry
	now := time.Now()
	for _, e := range matches {
		if mt := board.Liveness(e.cwd, e.session); !mt.IsZero() && now.Sub(mt) < idleWindow {
			live = append(live, e)
		}
	}
	if len(live) == 1 {
		return live[0].path, true
	}
	return "", false
}

func (m *model) handlePickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc", "ctrl+c":
		return m, tea.Quit
	case "j", "down":
		if m.pickerCur < len(m.entries)-1 {
			m.pickerCur++
		}
	case "k", "up":
		if m.pickerCur > 0 {
			m.pickerCur--
		}
	case "r":
		m.entries = listBoards()
		if m.pickerCur >= len(m.entries) {
			m.pickerCur = max(0, len(m.entries)-1)
		}
	case "enter":
		if m.pickerCur < len(m.entries) {
			if err := m.openBoard(m.entries[m.pickerCur].path); err != nil {
				m.status = err.Error()
				return m, nil
			}
			return m, m.Init()
		}
	}
	return m, nil
}

func (m *model) viewPicker() string {
	var b strings.Builder
	b.WriteString(styleTitle.Render("session boards") + "\n\n")
	if len(m.entries) == 0 {
		b.WriteString(styleDim.Render("  no boards in "+board.BoardsDir()) + "\n")
	}
	for i, e := range m.entries {
		prefix := "  "
		name := e.session
		if e.title != "" {
			name = e.title
		}
		line := fmt.Sprintf("%-24s %s %s %s", truncate(name, 24),
			styleDim.Render(shortID(e.session)),
			styleDim.Render(e.mtime.Format("Jan 02 15:04")), e.cwd)
		if e.thread != "" {
			line += "  " + styleInProg.Render("[>] "+truncate(e.thread, 40))
		}
		if i == m.pickerCur {
			prefix = styleCursor.Render("> ")
			line = lipgloss.NewStyle().Bold(true).Render(line)
		}
		b.WriteString(prefix + line + "\n")
	}
	b.WriteString("\n" + styleHelpBar.Render("j/k move · enter open · r refresh · q quit"))
	return b.String()
}

// shortID abbreviates a session id for display: the first UUID segment (up to
// the first '-'), or the first 8 characters for non-UUID ids.
func shortID(s string) string {
	if i := strings.IndexByte(s, '-'); i > 0 {
		return s[:i]
	}
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}
