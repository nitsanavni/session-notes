// Surprise recorder for the map view, ported from mm (see docs/mm-port.md).
//
// The map's spatial navigation sometimes lands somewhere the user's fingers
// didn't expect. Rather than guessing at fixes, the map keeps a ring buffer of
// the last feedbackWindow actions — each with a fully replayable before-state —
// and `!` opens a prompt showing what just happened ("you hit k · focus moved
// 'Log' → 'Working Agreements' — where did you expect it?"). The note plus the
// action trail is appended to <board>.feedback.jsonl; each record is a
// ready-made test fixture for tuning the navigation.
//
// Oversized records (the board markdown is embedded once per buffered event,
// so big boards multiply) are spilled as plain JSON files into a
// <board>.feedback/ sidecar directory and referenced from the .jsonl by a
// small stub, exactly like mm. Plain JSON (no gzip/base64) keeps the sidecar
// greppable and the reader trivial; every reader resolves stubs transparently.
package tui

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/nitsanavni/session-notes/internal/board"
)

// feedbackWindow is how many map actions the ring buffer keeps (mm's value).
const feedbackWindow = 20

// feedbackSidecarThreshold is the serialized-record size above which the
// record is spilled to the sidecar dir instead of inlined in the .jsonl.
const feedbackSidecarThreshold = 8 * 1024

// feedbackState is everything a map-nav decision depends on, captured so a
// (state, key) pair replays deterministically: the full board markdown plus
// the map-view knobs that shape the layout (folds, reply expansion, Log
// visibility) and the focused node's stable key ("" is the center).
type feedbackState struct {
	Board     string `json:"board"`
	FocusKey  string `json:"focusKey"`
	FocusText string `json:"focusText"`
	// FocusRoot is the stable key the map is re-rooted on (mm's `f` focus). ""
	// (the default, and absent in pre-focus JSONL records) is the whole board.
	FocusRoot string `json:"focusRoot,omitempty"`
	// FocusRootID is the durable node id of the focus root (M2): stable across
	// edits and shareable, unlike the positional FocusRoot key. Preferred on
	// restore; FocusRoot remains for pre-M2 records.
	FocusRootID string `json:"focusRootId,omitempty"`
	// Fold is the unified per-node fold state, keyed by stable node key. Replaces
	// the old Folded/RepliesShown pair; those are still read (see restoreMapModel)
	// so pre-existing JSONL records replay unchanged.
	Fold    map[string]foldState `json:"fold,omitempty"`
	ShowLog bool                 `json:"showLog,omitempty"`
	// Deprecated: read-only back-compat for records written before the fold
	// model was unified. Never written now (see captureMapState).
	Folded       []string `json:"folded,omitempty"`
	RepliesShown []string `json:"repliesShown,omitempty"`
}

// feedbackAfter is where an action landed: the focus after the key ran.
type feedbackAfter struct {
	FocusKey  string `json:"focusKey"`
	FocusText string `json:"focusText"`
}

// feedbackEvent is one ring-buffer entry: the key pressed, the full
// replayable state just before it, and where focus ended up.
type feedbackEvent struct {
	Key    string        `json:"key"`
	Before feedbackState `json:"before"`
	After  feedbackAfter `json:"after"`
}

// feedbackRecord is one line of <board>.feedback.jsonl. A stub (Ref != "",
// empty State.Board) stands in for a record spilled to the sidecar dir.
type feedbackRecord struct {
	TS       string          `json:"ts"`
	File     string          `json:"file,omitempty"`
	Feedback string          `json:"feedback"`
	State    feedbackState   `json:"state,omitempty"`
	Events   []feedbackEvent `json:"events,omitempty"`
	// Ref is set only on a stub: the spilled record's path relative to the
	// .jsonl's directory.
	Ref string `json:"ref,omitempty"`
}

func (r *feedbackRecord) isStub() bool { return r.Ref != "" && r.State.Board == "" }

// feedbackPath is the JSONL log's path for a board file.
func feedbackPath(boardPath string) string { return boardPath + ".feedback.jsonl" }

// feedbackSidecarDir is where oversized records spill: <board>.feedback/.
func feedbackSidecarDir(jsonlPath string) string {
	return strings.TrimSuffix(jsonlPath, ".jsonl")
}

// --- capture (model side) ---

// recordedMapKeys are the map actions worth replaying: navigation, folding,
// visibility toggles, and the one-keystroke mutations. Prompt-opening keys
// (a/e/!) and quit keys are not actions on the map itself.
var recordedMapKeys = map[string]bool{
	"h": true, "j": true, "k": true, "l": true,
	"left": true, "right": true, "up": true, "down": true,
	"d":     true,
	"enter": true, "w": true, " ": true, "D": true, "M": true,
	"u": true, "ctrl+r": true, "r": true,
	"f": true, "b": true, // focus in / out (re-rooting)
}

