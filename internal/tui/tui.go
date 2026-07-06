// Package tui implements the session-notes bubbletea interface: the board
// view, the board picker, and file watching for live reload.
package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	modeInputReply
	modeInputCustomSection
	modeAddSections
	modeHelp
)

// customSectionLabel is the final, always-present entry in the add-sections
// overlay that prompts for a free-text section name.
const customSectionLabel = "custom…"

// navItem addresses one navigable stop on the board. A stop is either a section
// header (header == true, item == nil) or a parsed item somewhere in that
// section's item tree. It carries the owning section index, the item pointer,
// and the item's nesting depth (0 = top level, 1 = reply, …).
type navItem struct {
	sec    int
	item   *board.Item
	depth  int
	header bool // true: this stop is section sec's "## " header line
}

type model struct {
	mode     mode
	prevMode mode // mode to return to after input/help

	// board view state
	path      string
	board     *board.Board
	positions []navItem // navigable (parsed) items, in document order
	cursor    int       // index into positions; -1 when there are none
	selSec    int       // active section (authoritative for `a` and tab)
	scroll    int
	status    string // transient one-line message

	// archiveExpanded controls whether the Archive section shows its items or
	// collapses to a one-line header with a count. Collapsed by default.
	archiveExpanded bool

	input textinput.Model
	// target is the item acted on by modeInputEdit / modeInputReply, captured
	// when the input opens (the cursor may not move, but this is robust to it).
	target *board.Item

	// add-sections overlay state (modeAddSections)
	addOpts []string     // offered section names; last entry is customSectionLabel
	addSel  map[int]bool // selected indices into addOpts
	addCur  int          // cursor within addOpts

	// picker state
	entries   []pickerEntry
	pickerCur int

	hist *history // bounded undo/redo of full board snapshots

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
	return &model{cursor: -1, input: ti, width: 80, height: 24, hist: newHistory(100)}
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
		// Every section header is a cursor stop of its own, so it can be acted
		// on (archive/remove the whole section) and, for Archive, toggled.
		m.positions = append(m.positions, navItem{sec: si, header: true})
		// A collapsed Archive hides its items entirely — they are not navigable.
		if s.Title == board.ArchiveTitle && !m.archiveExpanded {
			continue
		}
		var walk func(items []*board.Item, depth int)
		walk = func(items []*board.Item, depth int) {
			for _, it := range items {
				if it.IsItem() {
					m.positions = append(m.positions, navItem{sec: si, item: it, depth: depth})
				}
				walk(it.Children, depth+1)
			}
		}
		walk(s.Items, 0)
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
	return m.positions[m.cursor].item
}

// onHeader reports whether the cursor sits on a section header stop, and if so
// returns that section. When the cursor is on an item it returns (nil, false).
func (m *model) onHeader() (*board.Section, bool) {
	if m.cursor < 0 || m.cursor >= len(m.positions) {
		return nil, false
	}
	p := m.positions[m.cursor]
	if !p.header || p.sec >= len(m.board.Sections) {
		return nil, false
	}
	return m.board.Sections[p.sec], true
}

func (m *model) save() {
	if err := m.board.Save(); err != nil {
		m.status = "save failed: " + err.Error()
	}
}

// currentContent is the board's current serialized form — the unit of the undo
// history and the value compared against disk to tell our own atomic-save echoes
// apart from genuine external edits.
func (m *model) currentContent() string {
	if m.board == nil {
		return ""
	}
	return m.board.Render()
}

// snapshot records the board's current state before a mutating action. All TUI
// mutations funnel through here so undo/redo (and any future mutation) has one
// obvious place to hook in.
func (m *model) snapshot() {
	m.hist.snapshot(m.currentContent())
}

// restore replaces the board with content and writes it back through the atomic
// save path so Claude's file watcher observes the change.
func (m *model) restore(content string) {
	b := board.Parse(content)
	b.Path = m.path
	m.board = b
	m.rebuildPositions()
	m.save()
}

// undo reverts to the snapshot taken before the last mutation.
func (m *model) undo() {
	if prev, ok := m.hist.undoTo(m.currentContent()); ok {
		m.restore(prev)
		m.status = "undo"
	} else {
		m.status = "nothing to undo"
	}
}

// redo re-applies the most recently undone state.
func (m *model) redo() {
	if next, ok := m.hist.redoTo(m.currentContent()); ok {
		m.restore(next)
		m.status = "redo"
	} else {
		m.status = "nothing to redo"
	}
}

// reload re-reads the board from disk with no history side effects. Used after
// $EDITOR returns, where the pre-launch snapshot already covers the change.
func (m *model) reload() { m.doReload(false) }

