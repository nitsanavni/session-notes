package cloud

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/nitsanavni/session-notes/internal/board"
)

// Ref is a parsed remote board reference: an http(s) URL of the form
// https://host[:port]/b/<board>[#<node>]. Server is the scheme://host[:port]
// prefix the API hangs off, Board the board id, Node the optional node fragment.
type Ref struct {
	Server string
	Host   string // host[:port], the token config key
	Board  string
	Node   string
}

// IsRemote reports whether a --board value routes to a remote server.
func IsRemote(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// ParseRef parses a remote board URL. The path must contain /b/<board>.
func ParseRef(raw string) (Ref, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return Ref{}, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return Ref{}, fmt.Errorf("not an http(s) board url: %s", raw)
	}
	ref := Ref{
		Server: u.Scheme + "://" + u.Host,
		Host:   u.Host,
		Node:   u.Fragment,
	}
	// Accept /b/<board> or /api/board/<board>; also a bare /<board>.
	p := strings.Trim(u.Path, "/")
	parts := strings.Split(p, "/")
	switch {
	case len(parts) >= 2 && parts[0] == "b":
		ref.Board = parts[1]
	case len(parts) >= 3 && parts[0] == "api" && parts[1] == "board":
		ref.Board = parts[2]
	case len(parts) == 1 && parts[0] != "":
		ref.Board = parts[0]
	default:
		return Ref{}, fmt.Errorf("board url must be .../b/<board>: %s", raw)
	}
	return ref, nil
}

// RemoteTree is the CLI-side board.Tree implementation talking to a cloud
// server over the HTTP+SSE protocol. It routes Get/Apply/Watch to the same
// endpoints board.html uses, so a remote board behaves like a local file.
type RemoteTree struct {
	base   string // scheme://host
	board  string
	token  string
	client *http.Client

	mu       sync.Mutex
	lastSeen int // last-seen board version, 0 = unknown (sent as base for optimistic concurrency)
}

func (t *RemoteTree) setSeen(v int) {
	t.mu.Lock()
	if v > t.lastSeen {
		t.lastSeen = v
	}
	t.mu.Unlock()
}

func (t *RemoteTree) seen() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lastSeen
}

// NewRemoteTree builds a client for a board on a server. token may be empty for
// an --insecure server.
func NewRemoteTree(server, boardID, token string) *RemoteTree {
	return &RemoteTree{
		base:   strings.TrimRight(server, "/"),
		board:  boardID,
		token:  token,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (t *RemoteTree) req(method, path string, body io.Reader) (*http.Request, error) {
	r, err := http.NewRequest(method, t.base+path, body)
	if err != nil {
		return nil, err
	}
	if t.token != "" {
		r.Header.Set("Authorization", "Bearer "+t.token)
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	return r, nil
}

// Raw fetches the board's whole markdown blob and its version.
func (t *RemoteTree) Raw() (content string, version int, err error) {
	r, err := t.req("GET", "/api/board/"+t.board+"/raw", nil)
	if err != nil {
		return "", 0, err
	}
	resp, err := t.client.Do(r)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", 0, httpError(resp)
	}
	var out struct {
		Content string `json:"content"`
		Version int    `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", 0, err
	}
	t.setSeen(out.Version)
	return out.Content, out.Version, nil
}

// Get implements board.Tree by fetching the raw board and building the node
// model locally (identical to the file backend's conversion).
func (t *RemoteTree) Get(id string, depth int) (*board.Node, error) {
	content, _, err := t.Raw()
	if err != nil {
		return nil, err
	}
	return board.Parse(content).Node(id, depth)
}

// Apply implements board.Tree by POSTing the op to the edit endpoint. It sends
// its last-seen version as the optimistic base so a racing writer can't be
// silently clobbered: on a 409 it refetches the current version and retries the
// (surgical) op once. Op.Root travels in the POST so carve-out refusal is
// enforced server-side.
func (t *RemoteTree) Apply(op board.Op) (board.OpResult, error) {
	req := editReq{
		Op:      op.Name,
		ID:      op.ID,
		Root:    op.Root,
		Section: op.Section,
		Raw:     op.Raw,
		Query:   op.Query,
		Text:    op.Text,
		Author:  op.Author,
		Status:  statusString(op.Status),
	}
	if op.Name == "urgent" {
		u := op.Urgent
		req.Urgent = &u
	}
	if op.Name == "pin" {
		p := op.Pinned
		req.Pinned = &p
	}

	res, conflict, err := t.postEdit(req, t.seen())
	if err != nil {
		return board.OpResult{}, err
	}
	// On a 409 (stale base) refetch the current version and retry the surgical
	// op — last-writer-wins without silently dropping a write. Under heavy
	// same-node contention two writers can ping-pong, so after a bounded set of
	// optimistic retries fall back to an unconditional apply (base<0): the
	// server serializes writes per board, so the op still lands exactly once.
	for attempts := 0; conflict && attempts < 8; attempts++ {
		_, v, rerr := t.Raw()
		if rerr != nil {
			return board.OpResult{}, rerr
		}
		res, conflict, err = t.postEdit(req, v)
		if err != nil {
			return board.OpResult{}, err
		}
	}
	if conflict {
		res, conflict, err = t.postEdit(req, -1)
		if err != nil {
			return board.OpResult{}, err
		}
	}
	if conflict {
		return board.OpResult{}, board.ErrOpNotFound
	}
	return res, nil
}

// postEdit POSTs one op with the given base version. conflict is true when the
// server returned 409 (stale base); on success it advances the last-seen
// version. base<0 disables the optimistic check server-side.
func (t *RemoteTree) postEdit(req editReq, base int) (res board.OpResult, conflict bool, err error) {
	b := base
	if b <= 0 {
		b = -1
	}
	req.Base = &b
	buf, _ := json.Marshal(req)
	r, err := t.req("POST", "/api/board/"+t.board+"/edit", bytes.NewReader(buf))
	if err != nil {
		return board.OpResult{}, false, err
	}
	resp, err := t.client.Do(r)
	if err != nil {
		return board.OpResult{}, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		return board.OpResult{}, true, nil
	}
	if resp.StatusCode >= 400 {
		return board.OpResult{}, false, wrapOpError(resp)
	}
	var out struct {
		Version int    `json:"version"`
		Note    string `json:"note"`
		ID      string `json:"id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	t.setSeen(out.Version)
	return board.OpResult{Note: out.Note, ID: out.ID}, false, nil
}

// Watch implements board.Tree by consuming the SSE events stream scoped to id.
// Each server "changed" event becomes a board.Event. The cancel func closes the
// stream.
func (t *RemoteTree) Watch(id string) (<-chan board.Event, func(), error) {
	path := "/api/board/" + t.board + "/events"
	if id != "" {
		path += "?node=" + url.QueryEscape(id)
	}
	r, err := t.req("GET", path, nil)
	if err != nil {
		return nil, nil, err
	}
	r.Header.Set("Accept", "text/event-stream")
	resp, err := t.client.Do(r)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, nil, httpError(resp)
	}
	ch := make(chan board.Event, 8)
	var once sync.Once
	cancel := func() { once.Do(func() { resp.Body.Close() }) }
	go func() {
		defer close(ch)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "data:") {
				select {
				case ch <- board.Event{NodeID: id, Kind: "changed"}:
				default:
				}
			}
		}
	}()
	return ch, cancel, nil
}

