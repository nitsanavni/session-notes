package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const feedbackSrc = "---\ntitle: My Board\n---\n## Plan\n- [ ] first\n\n## Working Agreements\n- [ ] pair daily\n\n## Log\n- note\n"

// TestRecorderRing asserts the ring buffer records recorded keys only, keeps
// before/after focus, and stays bounded at feedbackWindow entries.
func TestRecorderRing(t *testing.T) {
	m := newMapModel(t, feedbackSrc, 80, 24)

	m.handleMapKey(keyPress("a")) // prompt-opening key: not recorded
	m.handleMapInputKey(keyPress("esc"))
	if len(m.feedbackEvents) != 0 {
		t.Fatalf("prompt key recorded: %d events", len(m.feedbackEvents))
	}

	m.handleMapKey(keyPress("l")) // center -> some first-ring node
	if len(m.feedbackEvents) != 1 {
		t.Fatalf("nav key not recorded: %d events", len(m.feedbackEvents))
	}
	e := m.feedbackEvents[0]
	if e.Key != "l" {
		t.Errorf("key = %q, want l", e.Key)
	}
	if e.Before.FocusKey != "" {
		t.Errorf("before focus = %q, want center", e.Before.FocusKey)
	}
	if e.After.FocusKey == "" {
		t.Errorf("after focus still the center; move not captured")
	}
	if !strings.Contains(e.Before.Board, "## Plan") {
		t.Errorf("before-state board not embedded:\n%s", e.Before.Board)
	}

	for i := 0; i < feedbackWindow+7; i++ {
		key := "j"
		if i%2 == 1 {
			key = "k"
		}
		m.handleMapKey(keyPress(key))
	}
	if len(m.feedbackEvents) != feedbackWindow {
		t.Errorf("ring size = %d, want %d", len(m.feedbackEvents), feedbackWindow)
	}
}

// TestRecorderCapturesMapKnobs asserts fold/log-visibility state rides along
// in the before-state so a replay reproduces the same layout.
func TestRecorderCapturesMapKnobs(t *testing.T) {
	m := newMapModel(t, feedbackSrc, 80, 24)
	m.mapShowLog = true
	m.mp = nil
	m.ensureMap()
	focusMapSection(t, m, "Plan")
	m.handleMapKey(keyPress("enter")) // fold Plan — recorded, and sets mapFold
	m.handleMapKey(keyPress("j"))
	last := m.feedbackEvents[len(m.feedbackEvents)-1]
	if !last.Before.ShowLog {
		t.Errorf("showLog not captured")
	}
	if got := last.Before.Fold["s0"]; got != foldCollapsed {
		t.Errorf("fold[s0] = %v, want foldCollapsed", got)
	}
	if len(last.Before.Fold) != 1 {
		t.Errorf("fold = %v, want a single s0 entry", last.Before.Fold)
	}
}

// TestFeedbackBangWritesRecord drives `!` end to end: prompt opens with the
// last-move summary, the note plus the event trail lands as one well-formed
// JSONL record next to the (temp) board.
func TestFeedbackBangWritesRecord(t *testing.T) {
	m := newMapModel(t, feedbackSrc, 80, 24)
	m.handleMapKey(keyPress("l"))
	m.handleMapKey(keyPress("j"))

	m.handleMapKey(keyPress("S"))
	if m.mode != modeMapFeedback {
		t.Fatalf("S did not enter feedback mode, got %v", m.mode)
	}
	sum := m.lastMoveSummary()
	if !strings.Contains(sum, "you hit j") {
		t.Errorf("summary %q does not name the key", sum)
	}
	m.input.SetValue("expected j to stay in the same section")
	m.handleMapFeedbackKey(keyPress("enter"))
	if m.mode != modeBoard {
		t.Fatalf("enter did not close the prompt, mode %v", m.mode)
	}
	if !strings.Contains(m.status, "feedback saved") {
		t.Errorf("status = %q, want feedback saved", m.status)
	}

	data, err := os.ReadFile(feedbackPath(m.path))
	if err != nil {
		t.Fatalf("no feedback file: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("want 1 JSONL line, got %d", len(lines))
	}
	var rec feedbackRecord
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("record is not valid JSON: %v", err)
	}
	if rec.Feedback != "expected j to stay in the same section" {
		t.Errorf("feedback = %q", rec.Feedback)
	}
	if rec.TS == "" || rec.File == "" {
		t.Errorf("missing ts/file: %+v", rec)
	}
	if len(rec.Events) != 2 {
		t.Fatalf("events = %d, want 2", len(rec.Events))
	}
	if rec.Events[1].Key != "j" || rec.Events[1].Before.Board == "" {
		t.Errorf("last event not replayable: %+v", rec.Events[1])
	}
	if rec.State.Board == "" {
		t.Errorf("record state has no board content")
	}
}

