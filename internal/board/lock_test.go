package board

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestWithLockMutualExclusion asserts that two goroutines contending on the same
// board's lock never run their critical sections concurrently.
func TestWithLockMutualExclusion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "b.md")

	var inside int32   // number of goroutines currently inside the critical section
	var maxSeen int32  // high-water mark of `inside`
	var overlaps int32 // times we observed >1 goroutine inside at once

	const workers = 8
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				err := WithLock(path, func() error {
					n := atomic.AddInt32(&inside, 1)
					if n > 1 {
						atomic.AddInt32(&overlaps, 1)
					}
					for {
						old := atomic.LoadInt32(&maxSeen)
						if n <= old || atomic.CompareAndSwapInt32(&maxSeen, old, n) {
							break
						}
					}
					// Hold briefly so a missing lock would reliably overlap.
					time.Sleep(200 * time.Microsecond)
					atomic.AddInt32(&inside, -1)
					return nil
				})
				if err != nil {
					t.Errorf("WithLock: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	if overlaps != 0 {
		t.Errorf("observed %d overlapping critical sections; lock is not exclusive", overlaps)
	}
	if maxSeen != 1 {
		t.Errorf("max goroutines inside lock = %d, want 1", maxSeen)
	}
}

// TestWithLockErrorPropagates checks the callback error is returned unchanged.
func TestWithLockErrorPropagates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "b.md")
	want := os.ErrClosed
	if got := WithLock(path, func() error { return want }); got != want {
		t.Errorf("WithLock err = %v, want %v", got, want)
	}
}

// TestSaveRebasingNoExternalChange: when disk matches lastDisk, the in-memory
// tree is written verbatim and no rebase is reported.
func TestSaveRebasingNoExternalChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "b.md")
	src := "## Plan\n- [ ] a\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	b := Parse(src)
	b.Path = path
	b.AddItem("Plan", "b")

	called := false
	res, err := b.SaveRebasing(path, src, func(*Board) { called = true })
	if err != nil {
		t.Fatal(err)
	}
	if res.Rebased {
		t.Error("unexpected rebase when disk == lastDisk")
	}
	if called {
		t.Error("reapply should not run without an external change")
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "- [ ] b") {
		t.Errorf("added item missing: %q", data)
	}
}

// TestSaveRebasingEditOntoChangedDisk: the external write added a line; our edit
// is re-applied onto the fresh tree so BOTH survive.
func TestSaveRebasingEditOntoChangedDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "b.md")
	lastDisk := "## Threads\n- [ ] alpha\n- [ ] beta\n"
	if err := os.WriteFile(path, []byte(lastDisk), 0o644); err != nil {
		t.Fatal(err)
	}
	// We loaded lastDisk and are about to edit "alpha" -> "alpha edited".
	b := Parse(lastDisk)
	b.Path = path
	target := b.Section("Threads").Items[0] // the "- [ ] alpha" line
	rawLine := target.Raw()
	target.Text = "alpha edited"

	// Meanwhile, an external writer appended a line and renamed the file in.
	external := "## Threads\n- [ ] alpha\n- [ ] beta\n- [ ] gamma from claude\n"
	if err := os.WriteFile(path, []byte(external), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := b.SaveRebasing(path, lastDisk, func(fresh *Board) {
		if it := fresh.FindByRawInSection("Threads", rawLine); it != nil {
			it.Text = "alpha edited"
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Rebased {
		t.Fatal("expected a rebase")
	}
	if res.DiskPrev != external {
		t.Errorf("DiskPrev = %q, want the external content", res.DiskPrev)
	}
	final, _ := os.ReadFile(path)
	got := string(final)
	if !strings.Contains(got, "- [ ] alpha edited") {
		t.Errorf("user edit lost: %q", got)
	}
	if !strings.Contains(got, "- [ ] gamma from claude") {
		t.Errorf("external line clobbered: %q", got)
	}
}

// TestSaveRebasingAddAppendedWhenSectionMoved: the external write reordered the
// board (section moved); an add re-applies as an append to the named section.
func TestSaveRebasingAddAppendedWhenSectionMoved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "b.md")
	lastDisk := "## Plan\n- [ ] p1\n\n## Threads\n- [ ] t1\n"
	if err := os.WriteFile(path, []byte(lastDisk), 0o644); err != nil {
		t.Fatal(err)
	}
	b := Parse(lastDisk)
	b.Path = path
	b.AddItem("Threads", "user new thread")

	// External writer swapped section order and added content.
	external := "## Threads\n- [ ] t1\n- [ ] t2 from claude\n\n## Plan\n- [ ] p1\n"
	if err := os.WriteFile(path, []byte(external), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := b.SaveRebasing(path, lastDisk, func(fresh *Board) {
		fresh.AddItem("Threads", "user new thread")
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Rebased {
		t.Fatal("expected a rebase")
	}
	got := res.Content
	if !strings.Contains(got, "- [ ] user new thread") {
		t.Errorf("user add lost: %q", got)
	}
	if !strings.Contains(got, "- [ ] t2 from claude") {
		t.Errorf("external add clobbered: %q", got)
	}
	// The user's item must live under Threads, not Plan.
	threads := res.Board.Section("Threads")
	found := false
	for _, it := range threads.Items {
		if it.DisplayText() == "user new thread" {
			found = true
		}
	}
	if !found {
		t.Errorf("user item not under Threads: %q", got)
	}
}

// TestFindByRawInSection covers exact-line lookup, the nested-reply case, and
// the empty-raw guard.
func TestFindByRawInSection(t *testing.T) {
	b := Parse("## Threads\n- [ ] top\n  - user: a reply\n- [ ] other\n")
	if it := b.FindByRawInSection("Threads", "- [ ] top"); it == nil || it.DisplayText() != "top" {
		t.Errorf("top not found: %v", it)
	}
	if it := b.FindByRawInSection("Threads", "  - user: a reply"); it == nil || it.DisplayText() != "user: a reply" {
		t.Errorf("nested reply not found: %v", it)
	}
	if it := b.FindByRawInSection("Threads", "- [ ] missing"); it != nil {
		t.Errorf("unexpected match for missing line: %v", it)
	}
	if it := b.FindByRawInSection("Nope", "- [ ] top"); it != nil {
		t.Errorf("match in nonexistent section: %v", it)
	}
	if it := b.FindByRawInSection("Threads", ""); it != nil {
		t.Errorf("empty raw must never match: %v", it)
	}
}
