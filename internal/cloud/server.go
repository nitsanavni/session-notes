package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/nitsanavni/session-notes/internal/board"
	"github.com/nitsanavni/session-notes/internal/keymap"
	"github.com/nitsanavni/session-notes/internal/web"
)

// Server is the cloud-mode HTTP server: a superset of the file-mode web API
// backed by the SQLite Store instead of on-disk board files. It speaks the same
// board JSON, edit, and SSE protocol so both the browser board.html and the CLI
// RemoteTree drive it unchanged.
type Server struct {
	store    *Store
	insecure bool // when true, bearer auth is not required (local dev)
}

// NewServer wraps a store. insecure disables bearer auth (loopback dev only).
func NewServer(store *Store, insecure bool) *Server {
	return &Server{store: store, insecure: insecure}
}

// Handler returns the routing table, bearer/cookie auth-wrapped unless insecure.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /b/{id}", s.handleBoardPage)
	mux.HandleFunc("GET /api/boards", s.handleBoards)
	mux.HandleFunc("POST /api/boards", s.handleCreateBoard)
	mux.HandleFunc("GET /api/board/{id}", s.handleBoard)
	mux.HandleFunc("GET /api/board/{id}/raw", s.handleRaw)
	mux.HandleFunc("PUT /api/board/{id}/raw", s.handlePutRaw)
	mux.HandleFunc("POST /api/board/{id}/edit", s.handleEdit)
	mux.HandleFunc("GET /api/board/{id}/events", s.handleEvents)
	mux.HandleFunc("GET /api/board/{id}/history", s.handleHistory)
	mux.HandleFunc("POST /api/tokens", s.handleCreateToken)
	mux.HandleFunc("GET /api/board/{id}/grants", s.handleListGrants)
	mux.HandleFunc("POST /api/board/{id}/grants", s.handleAddGrant)
	mux.HandleFunc("DELETE /api/board/{id}/grants", s.handleRevokeGrant)
	mux.HandleFunc("GET /api/keymap", s.handleKeymap)
	if s.insecure {
		return mux
	}
	return s.authWrap(mux)
}

const authCookie = "session-notes-token"

type ctxKey int

const identityKey ctxKey = 0

// identity returns the principal behind the request. In insecure mode every
// request is an implicit all-boards admin; otherwise the identity was resolved
// and stashed by authWrap.
func (s *Server) identity(r *http.Request) Identity {
	if s.insecure {
		return Identity{Subject: "insecure", Admin: true}
	}
	if id, ok := r.Context().Value(identityKey).(Identity); ok {
		return id
	}
	return Identity{}
}

// authWrap admits a request presenting a valid bearer token (Authorization:
// Bearer, ?token=, or the cookie a tokened visit set) and stashes the resolved
// identity in the request context for per-board grant checks. Everything else
// is 401.
func (s *Server) authWrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		admit := func(id Identity) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityKey, id)))
		}
		if c, err := r.Cookie(authCookie); err == nil {
			if id, ok := s.store.TokenIdentity(c.Value); ok {
				admit(id)
				return
			}
		}
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			if id, ok := s.store.TokenIdentity(strings.TrimPrefix(h, "Bearer ")); ok {
				admit(id)
				return
			}
		}
		if p := r.URL.Query().Get("token"); p != "" && s.store.ValidToken(p) {
			http.SetCookie(w, &http.Cookie{
				Name: authCookie, Value: p, Path: "/",
				HttpOnly: true, SameSite: http.SameSiteLaxMode,
			})
			q := r.URL.Query()
			q.Del("token")
			clean := r.URL.Path
			if len(q) > 0 {
				clean += "?" + q.Encode()
			}
			http.Redirect(w, r, clean, http.StatusFound)
			return
		}
		httpErr(w, http.StatusUnauthorized, "token required (Authorization: Bearer, ?token=, or cookie)")
	})
}