// cloneFold copies a fold-state map (nil for empty), so the recorded before-
// state is a snapshot independent of later mutations.
func cloneFold(m map[string]foldState) map[string]foldState {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]foldState, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// captureMapState snapshots the current map-relevant state. Cheap for board
// sizes we see (one Render of the markdown); only called on recorded map keys.
func (m *model) captureMapState() feedbackState {
	m.ensureMap()
	return feedbackState{
		Board:       m.board.Render(),
		FocusKey:    m.mapFocusKey,
		FocusText:   mapFocusDetail(m.mp),
		FocusRoot:   m.mapFocusRoot,
		FocusRootID: m.mapFocusRootID,
		Fold:        cloneFold(m.mapFold),
		ShowLog:     m.mapShowLog,
	}
}

// recordMapEvent appends one action to the ring buffer, dropping the oldest
// entry past feedbackWindow.
func (m *model) recordMapEvent(key string, before feedbackState) {
	m.ensureMap() // the action may have niled the layout (fold, M, mutation)
	m.feedbackEvents = append(m.feedbackEvents, feedbackEvent{
		Key:    key,
		Before: before,
		After:  feedbackAfter{FocusKey: m.mapFocusKey, FocusText: mapFocusDetail(m.mp)},
	})
	if len(m.feedbackEvents) > feedbackWindow {
		m.feedbackEvents = m.feedbackEvents[len(m.feedbackEvents)-feedbackWindow:]
	}
}

// feedbackKeyLabel renders a key for humans: arrows as glyphs, space spelled out.
func feedbackKeyLabel(key string) string {
	switch key {
	case "up":
		return "↑"
	case "down":
		return "↓"
	case "left":
		return "←"
	case "right":
		return "→"
	case " ":
		return "space"
	}
	return key
}

// lastMoveSummary is the dimmed context line above the `!` prompt: what the
// last buffered action did, phrased as a question about expectation.
func (m *model) lastMoveSummary() string {
	if len(m.feedbackEvents) == 0 {
		return "no map actions recorded yet — describe what surprised you"
	}
	e := m.feedbackEvents[len(m.feedbackEvents)-1]
	label := feedbackKeyLabel(e.Key)
	if e.Before.FocusKey == e.After.FocusKey {
		return fmt.Sprintf("you hit %s · focus stayed on %q — what did you expect?", label, e.After.FocusText)
	}
	return fmt.Sprintf("you hit %s · focus moved %q → %q — where did you expect it?", label, e.Before.FocusText, e.After.FocusText)
}

// saveMapFeedback builds and appends the feedback record for the user's note.
func (m *model) saveMapFeedback(note string) {
	rec := feedbackRecord{
		TS:       time.Now().UTC().Format(time.RFC3339),
		File:     filepath.Base(m.path),
		Feedback: note,
		State:    m.captureMapState(),
		Events:   append([]feedbackEvent(nil), m.feedbackEvents...),
	}
	if err := appendFeedback(feedbackPath(m.path), rec); err != nil {
		m.status = "feedback save failed: " + err.Error()
		return
	}
	m.status = "feedback saved → " + filepath.Base(feedbackPath(m.path))
}

// handleMapFeedbackKey drives the `!` prompt: enter saves, esc cancels.
func (m *model) handleMapFeedbackKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeBoard
		m.input.Blur()
		m.status = "feedback cancelled"
		return m, nil
	case "ctrl+c":
		return m, tea.Quit
	case "enter":
		text := strings.TrimSpace(m.input.Value())
		m.mode = modeBoard
		m.input.Blur()
		if text == "" {
			m.status = "feedback discarded (empty)"
			return m, nil
		}
		m.saveMapFeedback(text)
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// --- persistence ---