// reloadExternal re-reads the board from disk and, if the file differs from what
// we hold, records the state we are leaving so undo steps back over the external
// write. Our own atomic-save echoes match what we hold and so are ignored.
func (m *model) reloadExternal() { m.doReload(true) }

func (m *model) doReload(recordExternal bool) {
	if m.path == "" {
		return
	}
	data, err := os.ReadFile(m.path)
	if err != nil {
		m.status = "reload failed: " + err.Error()
		return
	}
	content := string(data)
	if recordExternal && m.board != nil && content != m.board.Render() {
		m.hist.record(m.board.Render())
	}
	b := board.Parse(content)
	b.Path = m.path
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
			m.reloadExternal()
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
	if m.isInputMode() {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

// isInputMode reports whether the model is currently capturing textinput.
func (m *model) isInputMode() bool {
	switch m.mode {
	case modeInputAdd, modeInputEdit, modeInputLog, modeInputReply, modeInputCustomSection:
		return true
	}
	return false
}

func (m *model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case modePicker:
		return m.handlePickerKey(msg)
	case modeInputAdd, modeInputEdit, modeInputLog, modeInputReply, modeInputCustomSection:
		return m.handleInputKey(msg)
	case modeAddSections:
		return m.handleAddSectionsKey(msg)
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
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		m.jumpToSection(int(msg.String()[0] - '1'))
	case "a":
		m.startInput(modeInputAdd, "", "add to "+m.sectionTitle(m.selSec))
	case "A":
		m.openAddSections()
	case " ":
		// StatusNone (plain bullets in Ideas/Log) has no cycle, so skip it rather
		// than record a no-op undo entry.
		if it := m.currentItem(); it != nil && it.Status != board.StatusNone {
			m.snapshot()
			it.CycleStatus()
			m.save()
		}
	case "!":
		if it := m.currentItem(); it != nil {
			m.snapshot()
			it.ToggleUrgent()
			m.save()
		}
	case "d":
		// Archive: move the item (or a whole section) into ## Archive.
		if sec, ok := m.onHeader(); ok {
			m.snapshot()
			if m.board.ArchiveSection(sec) {
				m.rebuildPositions()
				m.save()
				m.status = "archived section " + sec.Title
			} else {
				m.status = "cannot archive " + sec.Title
			}
		} else if it := m.currentItem(); it != nil {
			m.snapshot()
			if m.board.ArchiveItem(it) { // item + its whole subtree
				m.rebuildPositions()
				m.save()
				m.status = "archived"
			}
		}
	case "D":
		// Hard delete: remove the item (or whole section) from the file.
		if sec, ok := m.onHeader(); ok {
			m.snapshot()
			m.board.RemoveSection(sec)
			m.rebuildPositions()
			m.save()
			m.status = "deleted section " + sec.Title
		} else if it := m.currentItem(); it != nil {
			m.snapshot()
			m.board.Remove(it) // removes the item and its whole subtree of replies
			m.rebuildPositions()
			m.save()
		}
	case "enter", "l", "right":
		// Toggle the Archive section between collapsed and expanded.
		if sec, ok := m.onHeader(); ok && sec.Title == board.ArchiveTitle {
			m.archiveExpanded = !m.archiveExpanded
			m.rebuildPositions()
		}
	case "h", "left":
		// Collapse the Archive when on its header (symmetric with l/right).
		if sec, ok := m.onHeader(); ok && sec.Title == board.ArchiveTitle && m.archiveExpanded {
			m.archiveExpanded = false
			m.rebuildPositions()
		}
	case "e":
		if it := m.currentItem(); it != nil {
			m.target = it
			m.startInput(modeInputEdit, it.DisplayText(), "edit item")
		}
	case "R":
		if it := m.currentItem(); it != nil {
			m.target = it
			m.startInput(modeInputReply, "", "reply to this item")
		}
	case "E":
		m.snapshot() // capture pre-edit state before handing off to $EDITOR
		return m, m.openEditor()
	case "o":
		return m, m.openLink()
	case "L":
		m.startInput(modeInputLog, "", "log entry")
	case "u":
		m.undo()
	case "ctrl+r":
		m.redo()
	case "r":
		m.reloadExternal()
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

// jumpToSection moves the cursor straight to the given section (0-based): to its
// first navigable item, or, if the section is empty, just onto its header (the
// header highlight moves; the cursor stays put).
func (m *model) jumpToSection(idx int) {
	if idx < 0 || idx >= len(m.board.Sections) {
		return
	}
	m.selSec = idx
	for i, p := range m.positions {
		if p.sec == idx {
			m.cursor = i
			return
		}
	}
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
	// Bound the input to the viewport so long entries horizontally scroll,
	// keeping the cursor visible, instead of overflowing off the right edge.
	m.input.Width = max(20, m.width-14) // leave room for the "label: > " prefix
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
		m.snapshot() // capture pre-mutation state for undo
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
			if m.target != nil {
				m.target.Text = text
			}
		case modeInputReply:
			if m.target != nil {
				// Forum-style: the human's reply is authored "user:".
				m.board.AddReply(m.target, "user: "+text)
				m.rebuildPositions()
				// Move the cursor onto the freshly added reply.
				for i, p := range m.positions {
					if p.item == m.target.Children[len(m.target.Children)-1] {
						m.cursor = i
						m.selSec = p.sec
						break
					}
				}
			}
		case modeInputLog:
			m.board.AppendLog("user", text)
			m.rebuildPositions()
		case modeInputCustomSection:
			m.board.AddSection(text)
			m.rebuildPositions()
			m.jumpToSectionByTitle(text)
		}
		m.save()
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// openAddSections builds the add-sections overlay: the canonical section types
// not already on the board, plus a final "custom…" entry.
func (m *model) openAddSections() {
	m.addOpts = m.addOpts[:0]
	for _, name := range board.CanonicalSections {
		if m.board.Section(name) == nil {
			m.addOpts = append(m.addOpts, name)
		}
	}
	m.addOpts = append(m.addOpts, customSectionLabel)
	m.addSel = make(map[int]bool)
	m.addCur = 0
	m.mode = modeAddSections
}

func (m *model) handleAddSectionsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c", "q":
		m.mode = modeBoard
	case "j", "down":
		if m.addCur < len(m.addOpts)-1 {
			m.addCur++
		}
	case "k", "up":
		if m.addCur > 0 {
			m.addCur--
		}
	case " ", "x":
		m.addSel[m.addCur] = !m.addSel[m.addCur]
	case "enter":
		return m.confirmAddSections()
	}
	return m, nil
}

// confirmAddSections appends the selected canonical sections, then either
// prompts for a custom section name (if "custom…" was selected) or returns to
// the board, jumping to the first section added.
func (m *model) confirmAddSections() (tea.Model, tea.Cmd) {
	var toAdd []string
	custom := false
	for i, name := range m.addOpts {
		if !m.addSel[i] {
			continue
		}
		if name == customSectionLabel {
			custom = true
			continue
		}
		toAdd = append(toAdd, name)
	}
	var first string
	if len(toAdd) > 0 {
		m.snapshot() // before mutating; a following custom-name entry snapshots again
		for _, name := range toAdd {
			m.board.AddSection(name)
		}
		first = toAdd[0]
		m.rebuildPositions()
		m.save()
	}
	if custom {
		// Prompt for the free-text name; canonical picks (if any) are already saved.
		m.startInput(modeInputCustomSection, "", "custom section name")
		return m, nil
	}
	m.mode = modeBoard
	if first != "" {
		m.jumpToSectionByTitle(first)
	}
	return m, nil
}

// jumpToSectionByTitle moves the cursor to the section with the given title.
func (m *model) jumpToSectionByTitle(title string) {
	for i, s := range m.board.Sections {
		if s.Title == title {
			m.jumpToSection(i)
			return
		}
	}
}

func (m *model) openEditor() tea.Cmd {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	c := exec.Command(editor, m.path) // #nosec G204 -- user's own $EDITOR
	return tea.ExecProcess(c, func(error) tea.Msg { return editorDoneMsg{} })
}

// openLink opens the FIRST [[wiki-link]] found in the current item's text in
// $EDITOR (same suspend-and-resume pattern as openEditor), creating the
// linked notes file — and its directory — if it doesn't exist yet. A no-op if
// there is no current item or its text has no links.
func (m *model) openLink() tea.Cmd {
	it := m.currentItem()
	if it == nil {
		return nil
	}
	links := board.ExtractLinks(it.DisplayText())
	if len(links) == 0 {
		return nil
	}
	name := links[0]
	path := board.ResolveLink(m.board, name)
	if err := ensureNoteFile(path, name); err != nil {
		m.status = "open link failed: " + err.Error()
		return nil
	}
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	c := exec.Command(editor, path) // #nosec G204 -- user's own $EDITOR
	return tea.ExecProcess(c, func(error) tea.Msg { return editorDoneMsg{} })
}

// ensureNoteFile makes sure the linked note file exists: creates its parent
// notes directory and, if the file itself is missing, seeds it with a
// "# name" heading. Existing files are left untouched.
func ensureNoteFile(path, name string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte("# "+name+"\n"), 0o644)
}