// permRank orders the perm ladder so a check can require "at least" a level.
func permRank(p string) int {
	switch p {
	case "read":
		return 1
	case "write":
		return 2
	case "admin":
		return 3
	}
	return 0
}

// authorize resolves the caller's grant on a board and checks it meets need.
// It returns the effective grant and an HTTP status: 200 authorized, 404 when
// the caller has no grant at all (existence is not leaked), 403 when a grant
// exists but is too weak. Admin identities get an implicit whole-board admin
// grant on every board.
func (s *Server) authorize(r *http.Request, boardID, need string) (Grant, int) {
	id := s.identity(r)
	if id.Admin {
		return Grant{Subject: id.Subject, BoardID: boardID, Perm: "admin"}, http.StatusOK
	}
	if id.Subject == "" {
		return Grant{}, http.StatusNotFound
	}
	g, ok := s.store.GrantFor(id.Subject, boardID)
	if !ok {
		return Grant{}, http.StatusNotFound
	}
	if permRank(g.Perm) < permRank(need) {
		return g, http.StatusForbidden
	}
	return g, http.StatusOK
}

// denied writes the status authorize produced (404 hidden as not-found, 403 as
// forbidden) and reports whether the request should stop.
func denied(w http.ResponseWriter, status int) bool {
	switch status {
	case http.StatusOK:
		return false
	case http.StatusNotFound:
		http.Error(w, "not found", http.StatusNotFound)
	default:
		httpErr(w, http.StatusForbidden, "forbidden")
	}
	return true
}

// ---- pages ----

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	servePage(w, "index.html")
}

func (s *Server) handleBoardPage(w http.ResponseWriter, r *http.Request) {
	if !s.store.BoardExists(r.PathValue("id")) {
		http.NotFound(w, r)
		return
	}
	if _, status := s.authorize(r, r.PathValue("id"), "read"); status != http.StatusOK {
		http.NotFound(w, r)
		return
	}
	servePage(w, "board.html")
}

func servePage(w http.ResponseWriter, name string) {
	data, err := web.Asset(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

// ---- boards ----

func (s *Server) handleBoards(w http.ResponseWriter, r *http.Request) {
	ids, err := s.store.ListBoards()
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	type card struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Session string `json:"session"`
		Cwd     string `json:"cwd"`
	}
	cards := []card{}
	for _, id := range ids {
		if _, status := s.authorize(r, id, "read"); status != http.StatusOK {
			continue // hide boards the caller has no grant on
		}
		content, _, err := s.store.Get(id)
		if err != nil {
			continue
		}
		b := board.Parse(content)
		name := b.Frontmatter.Title
		if name == "" {
			name = id
		}
		cards = append(cards, card{ID: id, Name: name, Session: b.Frontmatter.Session, Cwd: b.Frontmatter.Cwd})
	}
	writeJSON(w, map[string]any{"boards": cards})
}

// createReq creates a board: an explicit id/name plus optional seed content.
type createReq struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Content string `json:"content"`
}

func (s *Server) handleCreateBoard(w http.ResponseWriter, r *http.Request) {
	if !s.identity(r).Admin {
		httpErr(w, http.StatusForbidden, "admin required to create boards")
		return
	}
	var req createReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, http.StatusBadRequest, "bad JSON: "+err.Error())
		return
	}
	id := req.ID
	if id == "" {
		id = req.Name
	}
	if id == "" {
		httpErr(w, http.StatusBadRequest, "id or name required")
		return
	}
	if s.store.BoardExists(id) {
		httpErr(w, http.StatusConflict, "board already exists: "+id)
		return
	}
	content := req.Content
	if content == "" {
		title := req.Name
		if title == "" {
			title = id
		}
		content = "---\ntitle: " + title + "\n---\n\n## Threads\n\n## Log\n"
	}
	if err := s.store.CreateBoard(id, content); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_, version, _ := s.store.Get(id)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "version": version})
}