// appendFeedback appends rec to the JSONL log, spilling oversized records to
// the sidecar dir and appending a stub instead (see the package comment).
func appendFeedback(jsonlPath string, rec feedbackRecord) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	line := data
	if len(data) > feedbackSidecarThreshold {
		dir := feedbackSidecarDir(jsonlPath)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		name := strings.NewReplacer(":", "-", ".", "-").Replace(rec.TS) + ".json"
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			return err
		}
		stub := feedbackRecord{
			TS:       rec.TS,
			File:     rec.File,
			Feedback: rec.Feedback,
			Ref:      filepath.Join(filepath.Base(dir), name),
		}
		if line, err = json.Marshal(stub); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(jsonlPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

// readFeedback loads every record from the JSONL log, resolving sidecar stubs.
// Unresolvable lines (bad JSON, missing sidecar file) are reported as warnings
// and skipped rather than failing the whole listing.
func readFeedback(jsonlPath string) (recs []feedbackRecord, warnings []string, err error) {
	data, err := os.ReadFile(jsonlPath)
	if err != nil {
		return nil, nil, err
	}
	for i, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec feedbackRecord
		if uerr := json.Unmarshal([]byte(line), &rec); uerr != nil {
			warnings = append(warnings, fmt.Sprintf("line %d: bad JSON: %v", i+1, uerr))
			continue
		}
		if rec.isStub() {
			side := filepath.Join(filepath.Dir(jsonlPath), rec.Ref)
			sdata, serr := os.ReadFile(side)
			if serr != nil {
				warnings = append(warnings, fmt.Sprintf("line %d: sidecar missing — %s", i+1, side))
				continue
			}
			var full feedbackRecord
			if uerr := json.Unmarshal(sdata, &full); uerr != nil {
				warnings = append(warnings, fmt.Sprintf("line %d: sidecar %s: bad JSON: %v", i+1, side, uerr))
				continue
			}
			rec = full
		}
		recs = append(recs, rec)
	}
	return recs, warnings, nil
}

// --- replay ---

// feedbackKeyMsg turns a recorded key string back into the tea.KeyMsg the map
// handler switches on (the inverse of msg.String() for the keys we record).
func feedbackKeyMsg(key string) tea.KeyMsg {
	switch key {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "ctrl+r":
		return tea.KeyMsg{Type: tea.KeyCtrlR}
	case " ":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
}

// restoreMapModel builds a map-view model in a recorded before-state. The
// board path is empty, so mutating replays stay in memory (saves no-op with a
// status message) — replay never touches the recorded board's real file.
func restoreMapModel(st feedbackState) *model {
	m := newModel()
	m.board = board.Parse(st.Board)
	m.mapView = true
	m.mapShowLog = st.ShowLog
	m.mapFold = map[string]foldState{}
	for k, v := range st.Fold {
		m.mapFold[k] = v
	}
	// Back-compat: records written before the fold model was unified stored a
	// folded-set (fully collapsed) and a replies-shown set (replies expanded).
	for _, k := range st.Folded {
		m.mapFold[k] = foldCollapsed
	}
	for _, k := range st.RepliesShown {
		m.mapFold[k] = foldRepliesExpanded
	}
	m.mapFocusKey = st.FocusKey
	m.mapFocusRoot = st.FocusRoot
	// Durable id wins when present; else derive it from the positional key so a
	// pre-M2 record still gets a stable, edit-surviving focus root.
	if st.FocusRootID != "" {
		m.mapFocusRootID = st.FocusRootID
	} else {
		m.mapFocusRootID = m.nodeIDForKey(st.FocusRoot)
	}
	m.rebuildPositions()
	m.mp = nil
	m.ensureMap()
	return m
}

// replayMapEvent replays (before, key) through the real map handler and
// returns the observed landing — today's actual behavior, not the stored
// After (so generated tests pass as-generated).
func replayMapEvent(before feedbackState, key string) feedbackAfter {
	m := restoreMapModel(before)
	m.handleMapKey(feedbackKeyMsg(key))
	m.ensureMap()
	return feedbackAfter{FocusKey: m.mapFocusKey, FocusText: mapFocusDetail(m.mp)}
}

// --- CLI: session-notes feedback <board.md> ---

// RunFeedback implements the `session-notes feedback` subcommand: a
// human-readable listing of a board's surprise records (newest last), raw
// JSON with jsonOut, or ready-to-paste Go test source with genTest.
func RunFeedback(boardPath string, jsonOut, genTest bool) error {
	recs, warnings, err := readFeedback(feedbackPath(boardPath))
	if err != nil {
		return fmt.Errorf("read feedback for %s: %w", boardPath, err)
	}
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, "session-notes: feedback:", w)
	}
	switch {
	case genTest:
		fmt.Print(generateFeedbackTests(recs))
	case jsonOut:
		out, err := json.MarshalIndent(recs, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(out))
	default:
		printFeedback(os.Stdout, recs)
	}
	return nil
}

