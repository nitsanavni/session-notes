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
		// remote pull --all <server-url> <local-dir>
		if len(args) >= 2 && args[1] == "--all" {
			if len(args) != 4 {
				return remoteErr("usage: session-notes remote pull --all <server-url> <local-dir>")
			}
			return remotePullAll(args[2], args[3])
		}
		if len(args) != 3 {
			return remoteErr("usage: session-notes remote pull <server-url>/b/<board> <local-board>")
		}
		return remotePull(args[1], args[2])
	case "grant":
		return remoteGrant(args[1:])
	case "grants":
		return remoteGrants(args[1:])
	case "revoke":
		return remoteRevoke(args[1:])
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

// remotePullAll exports every board the caller can see to <dir>/<id>.md — the
// remote counterpart of `server export`, wire-ready for an off-box backup cron.
func remotePullAll(server, dir string) int {
	server = strings.TrimRight(server, "/")
	host := hostOf(server)
	token := cloud.TokenFor(host)
	cards, err := cloud.ListBoards(server, token)
	if err != nil {
		return remoteErr(err.Error())
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return remoteErr(err.Error())
	}
	n := 0
	for _, c := range cards {
		tree := cloud.NewRemoteTree(server, c.ID, token)
		content, _, err := tree.Raw()
		if err != nil {
			return remoteErr(err.Error())
		}
		if err := os.WriteFile(dir+"/"+c.ID+".md", []byte(content), 0o644); err != nil {
			return remoteErr(err.Error())
		}
		n++
	}
	fmt.Fprintf(os.Stderr, "pulled %d board(s) to %s\n", n, dir)
	return 0
}

// remoteGrant implements `session-notes remote grant <url>/b/<board>[#<node>]
// --token-name <n> | --new-token <n> [--subject <who>] [--perm read|write]`.
// With --new-token it mints a token + grant in one step and prints an attach
// line the user can paste to a sub-agent: URL (with #node) + token, scoped to
// the granted subtree. Admin token required.
func remoteGrant(args []string) int {
	if len(args) < 1 {
		return remoteErr("usage: session-notes remote grant <url>/b/<board>[#<node>] [--new-token <n> | --token-name <n> | --subject <who>] [--perm read|write]")
	}
	target := args[0]
	var tokenName, newToken, subject, perm string
	perm = "read"
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--token-name":
			i++
			if i < len(args) {
				tokenName = args[i]
			}
		case "--new-token":
			i++
			if i < len(args) {
				newToken = args[i]
			}
		case "--subject":
			i++
			if i < len(args) {
				subject = args[i]
			}
		case "--perm":
			i++
			if i < len(args) {
				perm = args[i]
			}
		default:
			return remoteErr("unknown flag: " + args[i])
		}
	}
	ref, err := cloud.ParseRef(target)
	if err != nil {
		return remoteErr(err.Error())
	}
	res, err := cloud.AddGrant(ref.Server, cloud.TokenFor(ref.Host), ref.Board, subject, tokenName, newToken, ref.Node, perm)
	if err != nil {
		return remoteErr(err.Error())
	}
	boardURL := fmt.Sprintf("%s/b/%s", ref.Server, ref.Board)
	if ref.Node != "" {
		boardURL += "#" + ref.Node
	}
	if res.Token != "" {
		// The headline carve-out handoff: one line to paste into a sub-agent.
		fmt.Printf("granted %s (%s) to %q\n", scopeLabel(ref.Node), res.Perm, res.Subject)
		fmt.Printf("attach: session-notes login %s --token %s && session-notes edit --board '%s'\n", ref.Server, res.Token, boardURL)
		fmt.Printf("token: %s\n", res.Token)
	} else {
		fmt.Printf("granted %s (%s) to %q on %s\n", scopeLabel(ref.Node), res.Perm, res.Subject, boardURL)
	}
	return 0
}

func scopeLabel(node string) string {
	if node == "" {
		return "whole board"
	}
	return "subtree #" + node
}

// remoteGrants lists a board's grants: `remote grants <url>/b/<board>`.
func remoteGrants(args []string) int {
	if len(args) != 1 {
		return remoteErr("usage: session-notes remote grants <url>/b/<board>")
	}
	ref, err := cloud.ParseRef(args[0])
	if err != nil {
		return remoteErr(err.Error())
	}
	grants, err := cloud.ListGrants(ref.Server, cloud.TokenFor(ref.Host), ref.Board)
	if err != nil {
		return remoteErr(err.Error())
	}
	if len(grants) == 0 {
		fmt.Println("(no grants)")
		return 0
	}
	for _, g := range grants {
		fmt.Printf("%s\t%s\t%s\n", g.Subject, g.Perm, scopeLabel(g.Root))
	}
	return 0
}

// remoteRevoke removes a grant: `remote revoke <url>/b/<board> --subject <who>`
// (or --token-name <n>).
func remoteRevoke(args []string) int {
	if len(args) < 1 {
		return remoteErr("usage: session-notes remote revoke <url>/b/<board> --subject <who> | --token-name <n>")
	}
	ref, err := cloud.ParseRef(args[0])
	if err != nil {
		return remoteErr(err.Error())
	}
	var subject, tokenName string
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--subject":
			i++
			if i < len(args) {
				subject = args[i]
			}
		case "--token-name":
			i++
			if i < len(args) {
				tokenName = args[i]
			}
		default:
			return remoteErr("unknown flag: " + args[i])
		}
	}
	if subject == "" && tokenName == "" {
		return remoteErr("revoke needs --subject <who> or --token-name <n>")
	}
	if err := cloud.RevokeGrant(ref.Server, cloud.TokenFor(ref.Host), ref.Board, subject, tokenName); err != nil {
		return remoteErr(err.Error())
	}
	fmt.Println("revoked")
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