func (s *Server) handleBoard(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	grant, status := s.authorize(r, id, "read")
	if denied(w, status) {
		return
	}
	content, version, err := s.store.Get(id)
	if errors.Is(err, ErrNoBoard) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	b := board.Parse(content)
	if grant.Root != "" {
		scoped, ok := b.Scoped(grant.Root)
		if !ok {
			http.NotFound(w, r)
			return
		}
		b = scoped
	}
	model := web.BoardModel(id, b, false, false)
	// Wrap so the version rides alongside the board JSON without disturbing the
	// browser client (which ignores unknown fields). The CLI reads "version".
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", fmt.Sprintf("%d", version))
	raw, _ := json.Marshal(model)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	m["version"] = version
	if grant.Root != "" {
		m["scope"] = grant.Root // browser shows a "scoped view" indicator
	}
	_ = json.NewEncoder(w).Encode(m)
}

func (s *Server) handleRaw(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	grant, status := s.authorize(r, id, "read")
	if denied(w, status) {
		return
	}
	content, version, err := s.store.Get(id)
	if errors.Is(err, ErrNoBoard) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if grant.Root != "" {
		scoped, ok := board.Parse(content).Scoped(grant.Root)
		if !ok {
			http.NotFound(w, r)
			return
		}
		content = scoped.Render()
	}
	writeJSON(w, map[string]any{"content": content, "version": version})
}

