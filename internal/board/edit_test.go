package board

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindByQuery(t *testing.T) {
	b := Parse("## Plan\n- [ ] alpha task\n- [>] beta task\n  - claude: beta reply\n")
	it, n := b.FindByQuery("beta")
	if n != 2 {
		t.Fatalf("count=%d want 2 (item + reply both contain beta)", n)
	}
	if it.DisplayText() != "beta task" {
		t.Errorf("first match=%q want the beta task (document order)", it.DisplayText())
	}
	if _, n := b.FindByQuery("gamma"); n != 0 {
		t.Errorf("count=%d want 0 for no match", n)
	}
}

func TestEditUnderLockRefreshesSnapshot(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "b.md")
	snap := filepath.Join(dir, "snap")
	if err := os.WriteFile(p, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := EditUnderLock(p, snap, func(c string) (string, error) {
		return c + "world\n", nil
	})
	if err != nil {
		t.Fatalf("EditUnderLock: %v", err)
	}
	got, _ := os.ReadFile(p)
	sgot, _ := os.ReadFile(snap)
	if string(got) != "hello\nworld\n" {
		t.Errorf("board=%q", got)
	}
	if string(sgot) != string(got) {
		t.Errorf("snapshot %q != board %q", sgot, got)
	}
}

func TestEditUnderLockTransformErrorAborts(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "b.md")
	if err := os.WriteFile(p, []byte("orig\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := EditUnderLock(p, "", func(string) (string, error) {
		return "mutated\n", os.ErrInvalid
	})
	if err == nil {
		t.Fatal("expected error from transform")
	}
	if got, _ := os.ReadFile(p); string(got) != "orig\n" {
		t.Errorf("board changed on aborted transform: %q", got)
	}
}
