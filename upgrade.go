package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/nitsanavni/session-notes/internal/update"
)

// runUpgrade updates the running session-notes binary in place. It prefers a
// git rebuild when invoked from the source checkout, otherwise downloads the
// latest prebuilt release. It returns a process exit code.
func runUpgrade() int {
	// Step 1: locate the real on-disk binary (resolving symlinks so we replace
	// the actual file, not a ~/.local/bin symlink into it).
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot locate the running binary:", err)
		return 1
	}
	target, err := filepath.EvalSymlinks(exe)
	if err != nil {
		target = exe
	}
	// `go run` builds a throwaway binary under the temp dir; upgrading it is
	// meaningless (it vanishes on exit).
	if underTempDir(target) {
		fmt.Fprintln(os.Stderr, "refusing to upgrade a go-run temp binary; use install.sh")
		return 1
	}

	// Step 2: source-checkout rebuild, when we're inside the repo and a Go
	// toolchain is available.
	if cwd, err := os.Getwd(); err == nil {
		if repo, ok := findSourceCheckout(cwd); ok {
			if goBin, ok := findGo(); ok {
				return upgradeFromSource(repo, goBin, target)
			}
			// No toolchain: fall through to the download path.
		}
	}

	// Step 3: download the prebuilt release.
	return upgradeFromDownload(target)
}