func (s *Server) handlePutRaw(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// A whole-blob replace ignores the carve-out, so a subtree-scoped grant may
	// not push; require write on the whole board.
	if grant, status := s.authorize(r, id, "write"); denied(w, status) {
		return
	} else if grant.Root != "" {
		httpErr(w, http.StatusForbidden, "subtree-scoped token cannot replace the whole board")
		return
	}
	var req struct {
		Content string `json:"content"`
		Author  string `json:"author"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, http.StatusBadRequest, "bad JSON: "+err.Error())
		return
	}
	version, err := s.store.SetContent(id, req.Content, req.Author)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"version": version})
}

// ---- edits ----

// editReq is the wire form of one mutation. It is a superset of the file-mode
// web editReq (op/id/section/raw/text/status/urgent/pinned/author) plus the
// CLI's Query and Root addressing and an optional Base version for conflict
// detection. base<0 (the default when omitted, encoded as -1) disables the
// check; base>=0 requires the stored version to match or the write 409s.
type editReq struct {
	Op      string `json:"op"`
	ID      string `json:"id"`
	Root    string `json:"root"`
	Section string `json:"section"`
	Raw     string `json:"raw"`
	Query   string `json:"query"`
	Text    string `json:"text"`
	Status  string `json:"status"`
	Author  string `json:"author"`
	Urgent  *bool  `json:"urgent"`
	Pinned  *bool  `json:"pinned"`
	Base    *int   `json:"base"`
}

func (s *Server) handleEdit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	grant, status := s.authorize(r, id, "write")
	if denied(w, status) {
		return
	}
	if !s.store.BoardExists(id) {
		http.NotFound(w, r)
		return
	}
	var req editReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, http.StatusBadRequest, "bad JSON: "+err.Error())
		return
	}
	// Mandatory carve-out: a subtree-scoped grant forces every op's Root to the
	// granted node. A client-supplied Root that isn't the grant root (or empty)
	// is refused — a scoped agent cannot address outside its subtree.
	if grant.Root != "" {
		if req.Root != "" && req.Root != grant.Root {
			httpErr(w, http.StatusForbidden, "op root outside granted subtree")
			return
		}
		req.Root = grant.Root
	}
	op, err := toOp(req)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	base := -1
	if req.Base != nil {
		base = *req.Base
	}
	author := req.Author
	if author == "" {
		author = s.identity(r).Subject // author defaults to the token subject
	}
	if author == "" {
		author = "remote"
	}
	res, err := s.store.ApplyOp(id, op, base, author)
	switch {
	case err == nil:
		writeJSON(w, map[string]any{"version": res.Version, "note": res.Note, "id": res.NodeID})
	case errors.Is(err, ErrConflict):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "version conflict", "version": res.Version})
	case errors.Is(err, board.ErrOpNotFound):
		httpErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, board.ErrOpInvalid):
		httpErr(w, http.StatusBadRequest, err.Error())
	default:
		httpErr(w, http.StatusInternalServerError, err.Error())
	}
}

// toOp translates a wire editReq into a board.Op, validating op-specific fields.
func toOp(req editReq) (board.Op, error) {
	op := board.Op{
		Name:    req.Op,
		ID:      req.ID,
		Root:    req.Root,
		Section: req.Section,
		Raw:     req.Raw,
		Query:   req.Query,
		Text:    req.Text,
		Author:  req.Author,
	}
	switch req.Op {
	case "status":
		st, ok := parseStatus(req.Status)
		if !ok {
			return op, fmt.Errorf("unknown status %q", req.Status)
		}
		op.Status = st
	case "urgent":
		if req.Urgent == nil {
			return op, errors.New("urgent flag required")
		}
		op.Urgent = *req.Urgent
	case "pin":
		if req.Pinned == nil {
			return op, errors.New("pin flag required")
		}
		op.Pinned = *req.Pinned
	}
	return op, nil
}

func parseStatus(s string) (board.Status, bool) {
	switch s {
	case "open":
		return board.StatusOpen, true
	case "wip":
		return board.StatusInProgress, true
	case "done":
		return board.StatusDone, true
	case "blocked":
		return board.StatusBlocked, true
	case "none", "":
		return board.StatusNone, true
	}
	return board.StatusNone, false
}

// ---- SSE ----

// handleEvents streams "changed" whenever the board's version bumps. Optional
// ?node=<id> scopes delivery: an event fires only when the subtree rooted at
// that node actually changed between the client's last-seen and current
// content, so edits to sibling subtrees stay invisible (the remote carve-out).
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	grant, status := s.authorize(r, id, "read")
	if denied(w, status) {
		return
	}
	if !s.store.BoardExists(id) {
		http.NotFound(w, r)
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	node := r.URL.Query().Get("node")
	if grant.Root != "" {
		node = grant.Root // scoped tokens only ever see their subtree's events
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	fmt.Fprint(w, ": connected\n\n")
	fl.Flush()

	ch, cancel := s.store.Subscribe(id)
	defer cancel()

	prev, _, _ := s.store.Get(id)
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		case <-ch:
			cur, _, err := s.store.Get(id)
			if err != nil {
				return
			}
			if node != "" && !subtreeChanged(prev, cur, node) {
				prev = cur
				continue
			}
			prev = cur
			fmt.Fprint(w, "data: changed\n\n")
			fl.Flush()
		}
	}
}

// subtreeChanged reports whether the subtree rooted at id differs between two
// board contents. Empty id (whole board) tracks any change.
func subtreeChanged(before, after, id string) bool {
	if id == "" {
		return before != after
	}
	b, _ := board.SubtreeSource(before, id)
	a, _ := board.SubtreeSource(after, id)
	return b != a
}

// ---- history ----

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	grant, status := s.authorize(r, id, "read")
	if denied(w, status) {
		return
	}
	if grant.Root != "" {
		// History is whole-board line-diffs; a subtree-scoped grant would leak
		// sibling edits, so it is refused rather than half-filtered.
		httpErr(w, http.StatusForbidden, "history unavailable for subtree-scoped tokens")
		return
	}
	if !s.store.BoardExists(id) {
		http.NotFound(w, r)
		return
	}
	entries, err := s.store.History(id)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	type entryJSON struct {
		Time    string   `json:"ts"`
		Author  string   `json:"author"`
		Added   []string `json:"added,omitempty"`
		Removed []string `json:"removed,omitempty"`
	}
	out := make([]entryJSON, 0, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		added, removed := board.DiffLines(e.Before, e.After)
		out = append(out, entryJSON{Time: e.TS.Format(time.RFC3339), Author: e.Author, Added: added, Removed: removed})
	}
	writeJSON(w, map[string]any{"entries": out})
}

func (s *Server) handleKeymap(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, keymap.Web())
}

// ---- token + grant management (admin only) ----

// handleCreateToken mints a bearer token over HTTP (admin only). Used by
// `remote grant --new-token` to provision a sub-agent identity remotely.
func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	if !s.identity(r).Admin {
		httpErr(w, http.StatusForbidden, "admin required")
		return
	}
	var req struct {
		Name    string `json:"name"`
		Subject string `json:"subject"`
		Admin   bool   `json:"admin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, http.StatusBadRequest, "bad JSON: "+err.Error())
		return
	}
	if req.Name == "" {
		httpErr(w, http.StatusBadRequest, "name required")
		return
	}
	subject := req.Subject
	if subject == "" {
		subject = req.Name
	}
	token, err := s.store.CreateTokenAs(req.Name, subject, req.Admin)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"token": token, "subject": subject})
}