// TestFeedbackEmptyNoteDiscarded asserts an empty submission writes nothing.
func TestFeedbackEmptyNoteDiscarded(t *testing.T) {
	m := newMapModel(t, feedbackSrc, 80, 24)
	m.handleMapKey(keyPress("j"))
	m.handleMapKey(keyPress("!"))
	m.handleMapFeedbackKey(keyPress("enter"))
	if _, err := os.Stat(feedbackPath(m.path)); !os.IsNotExist(err) {
		t.Errorf("empty note created a feedback file")
	}
	if !strings.Contains(m.status, "discarded") {
		t.Errorf("status = %q", m.status)
	}
}

// TestFeedbackSidecarSpill asserts a record whose JSON exceeds the threshold
// is spilled to <board>.feedback/ and replaced by a resolvable stub.
func TestFeedbackSidecarSpill(t *testing.T) {
	dir := t.TempDir()
	jsonl := filepath.Join(dir, "big.md.feedback.jsonl")
	big := feedbackRecord{
		TS:       "2026-07-09T12:00:00Z",
		File:     "big.md",
		Feedback: "huge board",
		State:    feedbackState{Board: strings.Repeat("- [ ] x\n", 3000), FocusKey: "s0"},
	}
	if err := appendFeedback(jsonl, big); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(jsonl)
	if len(data) > 1024 {
		t.Errorf("stub line too big (%d bytes) — record not spilled", len(data))
	}
	var stub feedbackRecord
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &stub); err != nil {
		t.Fatal(err)
	}
	if !stub.isStub() {
		t.Fatalf("jsonl line is not a stub: %+v", stub)
	}
	if _, err := os.Stat(filepath.Join(dir, stub.Ref)); err != nil {
		t.Errorf("sidecar file missing: %v", err)
	}

	recs, warns, err := readFeedback(jsonl)
	if err != nil || len(warns) != 0 {
		t.Fatalf("read: err=%v warns=%v", err, warns)
	}
	if len(recs) != 1 || recs[0].State.Board != big.State.Board {
		t.Errorf("stub did not resolve to the full record")
	}
}

// TestFeedbackListingParses round-trips: what `!` wrote, the listing reads and
// formats with the action trail.
func TestFeedbackListingParses(t *testing.T) {
	m := newMapModel(t, feedbackSrc, 80, 24)
	m.handleMapKey(keyPress("l"))
	m.handleMapKey(keyPress("!"))
	m.input.SetValue("surprise note")
	m.handleMapFeedbackKey(keyPress("enter"))

	recs, warns, err := readFeedback(feedbackPath(m.path))
	if err != nil || len(warns) != 0 || len(recs) != 1 {
		t.Fatalf("read: err=%v warns=%v n=%d", err, warns, len(recs))
	}
	var b strings.Builder
	printFeedback(&b, recs)
	out := b.String()
	for _, want := range []string{"#1", "feedback: surprise note", "recent actions", "→ hit l"} {
		if !strings.Contains(out, want) {
			t.Errorf("listing missing %q:\n%s", want, out)
		}
	}
}

// TestGenTestOutput asserts the generated source replays the recorded move,
// asserts the observed landing, and carries the TODO with the user's note.
func TestGenTestOutput(t *testing.T) {
	m := newMapModel(t, feedbackSrc, 80, 24)
	focusMapSection(t, m, "Working Agreements")
	m.handleMapKey(keyPress("k")) // the "surprising" move
	m.handleMapKey(keyPress("!"))
	m.input.SetValue("expected k to land on Plan")
	m.handleMapFeedbackKey(keyPress("enter"))

	recs, _, err := readFeedback(feedbackPath(m.path))
	if err != nil {
		t.Fatal(err)
	}
	src := generateFeedbackTests(recs)

	// The landing the generator asserts must be today's actual behavior:
	// replay the same event here and expect its focus key in the output.
	last := recs[0].Events[len(recs[0].Events)-1]
	landed := replayMapEvent(last.Before, last.Key)
	for _, want := range []string{
		"func TestMapSurprise1(t *testing.T)",
		"newMapModel(t, src, 80, 24)",
		`keyPress("k")`,
		`got != ` + `"` + landed.FocusKey + `"`,
		"TODO: the user expected: \"expected k to land on Plan\"",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("gen-test output missing %q:\n%s", want, src)
		}
	}
}

// TestReplayMapEventDeterministic asserts replay reproduces the landing the
// live session observed, on the same board and state.
func TestReplayMapEventDeterministic(t *testing.T) {
	m := newMapModel(t, feedbackSrc, 80, 24)
	focusMapSection(t, m, "Working Agreements")
	m.handleMapKey(keyPress("k"))
	e := m.feedbackEvents[0]
	landed := replayMapEvent(e.Before, e.Key)
	if landed.FocusKey != e.After.FocusKey {
		t.Errorf("replay landed %q, live landed %q", landed.FocusKey, e.After.FocusKey)
	}
}
