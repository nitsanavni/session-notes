package board

// $EDITOR 3-way merge.
//
// $EDITOR is not lock-aware: if it edited the board file directly, Claude's
// concurrent writes would be clobbered when the editor saves. So the TUI edits a
// temp copy and hands the result here; SaveEditorMerge recovers Claude's writes
// ("theirs") and 3-way merges them with the user's edit ("mine") over the shared
// base. Non-auto-mergeable conflicts are NEVER resolved by favoring the user:
// the conflict markers are written verbatim (both sides preserved) and an urgent
// @claude item asks Claude to reconcile.

import (
	"errors"
	"os"
	"os/exec"
	"strings"
)

// editorConflictPrompt is the text of the urgent @claude item added to the board
// whenever a $EDITOR merge could not be auto-reconciled. It points at the
// safety-net sidecars so the pre-merge versions are discoverable.
const editorConflictPrompt = "@claude $EDITOR merge conflict in the board above — reconcile the <<<<<<< / ======= / >>>>>>> markers, integrating BOTH the user's edit and your writes. Never drop the user's text; remove the markers when done. Full pre-merge copies are saved alongside the board as <board>.conflict-mine (the user's editor version) and <board>.conflict-theirs (your version) for recovery."

// Sidecar suffixes for the pre-merge safety-net copies written on a conflict.
const (
	conflictMineSuffix   = ".conflict-mine"
	conflictTheirsSuffix = ".conflict-theirs"
)

// EditorMergeResult reports the outcome of SaveEditorMerge.
type EditorMergeResult struct {
	// Content is the exact markdown written to disk.
	Content string
	// Theirs is the board content found on disk at merge time — Claude's
	// concurrent writes, if any. Equals base when nothing changed underneath.
	Theirs string
	// Conflicted is true when the merge could not auto-reconcile: conflict
	// markers were written and an urgent @claude item was added.
	Conflicted bool
}

// SaveEditorMerge reconciles a user's $EDITOR session (which edited a temp copy
// of the board, saved as mine) with any writes Claude made to the real board
// while the editor was open, then atomically persists the result. base is the
// board content captured when the editor launched. The whole read → merge →
// write runs inside one WithLock(path), so no external writer can interleave.
//
// Outcomes (see EditorMergeResult):
//   - no concurrent change (disk == base) or board deleted (disk == "") → mine
//     is written verbatim;
//   - a clean 3-way merge → the merged text is written, Conflicted=false;
//   - a conflicting merge → the conflict markers are written verbatim with an
//     urgent @claude item, Conflicted=true, favoring NEITHER side;
//   - git unavailable → Claude's board is preserved and mine is appended under a
//     "Editor Merge (needs reconcile)" section with the same urgent item, so the
//     user's text is never silently dropped, Conflicted=true.
//
// On any conflicted outcome, the two pre-merge full versions are also written to
// sidecar files (<board>.conflict-mine and <board>.conflict-theirs) so nothing
// is unrecoverable even if the marker reconciliation goes wrong.
func SaveEditorMerge(path, base, mine string) (EditorMergeResult, error) {
	var res EditorMergeResult
	err := WithLock(path, func() error {
		data, rerr := os.ReadFile(path)
		if rerr != nil && !os.IsNotExist(rerr) {
			return rerr
		}
		theirs := string(data)
		res.Theirs = theirs

		// No concurrent change (or the board vanished mid-session): the user's
		// edit is authoritative, write it directly.
		if theirs == "" || theirs == base {
			res.Content = mine
			return writeFileAtomic(path, mine)
		}

		merged, conflicts, mergeErr := gitMergeFile(mine, base, theirs)
		if mergeErr != nil {
			// git unavailable / exec failure: favor NEITHER side. Keep Claude's
			// current board and append the user's whole buffer under a dedicated
			// section for manual reconciliation, so mine is never dropped.
			b := Parse(theirs)
			b.Path = path
			sec := &Section{Title: "Editor Merge (needs reconcile)"}
			for _, line := range strings.Split(strings.TrimRight(mine, "\n"), "\n") {
				sec.Items = append(sec.Items, &Item{raw: line})
			}
			b.Sections = append(b.Sections, sec)
			addEditorConflictQuestion(b)
			writeConflictSidecars(path, mine, theirs)
			res.Content = b.Render()
			res.Conflicted = true
			return writeFileAtomic(path, res.Content)
		}

		if conflicts == 0 {
			// Clean 3-way merge (the unchanged happy path): write git's output
			// verbatim so disjoint edits combine byte-for-byte.
			res.Content = merged
			return writeFileAtomic(path, merged)
		}

		// Conflicts: markers are in merged, both sides verbatim and lossless. Do
		// NOT favor the user. Reparse (lossless) and add an urgent @claude item
		// asking Claude to reconcile the markers, then write.
		b := Parse(merged)
		b.Path = path
		addEditorConflictQuestion(b)
		writeConflictSidecars(path, mine, theirs)
		res.Content = b.Render()
		res.Conflicted = true
		return writeFileAtomic(path, res.Content)
	})
	return res, err
}

