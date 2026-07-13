package cloud

import (
	"crypto/subtle"
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
	mux.HandleFunc("GET /api/keymap", s.handleKeymap)
	if s.insecure {
		return mux
	}
	return s.authWrap(mux)
}

const authCookie = "session-notes-token"

// authWrap admits a request presenting a valid bearer token (Authorization:
// Bearer, ?token=, or the cookie a tokened visit set). Everything else is 401.
func (s *Server) authWrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(authCookie); err == nil && s.store.ValidToken(c.Value) {
			next.ServeHTTP(w, r)
			return
		}
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") &&
			s.store.ValidToken(strings.TrimPrefix(h, "Bearer ")) {
			next.ServeHTTP(w, r)
			return
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
		_ = subtle.ConstantTimeCompare // constant-time compare happens in ValidToken
		httpErr(w, http.StatusUnauthorized, "token required (Authorization: Bearer, ?token=, or cookie)")
	})
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
	model := web.BoardModel(id, b, false, false)
	// Wrap so the version rides alongside the board JSON without disturbing the
	// browser client (which ignores unknown fields). The CLI reads "version".
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", fmt.Sprintf("%d", version))
	raw, _ := json.Marshal(model)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	m["version"] = version
	_ = json.NewEncoder(w).Encode(m)
}

func (s *Server) handleRaw(w http.ResponseWriter, r *http.Request) {
	content, version, err := s.store.Get(r.PathValue("id"))
	if errors.Is(err, ErrNoBoard) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"content": content, "version": version})
}

func (s *Server) handlePutRaw(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
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
	if !s.store.BoardExists(id) {
		http.NotFound(w, r)
		return
	}
	var req editReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, http.StatusBadRequest, "bad JSON: "+err.Error())
		return
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

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
