package main

import (
	"fmt"
	"net"
	"net/http"
	"os"

	"github.com/nitsanavni/session-notes/internal/web"
)

// runServe implements `session-notes serve [--addr host:port] [--board <path>]
// [--token <t>] [--insecure]`: the web UI for people not running the tmux TUI.
// Serves the dashboard at "/" and each board at /b/<session-id>, editing
// through the same locked write path as everything else.
//
// Access control: loopback binds need nothing. Binding wider requires a token
// (--token or $SESSION_NOTES_TOKEN; every request must then present it — see
// web.SetToken) or an explicit --insecure. There is no TLS — for anything
// beyond a trusted LAN, front it with a tunnel (ssh -L), a tailnet, or a
// TLS-terminating proxy; the token still protects against other tenants of
// the same network.
func runServe(args []string) int {
	addr := "127.0.0.1:7080"
	token := os.Getenv("SESSION_NOTES_TOKEN")
	insecure := false
	var boardPath string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--addr":
			if i+1 >= len(args) {
				return serveErr("--addr needs host:port")
			}
			i++
			addr = args[i]
		case "--board":
			if i+1 >= len(args) {
				return serveErr("--board needs a path")
			}
			i++
			boardPath = args[i]
		case "--token":
			if i+1 >= len(args) {
				return serveErr("--token needs a value")
			}
			i++
			token = args[i]
		case "--insecure":
			insecure = true
		default:
			return serveErr(fmt.Sprintf("unknown argument: %s", args[i]))
		}
	}

	srv := web.New()
	if boardPath != "" {
		id, err := srv.Register(boardPath)
		if err != nil {
			return serveErr(err.Error())
		}
		srv.SetHome(id)
	}
	if token != "" {
		srv.SetToken(token)
	}

	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return serveErr(fmt.Sprintf("bad --addr %q: %v", addr, err))
	}
	if !isLoopback(host) && token == "" {
		if !insecure {
			return serveErr("refusing to bind " + addr + " without auth: anyone who can reach it could read and edit your boards.\n  Pass --token <secret> (or set SESSION_NOTES_TOKEN), or --insecure to serve open anyway")
		}
		fmt.Fprintln(os.Stderr, "session-notes: warning: serving on a non-loopback address with no auth (--insecure) — anyone who can reach it can read and edit your boards")
	}

	if token != "" {
		fmt.Printf("session-notes web UI: http://%s/?token=%s\n", displayAddr(host, addr), token)
		fmt.Println("  (the tokened URL signs the browser in via a cookie; API callers send 'Authorization: Bearer <token>')")
	} else {
		fmt.Printf("session-notes web UI: http://%s/\n", displayAddr(host, addr))
	}
	if err := http.ListenAndServe(addr, srv.Handler()); err != nil {
		return serveErr(err.Error())
	}
	return 0
}

// isLoopback reports whether host names the local machine only. An empty host
// ("":port") binds every interface, so it is NOT loopback.
func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// displayAddr makes the printed URL clickable when binding all interfaces.
func displayAddr(host, addr string) string {
	if host != "" {
		return addr
	}
	_, port, _ := net.SplitHostPort(addr)
	return "localhost:" + port
}

func serveErr(msg string) int {
	fmt.Fprintln(os.Stderr, "session-notes:", msg)
	return 2
}
