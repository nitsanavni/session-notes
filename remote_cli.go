package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/nitsanavni/session-notes/internal/cloud"
)

// runLogin implements `session-notes login <server-url>`: prompt for (or accept
// via --token / stdin) a bearer token and store it keyed by host under the
// config dir (0600), so remote edit/watch pick it up automatically.
func runLogin(args []string) int {
	var server, token string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			fmt.Println("usage: session-notes login <server-url> [--token <t>]")
			return 0
		case "--token":
			if i+1 < len(args) {
				i++
				token = args[i]
			}
		default:
			if server == "" {
				server = args[i]
			}
		}
	}
	if server == "" {
		return loginErr("usage: session-notes login <server-url> [--token <t>]")
	}
	ref, err := cloud.ParseRef(strings.TrimRight(server, "/") + "/b/_")
	host := ""
	if err == nil {
		host = ref.Host
	} else {
		// Accept a bare scheme://host with no /b/ path.
		host = strings.TrimPrefix(strings.TrimPrefix(server, "https://"), "http://")
		host = strings.TrimRight(host, "/")
	}
	if token == "" {
		if v := os.Getenv("SESSION_NOTES_TOKEN"); v != "" {
			token = v
		} else {
			fmt.Fprintf(os.Stderr, "token for %s: ", host)
			sc := bufio.NewScanner(os.Stdin)
			if sc.Scan() {
				token = strings.TrimSpace(sc.Text())
			}
		}
	}
	if token == "" {
		return loginErr("no token provided")
	}
	if err := cloud.SaveToken(host, token); err != nil {
		return loginErr(err.Error())
	}
	fmt.Fprintf(os.Stderr, "saved token for %s\n", host)
	return 0
}

// runRemote implements `session-notes remote <new|push|pull>`: board
// import/export against a cloud server.
func runRemote(args []string) int {
	if len(args) == 0 {
		return remoteErr("usage: session-notes remote <new|push|pull> …")
	}
	switch args[0] {
	case "new":
		// remote new <server-url> <name>
		if len(args) != 3 {
			return remoteErr("usage: session-notes remote new <server-url> <name>")
		}
		server, name := strings.TrimRight(args[1], "/"), args[2]
		host := hostOf(server)
		if err := cloud.CreateBoard(server, cloud.TokenFor(host), name, name, ""); err != nil {
			return remoteErr(err.Error())
		}
		fmt.Printf("%s/b/%s\n", server, name)
		return 0
	case "push":
		// remote push <local-board> <server-url> [<name>]
		if len(args) < 3 || len(args) > 4 {
			return remoteErr("usage: session-notes remote push <local-board> <server-url> [<name>]")
		}
		local, server := args[1], strings.TrimRight(args[2], "/")
		name := ""
		if len(args) == 4 {
			name = args[3]
		}
		return remotePush(local, server, name)
	case "pull":
		// remote pull <server-url>/b/<board> <local-board>
		if len(args) != 3 {
			return remoteErr("usage: session-notes remote pull <server-url>/b/<board> <local-board>")
		}
		return remotePull(args[1], args[2])
	default:
		return remoteErr("unknown remote subcommand: " + args[0])
	}
}

func remotePush(local, server, name string) int {
	data, err := os.ReadFile(local)
	if err != nil {
		return remoteErr(err.Error())
	}
	if name == "" {
		base := local
		if i := strings.LastIndexByte(base, '/'); i >= 0 {
			base = base[i+1:]
		}
		name = strings.TrimSuffix(base, ".md")
	}
	host := hostOf(server)
	if _, err := cloud.PushContent(server, cloud.TokenFor(host), name, string(data), "push"); err != nil {
		return remoteErr(err.Error())
	}
	fmt.Printf("%s/b/%s\n", server, name)
	return 0
}

func remotePull(remoteURL, local string) int {
	ref, err := cloud.ParseRef(remoteURL)
	if err != nil {
		return remoteErr(err.Error())
	}
	tree := cloud.NewRemoteTree(ref.Server, ref.Board, cloud.TokenFor(ref.Host))
	content, _, err := tree.Raw()
	if err != nil {
		return remoteErr(err.Error())
	}
	if err := os.WriteFile(local, []byte(content), 0o644); err != nil {
		return remoteErr(err.Error())
	}
	fmt.Println(local)
	return 0
}

func hostOf(server string) string {
	h := strings.TrimPrefix(strings.TrimPrefix(server, "https://"), "http://")
	return strings.TrimRight(h, "/")
}

func loginErr(msg string) int {
	fmt.Fprintln(os.Stderr, "session-notes login:", msg)
	return 2
}

func remoteErr(msg string) int {
	fmt.Fprintln(os.Stderr, "session-notes remote:", msg)
	return 2
}
