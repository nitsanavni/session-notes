// Package tui implements the session-notes bubbletea interface: the board
// view, the board picker, and file watching for live reload.
package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/nitsanavni/session-notes/internal/board"
)

type mode int

const (
	modeBoard mode = iota
	modePicker
	modeInputAdd
	modeInputEdit
	modeInputLog
	modeHelp
)

// pos addresses one item on the board: section index + index within that
// section's Items slice.
type pos struct{ sec, item int }

type model struct {
	mode     mode
	prevMode mode // mode to return to after input/help

	// board view state
	path      string
	board     *board.Board
	positions []pos // navigable (parsed) items, in document order
	cursor    int   // index into positions; -1 when there are none
	selSec    int   // active section (authoritative for `a` and tab)
	scroll    int
	status    string // transient one-line message

	input   textinput.Model
	editPos pos // item being edited in modeInputEdit

	// picker state
	entries   []pickerEntry
	pickerCur int

	watch  *watcher
	width  int
	height int
}

// Run opens the TUI on a specific board file.
func Run(path string) error {
	m := newModel()
	if err := m.openBoard(path); err != nil {
		return fmt.Errorf("open board %s: %w", path, err)
	}
	return runProgram(m)
}

// RunPicker opens the TUI in picker mode.
func RunPicker() error {
	m := newModel()
	m.mode = modePicker
	m.entries = listBoards()
	return runProgram(m)
}

func runProgram(m *model) error {
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	if m.watch != nil {
		m.watch.close()
	}
	return err
}

func newModel() *model {
	ti := textinput.New()
	ti.CharLimit = 500
	ti.Prompt = "> "
	return &model{cursor: -1, input: ti, width: 80, height: 24}
}

func (m *model) openBoard(path string) error {
	b, err := board.Load(path)
	if err != nil {
		return err
	}
	m.path = path
	m.board = b
	m.mode = modeBoard
	m.rebuildPositions()
	if m.watch != nil {
		m.watch.close()
	}
	m.watch, _ = newWatcher(path) // nil watcher is tolerated (no live reload)
	return nil
}

func (m *model) rebuildPositions() {
	m.positions = m.positions[:0]
	for si, s := range m.board.Sections {
		for ii, it := range s.Items {
			if it.IsItem() {
				m.positions = append(m.positions, pos{si, ii})
			}
		}
	}
	if len(m.positions) == 0 {
		m.cursor = -1
	} else if m.cursor < 0 {
		m.cursor = 0
	} else if m.cursor >= len(m.positions) {
		m.cursor = len(m.positions) - 1
	}
	if m.selSec >= len(m.board.Sections) {
		m.selSec = max(0, len(m.board.Sections)-1)
	}
	if m.cursor >= 0 {
		m.selSec = m.positions[m.cursor].sec
	}
}

func (m *model) currentItem() *board.Item {
	if m.cursor < 0 || m.cursor >= len(m.positions) {
		return nil
	}
	p := m.positions[m.cursor]
	return m.board.Sections[p.sec].Items[p.item]
}

func (m *model) save() {
	if err := m.board.Save(); err != nil {
		m.status = "save failed: " + err.Error()
	}
}

func (m *model) reload() {
	if m.path == "" {
		return
	}
	b, err := board.Load(m.path)
	if err != nil {
		m.status = "reload failed: " + err.Error()
		return
	}
	m.board = b
	m.rebuildPositions()
}

// --- tea.Model ---

// reloadMsg comes from the file watcher; editorDoneMsg from resuming after $EDITOR.
type reloadMsg struct{}
type editorDoneMsg struct{}