// printFeedback writes the human-readable listing, oldest first.
func printFeedback(w io.Writer, recs []feedbackRecord) {
	if len(recs) == 0 {
		fmt.Fprintln(w, "no feedback records")
		return
	}
	for i, r := range recs {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "─── #%d · %s ───\n", i+1, r.TS)
		fmt.Fprintf(w, "feedback: %s\n", r.Feedback)
		if len(r.Events) == 0 {
			continue
		}
		fmt.Fprintln(w, "\nrecent actions (oldest first):")
		for _, e := range r.Events {
			fmt.Fprintf(w, "  %-6s %q → %q\n", feedbackKeyLabel(e.Key), e.Before.FocusText, e.After.FocusText)
		}
		last := r.Events[len(r.Events)-1]
		fmt.Fprintf(w, "→ hit %s, landed on %q\n", feedbackKeyLabel(last.Key), last.After.FocusText)
	}
}

// --- gen-test ---

const genTestHeader = `// Generated by ` + "`session-notes feedback <board.md> --gen-test`" + `.
// Paste into internal/tui (package tui, a _test.go file) and rename the tests.
// Each test replays a recorded surprising move and asserts where it lands
// TODAY — flip the want line to the user's expectation to make it fail.

`

// generateFeedbackTests prints one Go test per record with buffered events,
// replaying the surprising (last) action to observe today's landing.
func generateFeedbackTests(recs []feedbackRecord) string {
	var b strings.Builder
	b.WriteString(genTestHeader)
	n := 0
	for _, r := range recs {
		if len(r.Events) == 0 {
			continue // nothing to replay
		}
		n++
		last := r.Events[len(r.Events)-1]
		landed := replayMapEvent(last.Before, last.Key)
		fmt.Fprintf(&b, "func TestMapSurprise%d(t *testing.T) {\n", n)
		fmt.Fprintf(&b, "\t// %s — %s\n", r.TS, oneLine(r.Feedback))
		fmt.Fprintf(&b, "\tsrc := %s\n", strconv.Quote(last.Before.Board))
		b.WriteString("\tm := newMapModel(t, src, 80, 24)\n")
		if last.Before.ShowLog {
			b.WriteString("\tm.mapShowLog = true\n")
		}
		if fold := mergedFold(last.Before); len(fold) > 0 {
			fmt.Fprintf(&b, "\tm.mapFold = map[string]foldState{%s}\n", foldLiteral(fold))
		}
		fmt.Fprintf(&b, "\tm.mapFocusKey = %s // %s\n", strconv.Quote(last.Before.FocusKey), oneLine(last.Before.FocusText))
		if last.Before.FocusRoot != "" {
			fmt.Fprintf(&b, "\tm.mapFocusRoot = %s\n", strconv.Quote(last.Before.FocusRoot))
		}
		if last.Before.FocusRootID != "" {
			fmt.Fprintf(&b, "\tm.mapFocusRootID = %s\n", strconv.Quote(last.Before.FocusRootID))
		}
		b.WriteString("\tm.mp = nil\n\tm.ensureMap()\n\n")
		fmt.Fprintf(&b, "\tm.handleMapKey(keyPress(%s)) // the surprising move\n\n", strconv.Quote(last.Key))
		fmt.Fprintf(&b, "\tif got := m.mapFocusKey; got != %s { // observed landing: %s\n", strconv.Quote(landed.FocusKey), oneLine(landed.FocusText))
		fmt.Fprintf(&b, "\t\tt.Errorf(\"focus key = %%q, want %%q\", got, %s)\n\t}\n", strconv.Quote(landed.FocusKey))
		fmt.Fprintf(&b, "\t// TODO: the user expected: %q — flip the want above to encode it.\n}\n\n", oneLine(r.Feedback))
	}
	if n == 0 {
		b.WriteString("// (no records with replayable events)\n")
	}
	return b.String()
}

// oneLine collapses whitespace so free text fits in a comment.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// mergedFold resolves a before-state's fold map, folding in any legacy
// Folded/RepliesShown sets so gen-test reproduces old records too.
func mergedFold(st feedbackState) map[string]foldState {
	out := map[string]foldState{}
	for k, v := range st.Fold {
		out[k] = v
	}
	for _, k := range st.Folded {
		out[k] = foldCollapsed
	}
	for _, k := range st.RepliesShown {
		out[k] = foldRepliesExpanded
	}
	return out
}

// foldName is the source-literal name of a fold state, for gen-test output.
func foldName(s foldState) string {
	switch s {
	case foldCollapsed:
		return "foldCollapsed"
	case foldRepliesExpanded:
		return "foldRepliesExpanded"
	default:
		return "foldDefault"
	}
}

// foldLiteral renders the body of a map[string]foldState literal for gen-test,
// keys sorted for stable output.
func foldLiteral(fold map[string]foldState) string {
	keys := make([]string, 0, len(fold))
	for k := range fold {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = strconv.Quote(k) + ": " + foldName(fold[k])
	}
	return strings.Join(parts, ", ")
}