// underTempDir reports whether path lives under the OS temp directory (a
// `go run` build artifact).
func underTempDir(path string) bool {
	tmp, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		tmp = os.TempDir()
	}
	rel, err := filepath.Rel(tmp, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

// findSourceCheckout walks up from start looking for a directory that is a
// session-notes source checkout: a go.mod whose first line declares the module
// and a sibling .git (dir or worktree file).
func findSourceCheckout(start string) (string, bool) {
	dir := start
	for {
		if firstLine(filepath.Join(dir, "go.mod")) == "module github.com/nitsanavni/session-notes" {
			if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
				return dir, true
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// firstLine returns the first line of a file, trimmed, or "" on any error.
func firstLine(path string) string {
	f, err := os.Open(path) // #nosec G304 -- walking known dirs for go.mod
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	if sc.Scan() {
		return strings.TrimSpace(sc.Text())
	}
	return ""
}

// findGo locates a Go toolchain the same way install.sh does: the pinned
// ~/.local/go/bin/go first, then whatever is on PATH.
func findGo() (string, bool) {
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, ".local", "go", "bin", "go")
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, true
		}
	}
	if p, err := exec.LookPath("go"); err == nil {
		return p, true
	}
	return "", false
}

// upgradeFromSource pulls the checkout fast-forward and rebuilds the binary in
// place, stamping the version exactly as install.sh does. It does NOT delegate
// to install.sh, which would also merge hooks into ./.claude/settings.json.
func upgradeFromSource(repo, goBin, target string) int {
	pull := exec.Command("git", "-C", repo, "pull", "--ff-only") // #nosec G204 -- fixed args
	out, err := pull.CombinedOutput()
	if len(out) > 0 {
		fmt.Print(string(out))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "git pull failed:", err)
		return 1
	}

	describe := gitDescribe(repo)
	build := exec.Command(goBin, "build", "-ldflags", "-X main.version="+describe, "-o", target, ".") // #nosec G204 -- fixed toolchain + repo dir
	build.Dir = repo
	build.Env = append(os.Environ(), "PATH="+filepath.Dir(goBin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	if out, err := build.CombinedOutput(); err != nil {
		if len(out) > 0 {
			fmt.Fprint(os.Stderr, string(out))
		}
		fmt.Fprintln(os.Stderr, "go build failed:", err)
		return 1
	}

	// Step 4: clear the hint and report.
	update.WriteCacheTag(describe)
	fmt.Printf("rebuilt session-notes at %s (%s)\n", target, describe)
	return 0
}

// gitDescribe returns `git describe --tags --always --dirty` for repo, or
// "unknown" on error (mirrors install.sh).
func gitDescribe(repo string) string {
	cmd := exec.Command("git", "-C", repo, "describe", "--tags", "--always", "--dirty") // #nosec G204 -- fixed args
	out, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

// upgradeFromDownload fetches the latest prebuilt release for this platform via
// the `gh` CLI (which already handles auth, private-repo access, and the signed
// asset redirect), extracts it beside the target, sanity-checks it, and
// atomically swaps it in.
func upgradeFromDownload(target string) int {
	goos, goarch := runtime.GOOS, runtime.GOARCH
	if goos != "linux" && goos != "darwin" {
		fmt.Fprintln(os.Stderr, "unsupported platform:", goos+"/"+goarch)
		return 1
	}
	if goarch != "amd64" && goarch != "arm64" {
		fmt.Fprintln(os.Stderr, "unsupported platform:", goos+"/"+goarch)
		return 1
	}
	asset := "session-notes_" + goos + "_" + goarch + ".tar.gz"

	gh, err := exec.LookPath("gh")
	if err != nil {
		fmt.Fprintln(os.Stderr, "gh CLI not found: cannot download a release from the private repo. "+
			"Install gh + `gh auth login`, or upgrade from a source checkout.")
		return 1
	}

	// Download into a temp dir under target's directory so the eventual Rename is
	// same-fs (a cross-device rename would fail).
	dlDir, err := os.MkdirTemp(filepath.Dir(target), ".session-notes-dl-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot create download dir:", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(dlDir) }()

	args := []string{"release", "download"}
	// Pin the tag we checked against so the check and the upgrade stay
	// consistent; when unknown, gh downloads the latest release.
	if tag := update.LatestTag(); tag != "" {
		args = append(args, tag)
	}
	args = append(args, "--repo", "nitsanavni/session-notes", "--pattern", asset, "--dir", dlDir, "--clobber")

	fmt.Println("downloading", asset, "via gh")
	dl := exec.Command(gh, args...) // #nosec G204 -- fixed subcommand + literal flags
	if out, err := dl.CombinedOutput(); err != nil {
		if len(out) > 0 {
			fmt.Fprint(os.Stderr, string(out))
		}
		fmt.Fprintln(os.Stderr, "gh release download failed:", err)
		return 1
	}

	archive, err := os.Open(filepath.Join(dlDir, asset)) // #nosec G304 -- asset name is a fixed platform string
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot open downloaded asset:", err)
		return 1
	}
	defer func() { _ = archive.Close() }()

	// Extract into the SAME directory as target so the final Rename is same-fs
	// (a cross-device rename would fail) and never overwrites a running inode.
	tmp := filepath.Join(filepath.Dir(target), fmt.Sprintf(".session-notes.new-%d", os.Getpid()))
	if err := extractSessionNotes(archive, tmp); err != nil {
		_ = os.Remove(tmp)
		fmt.Fprintln(os.Stderr, "extract failed:", err)
		return 1
	}

	// Sanity: the freshly downloaded binary must run. Capture its version for
	// the summary and cache.
	newVer, err := binaryVersion(tmp)
	if err != nil {
		_ = os.Remove(tmp)
		fmt.Fprintln(os.Stderr, "downloaded binary failed its sanity check:", err)
		return 1
	}

	old := versionString()
	// Rename replaces the directory entry; the running process keeps its own
	// inode. An in-place write would fail with ETXTBSY on Linux.
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		fmt.Fprintf(os.Stderr, "cannot replace %s: %v\n", target, err)
		return 1
	}

	// Step 4: clear the hint and report.
	update.WriteCacheTag(newVer)
	fmt.Printf("upgraded session-notes %s -> %s (%s)\n", old, newVer, target)
	return 0
}

// extractSessionNotes streams the gzip+tar archive from r and writes the
// `session-notes` entry to dest with mode 0755.
func extractSessionNotes(r io.Reader, dest string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("session-notes entry not found in archive")
		}
		if err != nil {
			return err
		}
		if filepath.Base(hdr.Name) != "session-notes" {
			continue
		}
		out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755) // #nosec G304 -- dest is a temp file we control
		if err != nil {
			return err
		}
		// Cap the copy to guard against a decompression bomb (binaries are a
		// few tens of MB).
		if _, err := io.CopyN(out, tr, 512<<20); err != nil && err != io.EOF {
			_ = out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
		return os.Chmod(dest, 0o755)
	}
}

// binaryVersion runs `<path> --version` (with a short timeout) and returns the
// version token from its "session-notes <version>" stdout line. A non-zero exit
// or unparseable output is an error.
func binaryVersion(path string) (string, error) {
	cmd := exec.Command(path, "--version") // #nosec G204 -- our just-extracted binary
	// Don't let the child's own update check reach out; keep the sanity check
	// hermetic and fast.
	cmd.Env = append(os.Environ(), "SESSION_NOTES_NO_UPDATE_CHECK=1")
	done := make(chan struct{})
	var out []byte
	var runErr error
	go func() {
		out, runErr = cmd.Output()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		return "", fmt.Errorf("--version timed out")
	}
	if runErr != nil {
		return "", runErr
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) >= 2 {
		return fields[1], nil
	}
	return "", fmt.Errorf("could not parse version from %q", string(out))
}
