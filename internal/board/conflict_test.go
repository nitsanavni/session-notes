package board

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSaveEditorMergeClean: the user edits one region in the editor while Claude
// edits a disjoint region on the real board. The 3-way merge combines both with
// no conflict.
func TestSaveEditorMergeClean(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "b.md")
	base := "## Plan\n- [ ] step one\n\n## Log\n- 10:00 start: began\n"
	if err := os.WriteFile(path, []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}
	// Claude appended a Log line to the real board while the editor was open.
	theirs := "## Plan\n- [ ] step one\n\n## Log\n- 10:00 start: began\n- 10:05 claude: did work\n"
	if err := os.WriteFile(path, []byte(theirs), 0o644); err != nil {
		t.Fatal(err)
	}
	// The user edited the Plan step in the editor (temp copy seeded from base).
	mine := "## Plan\n- [ ] step one EDITED\n\n## Log\n- 10:00 start: began\n"

	res, err := SaveEditorMerge(path, base, mine)
	if err != nil {
		t.Fatal(err)
	}
	if res.Conflicted {
		t.Errorf("expected a clean merge, got Conflicted=true: %q", res.Content)
	}
	if res.Theirs != theirs {
		t.Errorf("Theirs = %q, want the disk content", res.Theirs)
	}
	got, _ := os.ReadFile(path)
	content := string(got)
	if !strings.Contains(content, "step one EDITED") {
		t.Errorf("user edit lost: %q", content)
	}
	if !strings.Contains(content, "10:05 claude: did work") {
		t.Errorf("claude's write clobbered: %q", content)
	}
	if strings.Contains(content, "<<<<<<<") {
		t.Errorf("unexpected conflict markers: %q", content)
	}
}

// TestSaveEditorMergeConflict: both sides changed the same line differently. The
// board must keep BOTH sides in conflict markers (nothing dropped), add an urgent
// @claude Questions item, report Conflicted, and drop safety-net sidecars.
func TestSaveEditorMergeConflict(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "b.md")
	base := "## Plan\n- [ ] shared line\n"
	if err := os.WriteFile(path, []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}
	theirs := "## Plan\n- [ ] shared line B\n"
	if err := os.WriteFile(path, []byte(theirs), 0o644); err != nil {
		t.Fatal(err)
	}
	mine := "## Plan\n- [ ] shared line A\n"

	res, err := SaveEditorMerge(path, base, mine)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Conflicted {
		t.Fatalf("expected Conflicted=true, got: %q", res.Content)
	}
	got, _ := os.ReadFile(path)
	content := string(got)
	if !strings.Contains(content, "shared line A") || !strings.Contains(content, "shared line B") {
		t.Errorf("a side was dropped: %q", content)
	}
	if !strings.Contains(content, "<<<<<<<") || !strings.Contains(content, ">>>>>>>") {
		t.Errorf("conflict markers missing: %q", content)
	}
	// An urgent @claude reconcile item must exist in Questions.
	b := Parse(content)
	urgent := b.UrgentOpenItems()
	foundQ := false
	for _, it := range urgent {
		if strings.Contains(it.DisplayText(), "@claude") && strings.Contains(it.DisplayText(), "conflict") {
			foundQ = true
		}
	}
	if !foundQ {
		t.Errorf("urgent @claude conflict item missing: %q", content)
	}
	if b.Section("Questions") == nil {
		t.Errorf("Questions section missing: %q", content)
	}
	// Safety-net sidecars hold the full pre-merge versions.
	if d, err := os.ReadFile(path + conflictMineSuffix); err != nil || string(d) != mine {
		t.Errorf("conflict-mine sidecar = %q (err %v), want %q", d, err, mine)
	}
	if d, err := os.ReadFile(path + conflictTheirsSuffix); err != nil || string(d) != theirs {
		t.Errorf("conflict-theirs sidecar = %q (err %v), want %q", d, err, theirs)
	}
}

