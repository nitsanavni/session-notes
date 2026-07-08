package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

// makeTarGz builds an in-memory gzip'd tar containing the given name->content
// entries, mirroring the release archive layout.
func makeTarGz(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractSessionNotes(t *testing.T) {
	archive := makeTarGz(t, map[string]string{
		"README.md":     "not me",
		"session-notes": "#!/bin/sh\necho hi\n",
	})
	dest := filepath.Join(t.TempDir(), ".session-notes.new")
	if err := extractSessionNotes(bytes.NewReader(archive), dest); err != nil {
		t.Fatalf("extractSessionNotes: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "#!/bin/sh\necho hi\n" {
		t.Fatalf("extracted content = %q", string(got))
	}
	fi, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("extracted file not executable: %v", fi.Mode())
	}
}

func TestExtractSessionNotesMissingEntry(t *testing.T) {
	archive := makeTarGz(t, map[string]string{"README.md": "only docs"})
	dest := filepath.Join(t.TempDir(), ".session-notes.new")
	if err := extractSessionNotes(bytes.NewReader(archive), dest); err == nil {
		t.Fatal("expected error when session-notes entry is absent")
	}
}

func TestFindSourceCheckout(t *testing.T) {
	root := t.TempDir()
	// A valid checkout: go.mod with the module line + a .git marker.
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(repo, "sub", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo, "go.mod"), "module github.com/nitsanavni/session-notes\n\ngo 1.26\n")
	writeFile(t, filepath.Join(repo, ".git"), "gitdir: /somewhere\n") // worktree-style .git file

	// From a nested subdir, the walk finds the repo root.
	got, ok := findSourceCheckout(filepath.Join(repo, "sub", "deep"))
	if !ok || got != repo {
		t.Fatalf("findSourceCheckout(nested) = %q,%v; want %q,true", got, ok, repo)
	}

	// A go.mod for a different module is not our checkout.
	other := filepath.Join(root, "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(other, "go.mod"), "module example.com/other\n")
	writeFile(t, filepath.Join(other, ".git"), "")
	if _, ok := findSourceCheckout(other); ok {
		t.Fatal("findSourceCheckout matched a foreign module")
	}

	// Our module line but no .git sibling: not a checkout.
	nogit := filepath.Join(root, "nogit")
	if err := os.MkdirAll(nogit, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(nogit, "go.mod"), "module github.com/nitsanavni/session-notes\n")
	if _, ok := findSourceCheckout(nogit); ok {
		t.Fatal("findSourceCheckout matched a module without .git")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