// addEditorConflictQuestion appends the urgent @claude reconcile prompt to the
// Questions section (created if missing).
func addEditorConflictQuestion(b *Board) {
	q := b.AddItem("Questions", editorConflictPrompt)
	q.Urgent = true
}

// writeConflictSidecars writes the two pre-merge full versions next to the board
// as a safety net: <board>.conflict-mine (the user's editor copy) and
// <board>.conflict-theirs (Claude's disk version). Best-effort — a failure here
// never blocks the merge write, since the conflicted board itself already holds
// both sides verbatim.
func writeConflictSidecars(path, mine, theirs string) {
	_ = os.WriteFile(path+conflictMineSuffix, []byte(mine), 0o644)
	_ = os.WriteFile(path+conflictTheirsSuffix, []byte(theirs), 0o644)
}

// gitMergeFile 3-way merges mine and theirs over the common base using
// `git merge-file -p`. It returns the merged text (with conflict markers when
// the sides disagree), the number of conflicts (0 = clean, matching git's exit
// code), and a non-nil error only when git could not be run (missing binary or
// a real merge-file failure), signaling the caller's git-unavailable fallback.
func gitMergeFile(mine, base, theirs string) (string, int, error) {
	mineF, err := writeTempFile(mine)
	if err != nil {
		return "", 0, err
	}
	defer os.Remove(mineF)
	baseF, err := writeTempFile(base)
	if err != nil {
		return "", 0, err
	}
	defer os.Remove(baseF)
	theirsF, err := writeTempFile(theirs)
	if err != nil {
		return "", 0, err
	}
	defer os.Remove(theirsF)

	// git merge-file incorporates the base->theirs change into mine, so the
	// -L labels name file1 (mine), the base, and file2 (theirs) in that order.
	cmd := exec.Command("git", "merge-file", "-p",
		"-L", "your $EDITOR change", "-L", "board (base)", "-L", "claude",
		mineF, baseF, theirsF) // #nosec G204 -- fixed args, temp file paths
	out, err := cmd.Output()
	if err == nil {
		return string(out), 0, nil
	}
	// Exit code = number of conflicts when >0; markers are in stdout. Any other
	// failure (binary not found, or merge-file's -1/255 trouble code) falls back.
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() > 0 && ee.ExitCode() != 255 {
		return string(out), ee.ExitCode(), nil
	}
	return "", 0, err
}

// writeTempFile writes content to a fresh temp file and returns its path; the
// caller is responsible for removing it.
func writeTempFile(content string) (string, error) {
	f, err := os.CreateTemp("", "board-merge-*.md")
	if err != nil {
		return "", err
	}
	name := f.Name()
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		os.Remove(name)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}

// writeFileAtomic writes raw content to path via a temp file + rename, WITHOUT
// taking the lock (callers already hold WithLock(path)). Unlike Board.writeAtomic
// it writes the given bytes verbatim rather than a re-rendered board, so merge
// output (including conflict markers) is preserved exactly.
func writeFileAtomic(path, content string) error {
	dir := "."
	if idx := strings.LastIndexByte(path, '/'); idx >= 0 {
		dir = path[:idx]
	}
	tmp, err := os.CreateTemp(dir, ".board-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after successful rename
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