// TestSaveEditorMergeNoConcurrentChange: disk still equals base, so the user's
// buffer is written verbatim with no markers.
func TestSaveEditorMergeNoConcurrentChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "b.md")
	base := "## Plan\n- [ ] a\n"
	if err := os.WriteFile(path, []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}
	mine := "## Plan\n- [ ] a\n- [ ] b added in editor\n"

	res, err := SaveEditorMerge(path, base, mine)
	if err != nil {
		t.Fatal(err)
	}
	if res.Conflicted {
		t.Errorf("unexpected conflict: %q", res.Content)
	}
	if res.Theirs != base {
		t.Errorf("Theirs = %q, want base", res.Theirs)
	}
	got, _ := os.ReadFile(path)
	if string(got) != mine {
		t.Errorf("board = %q, want mine verbatim %q", got, mine)
	}
	if strings.Contains(string(got), "<<<<<<<") {
		t.Errorf("unexpected markers: %q", got)
	}
}

// TestSaveEditorMergeBoardDeleted: the board vanished mid-session (disk empty),
// so mine is written directly rather than lost.
func TestSaveEditorMergeBoardDeleted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "b.md")
	base := "## Plan\n- [ ] a\n"
	mine := "## Plan\n- [ ] a edited\n"
	// Note: file does not exist on disk.

	res, err := SaveEditorMerge(path, base, mine)
	if err != nil {
		t.Fatal(err)
	}
	if res.Conflicted {
		t.Errorf("unexpected conflict: %q", res.Content)
	}
	got, _ := os.ReadFile(path)
	if string(got) != mine {
		t.Errorf("board = %q, want mine %q", got, mine)
	}
}

// TestSaveEditorMergeGitMissing: with git unreachable, favor NEITHER side —
// preserve Claude's board and append the user's whole buffer under a reconcile
// section, plus the urgent @claude item. Nothing is dropped.
func TestSaveEditorMergeGitMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "b.md")
	base := "## Plan\n- [ ] shared\n"
	if err := os.WriteFile(path, []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}
	theirs := "## Plan\n- [ ] shared\n- [ ] claude added\n"
	if err := os.WriteFile(path, []byte(theirs), 0o644); err != nil {
		t.Fatal(err)
	}
	mine := "## Plan\n- [ ] shared edited by user\n"

	// Shim PATH to a directory without `git` so exec fails at lookup.
	t.Setenv("PATH", t.TempDir())

	res, err := SaveEditorMerge(path, base, mine)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Conflicted {
		t.Fatalf("expected Conflicted=true on git-missing fallback: %q", res.Content)
	}
	got, _ := os.ReadFile(path)
	content := string(got)
	if !strings.Contains(content, "claude added") {
		t.Errorf("claude's board not preserved: %q", content)
	}
	if !strings.Contains(content, "shared edited by user") {
		t.Errorf("user's buffer dropped: %q", content)
	}
	if !strings.Contains(content, "Editor Merge (needs reconcile)") {
		t.Errorf("reconcile section missing: %q", content)
	}
	b := Parse(content)
	if len(b.UrgentOpenItems()) == 0 {
		t.Errorf("urgent @claude item missing: %q", content)
	}
}

// TestSaveEditorMergeLockSerialized asserts the whole read-theirs → write runs
// under the board lock: a merge started while another writer holds the lock
// blocks until release, and then observes exactly the content that writer left.
func TestSaveEditorMergeLockSerialized(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "b.md")
	base := "## Plan\n- [ ] base\n"
	if err := os.WriteFile(path, []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	underLock := "## Plan\n- [ ] claude wrote this under the lock\n"

	// A competing writer holds the lock, then writes its version and releases.
	go func() {
		_ = WithLock(path, func() error {
			close(entered)
			<-release
			return writeFileAtomic(path, underLock)
		})
	}()
	<-entered

	type out struct {
		res EditorMergeResult
		err error
	}
	done := make(chan out, 1)
	mine := "## Plan\n- [ ] user edited\n"
	go func() {
		res, err := SaveEditorMerge(path, base, mine)
		done <- out{res, err}
	}()

	// While the lock is held, the merge must not complete.
	select {
	case <-done:
		t.Fatal("SaveEditorMerge returned while the lock was held")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.res.Theirs != underLock {
		t.Errorf("Theirs = %q, want the under-lock write %q", got.res.Theirs, underLock)
	}
}
