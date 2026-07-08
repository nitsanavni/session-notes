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
	modeLinkPick
	modeHelp
	modeDash
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
	path  string
	board *board.Board
	// lastDisk is the content the model believes is currently on disk: the exact
	// bytes it last loaded or last wrote. Save-with-rebase re-reads disk under the
	// lock and compares against this to tell a genuine concurrent external write
	// apart from our own echo, so it merges the former instead of clobbering it.
	lastDisk  string
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

	// link-chooser overlay state (modeLinkPick)
	linkOpts []string // links found on the current item
	linkCur  int      // cursor within linkOpts

	// add-sections overlay state (modeAddSections)
	addOpts []string     // offered section names; last entry is customSectionLabel
	addSel  map[int]bool // selected indices into addOpts
	addCur  int          // cursor within addOpts

	// picker state
	entries   []pickerEntry
	pickerCur int

	// dashboard state (modeDash)
	cards        []dashCard
	dashCur      int    // selected card
	dashAll      bool   // --all: every project, not just this cwd
	dashCwd      string // cwd whose boards are shown when !dashAll
	cameFromDash bool   // board view was opened from the dashboard; q/esc returns there

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

// RunDash opens the full-screen live dashboard of all boards. When all is
// false only boards whose frontmatter cwd matches the current working directory
// are shown; all==true shows every project's boards.
func RunDash(all bool) error {
	m := newModel()
	m.mode = modeDash
	m.dashAll = all
	if cwd, err := os.Getwd(); err == nil {
		m.dashCwd = cwd
	}
	m.rescanDash()
	m.watch, _ = newDirWatcher(board.BoardsDir()) // nil is tolerated (tick still refreshes)
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
	// Seed the last-known-disk state from the exact file bytes so the first save
	// does not see a phantom external change (Render may differ from source only
	// in cosmetic whitespace, but starting from the real bytes avoids even that).
	if data, rerr := os.ReadFile(path); rerr == nil {
		m.lastDisk = string(data)
	} else {
		m.lastDisk = b.Render()
	}
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

// save is the plain last-writer-wins save: it writes the whole in-memory tree
// under the board lock. Used by the structural / one-keystroke mutations
// (status cycle, urgent toggle, archive, delete, add-section) that are cheap to
// redo and do not rebase. The lock still guarantees the write can't interleave
// with an external writer; it just does not merge a concurrent external change.
func (m *model) save() {
	if err := m.board.Save(); err != nil {
		m.status = "save failed: " + err.Error()
		return
	}
	m.lastDisk = m.board.Render()
}

// saveWithRebase persists a text-entry mutation (add/edit/reply/log) without
// losing a concurrent external write. The caller has already applied the
// mutation to m.board; op describes it so it can be re-applied onto a fresh disk
// tree if the file changed underneath us. On a rebase the model adopts the
// merged board, records the external state for undo coherence, and reports it.
func (m *model) saveWithRebase(op pendingOp) {
	var msg string
	res, err := m.board.SaveRebasing(m.path, m.lastDisk, func(fresh *board.Board) {
		msg = applyOp(fresh, op)
	})
	if err != nil {
		m.status = "save failed: " + err.Error()
		return
	}
	if res.Rebased {
		// Record the external write so undo steps back through it (first undo
		// reverts our re-applied op, revealing the external edit; a second undo
		// reaches the pre-mutation snapshot taken before this action).
		m.hist.record(res.DiskPrev)
		m.board = res.Board
		m.board.Path = m.path
		m.rebuildPositions()
		if msg != "" {
			m.status = msg
		}
	}
	m.lastDisk = res.Content
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
	m.lastDisk = content
	m.rebuildPositions()
}

// --- tea.Model ---

// reloadMsg comes from the file watcher; editorDoneMsg from resuming after $EDITOR.
type reloadMsg struct{}
type editorDoneMsg struct{}

func (m *model) Init() tea.Cmd {
	var cmds []tea.Cmd
	if m.watch != nil {
		cmds = append(cmds, m.watch.wait())
	}
	if m.mode == modeDash {
		// A single perpetual tick chain drives the 2s dashboard refresh. It is
		// re-issued on every dashTickMsg (see Update) so it survives dipping into
		// a board and back, and never spawns a second chain.
		cmds = append(cmds, dashTick())
	}
	return tea.Batch(cmds...)
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case reloadMsg:
		// While an inline input is open the model holds a pending mutation: the
		// input buffer plus m.target, a pointer into the current m.board tree.
		// Reparsing here would swap m.board for a fresh tree, orphaning m.target
		// so the pending edit lands on a discarded item — the exact lost-update
		// this fix targets. So we ignore external writes for the duration of the
		// input; save-with-rebase re-reads disk under the lock on enter and merges
		// them (esc resyncs). The picker has its own model and never reloads.
		if m.mode == modeDash {
			m.rescanDash()
		} else if m.mode != modePicker && !m.isInputMode() {
			m.reloadExternal()
		}
		if m.watch != nil {
			return m, m.watch.wait()
		}
		return m, nil
	case dashTickMsg:
		if m.mode == modeDash {
			m.rescanDash()
		}
		return m, dashTick()
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
	case modeDash:
		return m.handleDashKey(msg)
	case modePicker:
		return m.handlePickerKey(msg)
	case modeInputAdd, modeInputEdit, modeInputLog, modeInputReply, modeInputCustomSection:
		return m.handleInputKey(msg)
	case modeAddSections:
		return m.handleAddSectionsKey(msg)
	case modeLinkPick:
		return m.handleLinkPickKey(msg)
	case modeHelp:
		m.mode = m.prevMode
		return m, nil
	}
	return m.handleBoardKey(msg)
}

