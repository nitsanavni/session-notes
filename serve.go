package main

import (
	"fmt"
	"net"
	"net/http"
	"os"

	"github.com/nitsanavni/session-notes/internal/web"
)

// runServe implements `session-notes serve [--addr host:port] [--board <path>]`:
// the web UI for people not running the tmux TUI. Serves the dashboard at "/"
// and each board at /b/<session-id>, editing through the same locked write path
// as everything else. Binds loopback-only by default — the server has no auth,
// so exposing it wider is an explicit, warned-about choice.
func runServe(args []string) int {
	addr := "127.0.0.1:7080"
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

	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return serveErr(fmt.Sprintf("bad --addr %q: %v", addr, err))
	}
	if !isLoopback(host) {
		fmt.Fprintln(os.Stderr, "session-notes: warning: serving on a non-loopback address with no auth — anyone who can reach it can read and edit your boards")
	}

	fmt.Printf("session-notes web UI: http://%s/\n", displayAddr(host, addr))
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