// statusString maps a board.Status to the wire string toOp/parseStatus expects.
func statusString(st board.Status) string {
	switch st {
	case board.StatusOpen:
		return "open"
	case board.StatusInProgress:
		return "wip"
	case board.StatusDone:
		return "done"
	case board.StatusBlocked:
		return "blocked"
	default:
		return "none"
	}
}

func httpError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
}

// wrapOpError maps a 4xx edit response to a board sentinel so callers keep exit
// codes; 400 is an invalid op (e.g. out-of-scope --root refusal).
func wrapOpError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	msg := strings.TrimSpace(string(b))
	if resp.StatusCode == http.StatusBadRequest {
		return fmt.Errorf("%w: %s", board.ErrOpInvalid, msg)
	}
	return fmt.Errorf("%s: %s", resp.Status, msg)
}

// CreateBoard asks the server to create a new board; returns nothing on success.
func CreateBoard(server, token, id, name, content string) error {
	body, _ := json.Marshal(map[string]string{"id": id, "name": name, "content": content})
	r, err := http.NewRequest("POST", strings.TrimRight(server, "/")+"/api/boards", bytes.NewReader(body))
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return httpError(resp)
	}
	return nil
}

// doJSON runs an authenticated JSON request against a server and decodes the
// response into out (may be nil). Non-2xx becomes an error.
func doJSON(method, server, token, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		buf, _ := json.Marshal(body)
		rdr = bytes.NewReader(buf)
	}
	r, err := http.NewRequest(method, strings.TrimRight(server, "/")+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return httpError(resp)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// GrantResult is the server's reply to an AddGrant; Token is set only when a new
// token was minted (--new-token), so the CLI can print an attach line.
type GrantResult struct {
	Subject string `json:"subject"`
	Perm    string `json:"perm"`
	Root    string `json:"root"`
	Token   string `json:"token"`
}

// AddGrant creates/updates a grant on a board (admin token required). subject,
// tokenName, or newToken selects the grantee; newToken mints a token in one step.
func AddGrant(server, token, boardID, subject, tokenName, newToken, root, perm string) (GrantResult, error) {
	var out GrantResult
	body := map[string]string{"subject": subject, "tokenName": tokenName, "newToken": newToken, "root": root, "perm": perm}
	err := doJSON("POST", server, token, "/api/board/"+boardID+"/grants", body, &out)
	return out, err
}

// ListGrants returns a board's grants (admin token required).
func ListGrants(server, token, boardID string) ([]Grant, error) {
	var out struct {
		Grants []Grant `json:"grants"`
	}
	err := doJSON("GET", server, token, "/api/board/"+boardID+"/grants", nil, &out)
	return out.Grants, err
}

// RevokeGrant removes a grantee's grant on a board (admin token required).
func RevokeGrant(server, token, boardID, subject, tokenName string) error {
	body := map[string]string{"subject": subject, "tokenName": tokenName}
	return doJSON("DELETE", server, token, "/api/board/"+boardID+"/grants", body, nil)
}

// BoardCard is one entry of the /api/boards list (the fields the CLI needs).
type BoardCard struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ListBoards returns the boards the token can see (used by `remote pull --all`).
func ListBoards(server, token string) ([]BoardCard, error) {
	var out struct {
		Boards []BoardCard `json:"boards"`
	}
	err := doJSON("GET", server, token, "/api/boards", nil, &out)
	return out.Boards, err
}

// PushContent uploads a whole markdown blob to a board (create-or-replace).
func PushContent(server, token, id, content, author string) (int, error) {
	body, _ := json.Marshal(map[string]string{"content": content, "author": author})
	r, err := http.NewRequest("PUT", strings.TrimRight(server, "/")+"/api/board/"+id+"/raw", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		// board absent: create it, then retry the push.
		if err := CreateBoard(server, token, id, id, content); err != nil {
			return 0, err
		}
		return 1, nil
	}
	if resp.StatusCode >= 400 {
		return 0, httpError(resp)
	}
	var out struct {
		Version int `json:"version"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out.Version, nil
}