func (m *model) handleBoardKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.status = ""
	switch msg.String() {
	case "q", "esc":
		// When the board was opened from the dashboard, q/esc returns there
		// rather than quitting the program (ctrl+c always quits).
		if m.cameFromDash {
			return m, m.enterDash()
		}
		return m, tea.Quit
	case "ctrl+c":
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
		// Reply in-thread: on a reply the new message continues the conversation
		// as a sibling (appended to the thread's parent); on a top-level item it
		// starts the thread. Discussions run flat; use F to fork.
		if it := m.currentItem(); it != nil {
			m.target = it
			if p := m.board.ParentOf(it); p != nil {
				m.target = p
			}
			m.startInput(modeInputReply, "", "reply in thread")
		}
	case "F":
		// Fork: nest a sub-topic under the exact message at the cursor.
		if it := m.currentItem(); it != nil {
			m.target = it
			m.startInput(modeInputReply, "", "fork a sub-thread")
		}
	case "E":
		m.snapshot() // capture pre-edit state before handing off to $EDITOR
		return m, m.openEditor()
	case "o":
		return m, m.openLink()
	case "L":
		m.startInput(modeInputLog, "", "log entry")
	case "B":
		// Back to the board picker: list all boards and pick another.
		return m, m.enterPicker()
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
		// External writes were ignored while the input was open (see reloadMsg);
		// now that no pending mutation depends on m.target, resync with disk so a
		// concurrent edit made during typing becomes visible.
		m.reloadExternal()
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
			sec := m.sectionTitle(m.selSec)
			m.board.AddItem(sec, text)
			m.rebuildPositions()
			// Move cursor to the new item (last parsed item of the section).
			for i := len(m.positions) - 1; i >= 0; i-- {
				if m.positions[i].sec == m.selSec {
					m.cursor = i
					break
				}
			}
			m.saveWithRebase(pendingOp{typ: opAdd, section: sec, payload: text})
		case modeInputEdit:
			if m.target != nil {
				// Capture identity BEFORE mutating: Raw() is the item's original
				// source line, the key used to relocate it in a fresh disk tree.
				op := pendingOp{typ: opEdit, section: m.board.SectionTitleOf(m.target), rawLine: m.target.Raw(), payload: text}
				m.target.Text = text
				m.saveWithRebase(op)
			}
		case modeInputReply:
			if m.target != nil {
				op := pendingOp{typ: opReply, section: m.board.SectionTitleOf(m.target), rawLine: m.target.Raw(), payload: text}
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
				m.saveWithRebase(op)
			}
		case modeInputLog:
			m.board.AppendLog("user", text)
			m.rebuildPositions()
			m.saveWithRebase(pendingOp{typ: opLog, payload: text})
		case modeInputCustomSection:
			// Add-section is structural and one-keystroke redoable; it uses the
			// plain last-writer save rather than rebasing.
			m.board.AddSection(text)
			m.rebuildPositions()
			m.jumpToSectionByTitle(text)
			m.save()
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// opType identifies a text-entry mutation that save-with-rebase can re-apply
// onto a freshly-reparsed disk tree.
type opType int

const (
	opAdd   opType = iota // add a new open item to a section
	opEdit                // replace an existing item's text
	opReply               // append a "user:" reply under an existing item
	opLog                 // append a user log line to Log
)

// pendingOp captures the one mutation an inline input produced, in a form that
// can be replayed against a different (freshly-loaded) board when the file
// changed underneath us. rawLine is the target item's verbatim source line (edit
// / reply); section is the section title it lives in / is added to; payload is
// the user's text.
type pendingOp struct {
	typ     opType
	section string
	rawLine string
	payload string
}

// applyOp re-applies op onto fresh (a board just reparsed from disk) and returns
// a one-line status describing what happened. It never loses the user's text: if
// the target item vanished from disk, the text is appended as a new item to the
// section rather than dropped.
func applyOp(fresh *board.Board, op pendingOp) string {
	switch op.typ {
	case opAdd:
		fresh.AddItem(op.section, op.payload)
		return "board changed, item re-added"
	case opLog:
		fresh.AppendLog("user", op.payload)
		return "board changed, log re-appended"
	case opEdit:
		if it := fresh.FindByRawInSection(op.section, op.rawLine); it != nil && it.IsItem() {
			it.Text = op.payload
			return "board changed, edit reapplied"
		}
		// Target gone: keep the user's edited text as a new item, never lose it.
		fresh.AddItem(op.section, op.payload)
		return "board changed, edited item was gone — kept your text"
	case opReply:
		if it := fresh.FindByRawInSection(op.section, op.rawLine); it != nil && it.IsItem() {
			fresh.AddReply(it, "user: "+op.payload)
			return "board changed, reply reapplied"
		}
		fresh.AddItem(op.section, "user: "+op.payload)
		return "board changed, reply target was gone — kept your text"
	}
	return "board changed, edit reapplied"
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

// openLink opens the [[wiki-link]] in the current item's text in $EDITOR
// (same suspend-and-resume pattern as openEditor), creating the linked notes
// file — and its directory — if it doesn't exist yet. With several links on
// the item, a chooser overlay picks which one. A no-op if there is no current
// item or its text has no links.
func (m *model) openLink() tea.Cmd {
	it := m.currentItem()
	if it == nil {
		return nil
	}
	links := board.ExtractLinks(it.DisplayText())
	switch len(links) {
	case 0:
		return nil
	case 1:
		return m.openLinkByName(links[0])
	}
	m.linkOpts = links
	m.linkCur = 0
	m.mode = modeLinkPick
	return nil
}

func (m *model) openLinkByName(name string) tea.Cmd {
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

func (m *model) handleLinkPickKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc", "ctrl+c":
		m.mode = modeBoard
	case "j", "down":
		if m.linkCur < len(m.linkOpts)-1 {
			m.linkCur++
		}
	case "k", "up":
		if m.linkCur > 0 {
			m.linkCur--
		}
	case "enter", "o":
		name := m.linkOpts[m.linkCur]
		m.mode = modeBoard
		return m, m.openLinkByName(name)
	}
	return m, nil
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
