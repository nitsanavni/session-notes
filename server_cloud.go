package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/nitsanavni/session-notes/internal/cloud"
)

// runServer implements `session-notes server`: the M3 cloud-mode server. It
// serves a superset of the file-mode web API backed by a SQLite database so any
// Claude session anywhere can attach to a board (or subtree) with the companion
// CLI. Subcommand `server token create --name <n>` mints a bearer token.
func runServer(args []string) int {
	if len(args) >= 1 && args[0] == "token" {
		return runServerToken(args[1:])
	}

	addr := "127.0.0.1:7099"
	db := defaultDBPath()
	insecure := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		takeVal := func() (string, bool) {
			if i+1 < len(args) {
				i++
				return args[i], true
			}
			return "", false
		}
		switch a {
		case "-h", "--help":
			fmt.Println("usage: session-notes server [--addr <host:port>] [--db <path>] [--insecure]")
			fmt.Println("       session-notes server token create --name <name> [--db <path>]")
			return 0
		case "--addr":
			if v, ok := takeVal(); ok {
				addr = v
			} else {
				return serverErr("--addr needs host:port")
			}
		case "--db":
			if v, ok := takeVal(); ok {
				db = v
			} else {
				return serverErr("--db needs a path")
			}
		case "--insecure":
			insecure = true
		default:
			return serverErr("unknown argument: " + a)
		}
	}

	store, err := cloud.Open(db)
	if err != nil {
		return serverErr(err.Error())
	}
	defer store.Close()

	if !insecure && !store.HasTokens() {
		return serverErr("no tokens registered; run `session-notes server token create --name <n>` or pass --insecure")
	}

	srv := cloud.NewServer(store, insecure)
	fmt.Fprintf(os.Stderr, "session-notes server: %s (db %s, insecure=%v)\n", addr, db, insecure)
	if err := http.ListenAndServe(addr, srv.Handler()); err != nil {
		return serverErr(err.Error())
	}
	return 0
}

func runServerToken(args []string) int {
	if len(args) < 1 || args[0] != "create" {
		return serverErr("usage: session-notes server token create --name <name> [--db <path>]")
	}
	name := ""
	db := defaultDBPath()
	rest := args[1:]
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case "--name":
			if i+1 < len(rest) {
				i++
				name = rest[i]
			}
		case "--db":
			if i+1 < len(rest) {
				i++
				db = rest[i]
			}
		}
	}
	if name == "" {
		return serverErr("token create needs --name <name>")
	}
	store, err := cloud.Open(db)
	if err != nil {
		return serverErr(err.Error())
	}
	defer store.Close()
	token, err := store.CreateToken(name)
	if err != nil {
		return serverErr(err.Error())
	}
	// Printed once — only the hash is stored.
	fmt.Println(token)
	return 0
}

func defaultDBPath() string {
	dir := os.Getenv("SESSION_NOTES_CONFIG_DIR")
	if dir == "" {
		if base, err := os.UserConfigDir(); err == nil {
			dir = filepath.Join(base, "session-notes")
		} else {
			dir = "."
		}
	}
	_ = os.MkdirAll(dir, 0o700)
	return filepath.Join(dir, "server.db")
}

func serverErr(msg string) int {
	fmt.Fprintln(os.Stderr, "session-notes server:", msg)
	return 1
}