func (m *model) Init() tea.Cmd {
	if m.watch != nil {
		return m.watch.wait()
	}
	return nil
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case reloadMsg:
		if m.mode != modePicker {
			m.reload()
		}
		if m.watch != nil {
			return m, m.watch.wait()
		}
		return m, nil
	case editorDoneMsg:
		m.reload()
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	if m.mode == modeInputAdd || m.mode == modeInputEdit || m.mode == modeInputLog {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case modePicker:
		return m.handlePickerKey(msg)
	case modeInputAdd, modeInputEdit, modeInputLog:
		return m.handleInputKey(msg)
	case modeHelp:
		m.mode = m.prevMode
		return m, nil
	}
	return m.handleBoardKey(msg)
}

func (m *model) handleBoardKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.status = ""
	switch msg.String() {
	case "q", "esc", "ctrl+c":
		return m, tea.Quit
	case "j", "down":
		if m.cursor < len(m.positions)-1 {
			m.cursor++
			m.selSec = m.positions[m.cursor].sec
		}
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
			m.selSec = m.positions[m.cursor].sec
		}
	case "tab":
		m.moveSection(1)
	case "shift+tab":
		m.moveSection(-1)
	case "a":
		m.startInput(modeInputAdd, "", "add to "+m.sectionTitle(m.selSec))
	case " ":
		if it := m.currentItem(); it != nil {
			it.CycleStatus()
			m.save()
		}
	case "!":
		if it := m.currentItem(); it != nil {
			it.ToggleUrgent()
			m.save()
		}
	case "d":
		if m.cursor >= 0 {
			p := m.positions[m.cursor]
			m.board.Sections[p.sec].DeleteItem(p.item)
			m.rebuildPositions()
			m.save()
		}
	case "e":
		if it := m.currentItem(); it != nil {
			m.editPos = m.positions[m.cursor]
			m.startInput(modeInputEdit, it.DisplayText(), "edit item")
		}
	case "E":
		return m, m.openEditor()
	case "L":
		m.startInput(modeInputLog, "", "log entry")
	case "r":
		m.reload()
		m.status = "reloaded"
	case "?":
		m.prevMode = m.mode
		m.mode = modeHelp
	}
	return m, nil
}

func (m *model) moveSection(delta int) {
	n := len(m.board.Sections)
	if n == 0 {
		return
	}
	m.selSec = ((m.selSec+delta)%n + n) % n
	for i, p := range m.positions {
		if p.sec == m.selSec {
			m.cursor = i
			return
		}
	}
	// Section has no items: keep cursor where it is; header highlight moves.
}

func (m *model) sectionTitle(i int) string {
	if i >= 0 && i < len(m.board.Sections) {
		return m.board.Sections[i].Title
	}
	return "board"
}

func (m *model) startInput(mo mode, value, placeholder string) {
	m.prevMode = m.mode
	m.mode = mo
	m.input.Placeholder = placeholder
	m.input.SetValue(value)
	m.input.CursorEnd()
	m.input.Focus()
}

func (m *model) handleInputKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.mode = modeBoard
		m.input.Blur()
		return m, nil
	case "enter":
		text := strings.TrimSpace(m.input.Value())
		mo := m.mode
		m.mode = modeBoard
		m.input.Blur()
		if text == "" {
			return m, nil
		}
		switch mo {
		case modeInputAdd:
			m.board.AddItem(m.sectionTitle(m.selSec), text)
			m.rebuildPositions()
			// Move cursor to the new item (last parsed item of the section).
			for i := len(m.positions) - 1; i >= 0; i-- {
				if m.positions[i].sec == m.selSec {
					m.cursor = i
					break
				}
			}
		case modeInputEdit:
			p := m.editPos
			if p.sec < len(m.board.Sections) && p.item < len(m.board.Sections[p.sec].Items) {
				m.board.Sections[p.sec].Items[p.item].Text = text
			}
		case modeInputLog:
			m.board.AppendLog("user", text)
			m.rebuildPositions()
		}
		m.save()
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *model) openEditor() tea.Cmd {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	c := exec.Command(editor, m.path) // #nosec G204 -- user's own $EDITOR
	return tea.ExecProcess(c, func(error) tea.Msg { return editorDoneMsg{} })
}
