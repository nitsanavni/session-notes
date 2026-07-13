package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/nitsanavni/session-notes/internal/board"
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
	if len(args) >= 1 && args[0] == "export" {
		return runServerExport(args[1:])
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
			fmt.Println("       session-notes server token list [--db <path>]")
			fmt.Println("       session-notes server token revoke <name> [--db <path>]")
			fmt.Println("       session-notes server export --dir <dir> [--db <path>]")
			fmt.Println("  token create mints an all-boards ADMIN bootstrap token (printed once).")
			fmt.Println("  Scoped, non-admin tokens are minted via `remote grant --new-token`.")
			fmt.Println("  The server shuts down gracefully on SIGTERM/SIGINT (drains SSE, closes DB).")
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
	httpSrv := &http.Server{Addr: addr, Handler: srv.Handler()}

	// Graceful shutdown: on SIGTERM/SIGINT stop accepting, let in-flight requests
	// (including open SSE streams, which end when their request context is
	// cancelled) drain, then close the DB. `store.Close()` runs via the deferred
	// call above once this function returns.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	errc := make(chan error, 1)
	go func() { errc <- httpSrv.ListenAndServe() }()

	fmt.Fprintf(os.Stderr, "session-notes server: %s (db %s, insecure=%v)\n", addr, db, insecure)
	select {
	case err := <-errc:
		if err != nil && err != http.ErrServerClosed {
			return serverErr(err.Error())
		}
		return 0
	case <-ctx.Done():
		fmt.Fprintln(os.Stderr, "session-notes server: shutting down…")
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutCtx); err != nil {
			return serverErr("shutdown: " + err.Error())
		}
		return 0
	}
}

// runServerExport implements `session-notes server export --dir <dir> [--db
// <path>]`: writes every board's markdown blob to `<dir>/<id>.md`. This is the
// format-durability layer — the boards land as plain, re-parseable markdown that
// outlives the SQLite file, wire-ready for a cron (see deploy/README.md).
func runServerExport(args []string) int {
	dir := ""
	db := defaultDBPath()
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dir":
			if i+1 < len(args) {
				i++
				dir = args[i]
			}
		case "--db":
			if i+1 < len(args) {
				i++
				db = args[i]
			}
		case "-h", "--help":
			fmt.Println("usage: session-notes server export --dir <dir> [--db <path>]")
			return 0
		default:
			return serverErr("unknown argument: " + args[i])
		}
	}
	if dir == "" {
		return serverErr("export needs --dir <dir>")
	}
	store, err := cloud.Open(db)
	if err != nil {
		return serverErr(err.Error())
	}
	defer store.Close()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return serverErr(err.Error())
	}
	ids, err := store.ListBoards()
	if err != nil {
		return serverErr(err.Error())
	}
	n := 0
	for _, id := range ids {
		content, _, err := store.Get(id)
		if err != nil {
			return serverErr(err.Error())
		}
		// Round-trip through the parser so the export is exactly what a reader
		// would parse back (stable ids, canonical render).
		content = board.Parse(content).Render()
		path := filepath.Join(dir, id+".md")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return serverErr(err.Error())
		}
		n++
	}
	fmt.Fprintf(os.Stderr, "exported %d board(s) to %s\n", n, dir)
	return 0
}

func runServerToken(args []string) int {
	if len(args) < 1 {
		return serverErr("usage: session-notes server token <create|list|revoke> …")
	}
	switch args[0] {
	case "create":
		return runServerTokenCreate(args[1:])
	case "list":
		return runServerTokenList(args[1:])
	case "revoke":
		return runServerTokenRevoke(args[1:])
	default:
		return serverErr("usage: session-notes server token <create|list|revoke> …")
	}
}

func runServerTokenCreate(rest []string) int {
	name := ""
	db := defaultDBPath()
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

// runServerTokenList prints registered tokens (name, subject, admin flag, age).
// Never the secret — only its hash is stored — so this is an audit/revocation aid.
func runServerTokenList(rest []string) int {
	db := defaultDBPath()
	for i := 0; i < len(rest); i++ {
		if rest[i] == "--db" && i+1 < len(rest) {
			i++
			db = rest[i]
		}
	}
	store, err := cloud.Open(db)
	if err != nil {
		return serverErr(err.Error())
	}
	defer store.Close()
	toks, err := store.ListTokens()
	if err != nil {
		return serverErr(err.Error())
	}
	if len(toks) == 0 {
		fmt.Println("(no tokens)")
		return 0
	}
	fmt.Printf("%-20s %-20s %-6s %s\n", "NAME", "SUBJECT", "ADMIN", "CREATED")
	for _, t := range toks {
		admin := "no"
		if t.Admin {
			admin = "yes"
		}
		fmt.Printf("%-20s %-20s %-6s %s\n", t.Name, t.Subject, admin, t.Created.Format(time.RFC3339))
	}
	return 0
}

// runServerTokenRevoke deletes every token registered under a name. The next
// request bearing a revoked secret 401s.
func runServerTokenRevoke(rest []string) int {
	db := defaultDBPath()
	name := ""
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case "--db":
			if i+1 < len(rest) {
				i++
				db = rest[i]
			}
		default:
			if name == "" {
				name = rest[i]
			}
		}
	}
	if name == "" {
		return serverErr("usage: session-notes server token revoke <name> [--db <path>]")
	}
	store, err := cloud.Open(db)
	if err != nil {
		return serverErr(err.Error())
	}
	defer store.Close()
	n, err := store.RevokeToken(name)
	if err != nil {
		return serverErr(err.Error())
	}
	if n == 0 {
		return serverErr("no token named " + name)
	}
	fmt.Fprintf(os.Stderr, "revoked %d token(s) named %q\n", n, name)
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