func (s *Server) handleListGrants(w http.ResponseWriter, r *http.Request) {
	if !s.identity(r).Admin {
		httpErr(w, http.StatusForbidden, "admin required")
		return
	}
	grants, err := s.store.ListGrants(r.PathValue("id"))
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if grants == nil {
		grants = []Grant{}
	}
	writeJSON(w, map[string]any{"grants": grants})
}

// grantReq addresses a grant by subject, by an existing token's name, or mints a
// fresh token (newToken) whose subject is the grantee — the one-step carve-out
// handoff.
type grantReq struct {
	Subject   string `json:"subject"`
	TokenName string `json:"tokenName"`
	NewToken  string `json:"newToken"`
	Root      string `json:"root"`
	Perm      string `json:"perm"`
}

func (s *Server) handleAddGrant(w http.ResponseWriter, r *http.Request) {
	if !s.identity(r).Admin {
		httpErr(w, http.StatusForbidden, "admin required")
		return
	}
	id := r.PathValue("id")
	if !s.store.BoardExists(id) {
		http.NotFound(w, r)
		return
	}
	var req grantReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, http.StatusBadRequest, "bad JSON: "+err.Error())
		return
	}
	if req.Perm == "" {
		req.Perm = "read"
	}
	subject := req.Subject
	var minted string
	switch {
	case req.NewToken != "":
		subject = req.NewToken
		tok, err := s.store.CreateTokenAs(req.NewToken, subject, false)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		minted = tok
	case req.TokenName != "":
		sub, ok := s.store.SubjectForTokenName(req.TokenName)
		if !ok {
			httpErr(w, http.StatusBadRequest, "no token named "+req.TokenName)
			return
		}
		subject = sub
	}
	if subject == "" {
		httpErr(w, http.StatusBadRequest, "subject, tokenName, or newToken required")
		return
	}
	if err := s.store.AddGrant(subject, id, req.Root, req.Perm); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	out := map[string]any{"subject": subject, "perm": req.Perm, "root": req.Root}
	if minted != "" {
		out["token"] = minted
	}
	writeJSON(w, out)
}

func (s *Server) handleRevokeGrant(w http.ResponseWriter, r *http.Request) {
	if !s.identity(r).Admin {
		httpErr(w, http.StatusForbidden, "admin required")
		return
	}
	id := r.PathValue("id")
	var req struct {
		Subject   string `json:"subject"`
		TokenName string `json:"tokenName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, http.StatusBadRequest, "bad JSON: "+err.Error())
		return
	}
	subject := req.Subject
	if subject == "" && req.TokenName != "" {
		if sub, ok := s.store.SubjectForTokenName(req.TokenName); ok {
			subject = sub
		}
	}
	if subject == "" {
		httpErr(w, http.StatusBadRequest, "subject or tokenName required")
		return
	}
	if err := s.store.RemoveGrant(subject, id); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"revoked": subject})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
