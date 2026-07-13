package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nitsanavni/session-notes/internal/cloud"
)

// runLink implements `session-notes link <ref>` / `link --show` / `unlink`
// (railway-style): pin a default board ref for the current directory so
// `edit`/`watch` with no --board resolve to it. <ref> is anything --board
// accepts — a local path or an http(s)://host/b/<board>[#<node>] URL. Local
// refs are stored as absolute paths so the link survives a later cwd change; a
// remote URL is stored verbatim. Lookups walk up parent dirs (git-style).
func runLink(unlink bool, args []string) int {
	show := false
	var ref string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			linkUsage()
			return 0
		case "--show":
			show = true
		default:
			if strings.HasPrefix(args[i], "-") {
				return linkErr("unknown flag: " + args[i])
			}
			if ref != "" {
				return linkErr("link takes a single <ref>")
			}
			ref = args[i]
		}
	}

	cwd, err := os.Getwd()
	if err != nil {
		return linkErr(err.Error())
	}

	if unlink {
		if ref != "" || show {
			return linkErr("usage: session-notes unlink")
		}
		removed, err := cloud.RemoveLink(cwd)
		if err != nil {
			return linkErr(err.Error())
		}
		if !removed {
			fmt.Fprintf(os.Stderr, "no link for %s\n", cwd)
			return 0
		}
		fmt.Fprintf(os.Stderr, "unlinked %s\n", cwd)
		return 0
	}

	if show || ref == "" {
		linked, source, ok := cloud.LinkFor(cwd)
		if !ok {
			fmt.Fprintf(os.Stderr, "no link for %s\n", cwd)
			return 1
		}
		where := source
		if source != cwd {
			where = source + " (parent)"
		}
		fmt.Printf("%s\n", linked)
		fmt.Fprintf(os.Stderr, "linked from %s\n", where)
		return 0
	}

	// Store: remote URLs verbatim (validated), local paths as absolute (with any
	// #<node> fragment preserved).
	stored := ref
	if cloud.IsRemote(ref) {
		if _, perr := cloud.ParseRef(ref); perr != nil {
			return linkErr(perr.Error())
		}
	} else {
		path, frag := splitFragment(ref)
		abs, aerr := filepath.Abs(path)
		if aerr != nil {
			return linkErr(aerr.Error())
		}
		if _, serr := os.Stat(abs); serr != nil {
			fmt.Fprintf(os.Stderr, "session-notes: warning: board not found yet: %s\n", abs)
		}
		stored = abs
		if frag != "" {
			stored += "#" + frag
		}
	}
	if err := cloud.SaveLink(cwd, stored); err != nil {
		return linkErr(err.Error())
	}
	fmt.Fprintf(os.Stderr, "linked %s -> %s\n", cwd, stored)
	return 0
}

// splitFragment splits a local ref into its path and trailing #<node> fragment.
func splitFragment(ref string) (path, frag string) {
	if i := strings.LastIndex(ref, "#"); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	return ref, ""
}

// linkedBoard resolves the per-directory link for the current cwd when neither
// --board nor --session was given. It returns the effective board ref (local
// path or remote URL) and, for a *local* linked ref, a #<node> fragment that
// becomes the default --root (edit) / --node (watch). Remote refs keep their
// fragment in the string for cloud.ParseRef to extract, mirroring --board.
func linkedBoard(boardPath, session string) (ref, node string) {
	if boardPath != "" || session != "" {
		return boardPath, ""
	}
	cwd, err := os.Getwd()
	if err != nil {
		return boardPath, ""
	}
	linked, _, ok := cloud.LinkFor(cwd)
	if !ok {
		return boardPath, ""
	}
	if cloud.IsRemote(linked) {
		return linked, ""
	}
	return splitFragment(linked)
}

func linkUsage() {
	fmt.Print(`session-notes link — pin a default board for this directory

Usage:
  session-notes link <ref>     pin <ref> (a --board path or http(s)://host/b/<board>[#<node>])
                               as the default board for the current directory
  session-notes link --show    print the effective linked ref and where it came from
  session-notes unlink         remove the current directory's link

After linking, every command that takes --board (edit subcommands, watch) uses
the linked ref when --board is omitted; an explicit --board always wins. A
#<node> in the linked ref becomes the default --root (edit) / --node (watch).
Lookups walk up parent directories, git-style.
`)
}

func linkErr(msg string) int {
	fmt.Fprintln(os.Stderr, "session-notes link:", msg)
	return 2
}
