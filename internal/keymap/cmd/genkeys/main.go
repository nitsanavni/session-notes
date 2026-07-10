// Command genkeys rewrites the generated keybindings table in README.md from the
// single source of truth (internal/keymap). Run via `go generate ./...`.
package main

import (
	"fmt"
	"os"

	"github.com/nitsanavni/session-notes/internal/keymap"
)

func main() {
	const path = "README.md"
	src, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "genkeys:", err)
		os.Exit(1)
	}
	out, err := keymap.InjectMarkdown(string(src))
	if err != nil {
		fmt.Fprintln(os.Stderr, "genkeys:", err)
		os.Exit(1)
	}
	if out == string(src) {
		return
	}
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "genkeys:", err)
		os.Exit(1)
	}
	fmt.Println("genkeys: README.md updated")
}
