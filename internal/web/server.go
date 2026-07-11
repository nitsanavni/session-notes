// Package web serves the session-notes web UI: the same boards the TUI shows,
// rendered in a browser for people who don't live in tmux. It is a thin HTTP
// frontend over the board package — every write goes through the same
// EditUnderLock (advisory flock + atomic rename) the TUI, hooks, and edit CLI
// use, so a browser edit can never clobber a concurrent edit from Claude or
// the terminal.
package web

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nitsanavni/session-notes/internal/board"
	"github.com/nitsanavni/session-notes/internal/keymap"
)

//go:embed assets/index.html assets/board.html
var assets embed.FS

// Liveness thresholds, mirroring the TUI dashboard's classification.
const (
	activeWindow = 2 * time.Minute
	idleWindow   = 2 * time.Hour
)

// Server holds the web UI's state: boards registered explicitly (paths outside
// the boards dir) and an optional home board that "/" redirects to. Undo state
// is NOT here — it lives in the board's shared undo journal (board.Undo), so
// web undo survives server restarts and interleaves with `session-notes edit
// undo` on one timeline.
type Server struct {
	extra map[string]string // board id -> explicit path
	home  string            // board id "/" redirects to ("" = dashboard)
	token string            // "" = no auth (loopback-only use)
}

// New returns an empty server; boards in board.BoardsDir() are always visible.
func New() *Server {
	return &Server{extra: map[string]string{}}
}

// Register makes a board outside the boards directory reachable, returning the
// id it is served under (frontmatter session, else the file's base name).
func (s *Server) Register(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	b, err := board.Load(abs)
	if err != nil {
		return "", err
	}
	id := b.Frontmatter.Session
	if id == "" {
		id = strings.TrimSuffix(filepath.Base(abs), ".md")
	}
	s.extra[id] = abs
	return id, nil
}

// SetHome makes "/" redirect to the given board instead of the dashboard.
func (s *Server) SetHome(id string) { s.home = id }

// SetToken gates every request behind a shared secret (see authWrap). Enables
// binding beyond loopback: `serve --addr :port --token <t>`.
func (s *Server) SetToken(t string) { s.token = t }

// Handler returns the routing table, auth-wrapped when a token is set.
func (s *Server) Handler() http.Handler {
	var h http.Handler = s.routes()
	if s.token != "" {
		h = authWrap(s.token, h)
	}
	return h
}

// authCookie carries the token between requests once presented, so a single
// tokened URL (http://host:port/?token=…) signs the whole browser session in
// — EventSource and fetch can't set custom headers, but they send cookies.
const authCookie = "session-notes-token"

// authWrap admits a request that presents the token as a Bearer header, a
// ?token= query parameter, or the cookie a previous tokened request set.
// Comparisons are constant-time. Everything else gets 401 — including "/",
// so an unauthenticated scan learns nothing about which boards exist.
func authWrap(token string, next http.Handler) http.Handler {
	ok := func(candidate string) bool {
		return subtle.ConstantTimeCompare([]byte(candidate), []byte(token)) == 1
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(authCookie); err == nil && ok(c.Value) {
			next.ServeHTTP(w, r)
			return
		}
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") && ok(strings.TrimPrefix(h, "Bearer ")) {
			next.ServeHTTP(w, r)
			return
		}
		if p := r.URL.Query().Get("token"); p != "" && ok(p) {
			http.SetCookie(w, &http.Cookie{
				Name: authCookie, Value: p, Path: "/",
				HttpOnly: true, SameSite: http.SameSiteLaxMode,
			})
			// Strip the token from the visible URL so it doesn't linger in
			// the address bar or get copied into shared links.
			q := r.URL.Query()
			q.Del("token")
			clean := r.URL.Path
			if len(q) > 0 {
				clean += "?" + q.Encode()
			}
			http.Redirect(w, r, clean, http.StatusFound)
			return
		}
		httpErr(w, http.StatusUnauthorized, "token required (Bearer header, ?token=, or the cookie a tokened visit sets)")
	})
}

func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleHome)
	mux.HandleFunc("GET /b/{id}", s.handleBoardPage)
	mux.HandleFunc("GET /api/boards", s.handleBoards)
	mux.HandleFunc("GET /api/board/{id}", s.handleBoard)
	mux.HandleFunc("POST /api/board/{id}/edit", s.handleEdit)
	mux.HandleFunc("GET /api/board/{id}/events", s.handleEvents)
	mux.HandleFunc("GET /api/board/{id}/note/{name}", s.handleNoteGet)
	mux.HandleFunc("POST /api/board/{id}/note/{name}", s.handleNotePut)
	mux.HandleFunc("GET /api/board/{id}/raw", s.handleRaw)
	mux.HandleFunc("GET /api/board/{id}/history", s.handleHistory)
	mux.HandleFunc("GET /api/keymap", s.handleKeymap)
	return mux
}

// resolve maps a board id to its file path, or "" when unknown.
func (s *Server) resolve(id string) string {
	if p, ok := s.extra[id]; ok {
		return p
	}
	// Only bare ids resolve through the boards dir — no path traversal.
	if strings.ContainsAny(id, "/\\") || id == "." || id == ".." {
		return ""
	}
	p := board.BoardPath(id)
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	if s.home != "" {
		http.Redirect(w, r, "/b/"+s.home, http.StatusFound)
		return
	}
	servePage(w, "assets/index.html")
}

func (s *Server) handleBoardPage(w http.ResponseWriter, r *http.Request) {
	if s.resolve(r.PathValue("id")) == "" {
		http.NotFound(w, r)
		return
	}
	servePage(w, "assets/board.html")
}

func servePage(w http.ResponseWriter, name string) {
	data, err := assets.ReadFile(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

// ---- JSON model ----

type itemJSON struct {
	Text         string     `json:"text"`
	Raw          string     `json:"raw"`
	Status       string     `json:"status"`
	Urgent       bool       `json:"urgent,omitempty"`
	Pinned       bool       `json:"pinned,omitempty"`
	Continuation bool       `json:"continuation,omitempty"`
	Undelivered  bool       `json:"undelivered,omitempty"`
	Children     []itemJSON `json:"children,omitempty"`
}

type sectionJSON struct {
	Title string     `json:"title"`
	Items []itemJSON `json:"items"`
}

type boardJSON struct {
	ID       string        `json:"id"`
	Session  string        `json:"session"`
	Title    string        `json:"title"`
	Cwd      string        `json:"cwd"`
	Path     string        `json:"path"`
	CanUndo  bool          `json:"canUndo"`
	CanRedo  bool          `json:"canRedo"`
	Sections []sectionJSON `json:"sections"`
	Status   *statusJSON   `json:"status,omitempty"`
}

// statusJSON mirrors the live session sidecar for the board footer.
type statusJSON struct {
	Model      string   `json:"model,omitempty"`
	ContextPct *float64 `json:"contextPct,omitempty"`
	CostUSD    float64  `json:"costUsd,omitempty"`
	Activity   string   `json:"activity,omitempty"`
}

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
	case "none":
		return board.StatusNone, true
	}
	return board.StatusNone, false
}

// itemsJSON converts an item tree, dropping blank raw lines (section spacing)
// while keeping meaningful continuation lines (ASCII blocks, stray prose).
func itemsJSON(items []*board.Item) []itemJSON {
	var out []itemJSON
	for _, it := range items {
		if it.IsContinuation() && strings.TrimSpace(it.Raw()) == "" {
			continue
		}
		j := itemJSON{
			Text:         it.DisplayText(),
			Raw:          it.Raw(),
			Continuation: it.IsContinuation(),
			Children:     itemsJSON(it.Children),
		}
		if it.IsItem() {
			j.Status = statusString(it.Status)
			j.Urgent = it.Urgent
			j.Pinned = it.Pinned
		}
		if it.IsContinuation() {
			// Preserve internal spacing for ASCII drawings; the client renders
			// continuations in a <pre>.
			j.Text = it.Raw()
		}
		out = append(out, j)
	}
	return out
}

// markUndelivered flags items whose raw line appears in the added-lines
// multiset from DiffLines(snapshot, board). Each flagged item consumes one
// occurrence, so duplicate lines mark the right number of items (in document
// order, matching how DiffLines counts).
func markUndelivered(secs []sectionJSON, added []string) {
	if len(added) == 0 {
		return
	}
	avail := map[string]int{}
	for _, l := range added {
		avail[l]++
	}
	var walk func(items []itemJSON)
	walk = func(items []itemJSON) {
		for i := range items {
			if avail[items[i].Raw] > 0 {
				avail[items[i].Raw]--
				items[i].Undelivered = true
			}
			walk(items[i].Children)
		}
	}
	for i := range secs {
		walk(secs[i].Items)
	}
}

func (s *Server) handleBoard(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	path := s.resolve(id)
	if path == "" {
		http.NotFound(w, r)
		return
	}
	b, err := board.Load(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	undoN, redoN := board.UndoDepths(path)
	canUndo, canRedo := undoN > 0, redoN > 0
	resp := boardJSON{
		ID:      id,
		Session: b.Frontmatter.Session,
		Title:   b.Frontmatter.Title,
		Cwd:     b.Frontmatter.Cwd,
		Path:    path,
		CanUndo: canUndo,
		CanRedo: canRedo,
	}
	for _, sec := range b.Sections {
		resp.Sections = append(resp.Sections, sectionJSON{Title: sec.Title, Items: itemsJSON(sec.Items)})
	}
	// Delivery receipts: lines the watch snapshot does not contain yet are
	// changes not delivered to the watching agent (watch and edit
	// --refresh-snapshot advance the snapshot exactly when a change has been
	// printed to it). No snapshot sidecar → no watcher → no markers.
	if snap, err := os.ReadFile(board.WatchSnapshotPath(path)); err == nil {
		if cur, err := os.ReadFile(path); err == nil {
			added, _ := board.DiffLines(string(snap), string(cur))
			markUndelivered(resp.Sections, added)
		}
	}
	if st, ok := board.ReadStatus(b.Frontmatter.Session); ok && st.Fresh(time.Now()) {
		sj := &statusJSON{Model: st.Model, CostUSD: st.CostUSD, Activity: st.Activity}
		if st.HasContext() {
			pct := st.ContextPct
			sj.ContextPct = &pct
		}
		resp.Status = sj
	}
	writeJSON(w, resp)
}

// ---- dashboard cards ----

type cardJSON struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Session  string   `json:"session"`
	Cwd      string   `json:"cwd"`
	Liveness string   `json:"liveness"` // active | idle | gone
	Ago      string   `json:"ago"`      // human relative activity time ("" = none)
	Threads  []string `json:"threads,omitempty"`
	Urgent   []string `json:"urgent,omitempty"`
	LastLog  string   `json:"lastLog,omitempty"`
}

func (s *Server) handleBoards(w http.ResponseWriter, r *http.Request) {
	type entry struct {
		id   string
		path string
	}
	seen := map[string]bool{}
	var entries []entry
	for id, p := range s.extra {
		entries = append(entries, entry{id, p})
		seen[p] = true
	}
	if files, err := os.ReadDir(board.BoardsDir()); err == nil {
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".md") {
				continue
			}
			p := filepath.Join(board.BoardsDir(), f.Name())
			if !seen[p] {
				entries = append(entries, entry{strings.TrimSuffix(f.Name(), ".md"), p})
			}
		}
	}

	now := time.Now()
	var cards []cardJSON
	mtimes := map[string]time.Time{}
	for _, e := range entries {
		b, err := board.Load(e.path)
		if err != nil {
			continue
		}
		session := b.Frontmatter.Session
		if session == "" {
			session = e.id
		}
		c := cardJSON{ID: e.id, Session: session, Cwd: b.Frontmatter.Cwd}
		c.Name = b.Frontmatter.Title
		if c.Name == "" {
			c.Name = shortSession(session)
		}
		mtime := board.Liveness(b.Frontmatter.Cwd, session)
		mtimes[e.id] = mtime
		c.Liveness, c.Ago = classify(mtime, now)
		if sec := b.Section("Threads"); sec != nil {
			for _, it := range sec.Items {
				if it.IsItem() && it.Status == board.StatusInProgress {
					c.Threads = append(c.Threads, it.DisplayText())
					if len(c.Threads) == 3 {
						break
					}
				}
			}
		}
		for _, it := range b.UrgentOpenItems() {
			c.Urgent = append(c.Urgent, it.DisplayText())
		}
		if sec := b.Section("Log"); sec != nil {
			for i := len(sec.Items) - 1; i >= 0; i-- {
				if sec.Items[i].IsItem() {
					c.LastLog = sec.Items[i].DisplayText()
					break
				}
			}
		}
		cards = append(cards, c)
	}
	sort.SliceStable(cards, func(i, j int) bool {
		a, b := mtimes[cards[i].ID], mtimes[cards[j].ID]
		if a.Equal(b) {
			return cards[i].Session < cards[j].Session
		}
		if a.IsZero() || b.IsZero() {
			return b.IsZero()
		}
		return a.After(b)
	})
	writeJSON(w, map[string]any{"boards": cards})
}

func shortSession(s string) string {
	if i := strings.IndexByte(s, '-'); i > 0 {
		return s[:i]
	}
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

func classify(mtime, now time.Time) (liveness, ago string) {
	if mtime.IsZero() {
		return "gone", "—"
	}
	d := now.Sub(mtime)
	if d < 0 {
		d = 0
	}
	switch {
	case d < activeWindow:
		liveness = "active"
	case d < idleWindow:
		liveness = "idle"
	default:
		liveness = "gone"
	}
	switch {
	case d < time.Minute:
		ago = fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		ago = fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		ago = fmt.Sprintf("%dh", int(d.Hours()))
	default:
		ago = fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	return liveness, ago
}

// ---- edits ----

// editReq is one board mutation from the browser. Items are addressed by
// section title + verbatim raw line (the same identity the TUI's rebase uses),
// with a normalized-text fallback when the line drifted cosmetically.
type editReq struct {
	// Op: add | reply | fork | edit | status | urgent | archive | delete |
	// log | title | add-section | rename-section | archive-section |
	// delete-section | set-content | undo | redo
	Op      string `json:"op"`
	Section string `json:"section"`
	Raw     string `json:"raw"`
	Text    string `json:"text"`
	Status  string `json:"status"`
	Author  string `json:"author"`
	Urgent  *bool  `json:"urgent"`
	Pinned  *bool  `json:"pinned"`
	// Base is set-content's optimistic lock: the verbatim content the client
	// loaded into its editor. The write applies only if the board still reads
	// exactly Base; otherwise 409, and the user's edit stays in their editor.
	Base string `json:"base"`
}

var (
	errBadReq   = errors.New("bad request")
	errConflict = errors.New("conflict")
)

func (s *Server) handleEdit(w http.ResponseWriter, r *http.Request) {
	path := s.resolve(r.PathValue("id"))
	if path == "" {
		http.NotFound(w, r)
		return
	}
	var req editReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, http.StatusBadRequest, "bad JSON: "+err.Error())
		return
	}

	var err error
	switch req.Op {
	case "undo":
		err = board.Undo(path)
	case "redo":
		err = board.Redo(path)
	case "set-content":
		// Whole-board raw replace (the TUI's E): instead of an optimistic
		// 409 on drift, do the same non-destructive 3-way merge the TUI uses
		// (board.SaveEditorMerge) so a concurrent write from Claude or the TUI
		// is recovered rather than forcing the user to copy/reload/re-apply.
		out := req.Text
		if out != "" && !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		res, merr := board.SaveEditorMerge(path, req.Base, out)
		if merr != nil {
			httpErr(w, http.StatusInternalServerError, merr.Error())
			return
		}
		writeJSON(w, map[string]any{"conflicted": res.Conflicted})
		return
	default:
		// Every mutation is journaled (author "web") into the board's shared
		// undo timeline — the same one `session-notes edit undo` walks.
		err = board.EditUnderLockJournaled(path, "", "web", func(content string) (string, error) {
			b := board.Parse(content)
			b.Path = path
			if aerr := applyEdit(b, req); aerr != nil {
				return "", aerr
			}
			return b.Render(), nil
		})
	}

	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, board.ErrOpNotFound), errors.Is(err, errConflict), errors.Is(err, board.ErrUndoConflict):
		httpErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, errBadReq), errors.Is(err, board.ErrOpInvalid), errors.Is(err, board.ErrUndoEmpty):
		httpErr(w, http.StatusBadRequest, err.Error())
	default:
		httpErr(w, http.StatusInternalServerError, err.Error())
	}
}

// applyEdit translates the wire request into a board.Op and dispatches to
// the shared op layer — the same dispatcher the edit CLI uses, so every verb
// means one thing everywhere.
func applyEdit(b *board.Board, req editReq) error {
	op := board.Op{Name: req.Op, Section: req.Section, Raw: req.Raw, Text: req.Text, Author: req.Author}
	switch req.Op {
	case "status":
		st, ok := parseStatus(req.Status)
		if !ok {
			return fmt.Errorf("%w: unknown status %q", errBadReq, req.Status)
		}
		op.Status = st
	case "urgent":
		if req.Urgent == nil {
			return fmt.Errorf("%w: urgent flag required", errBadReq)
		}
		op.Urgent = *req.Urgent
	case "pin":
		if req.Pinned == nil {
			return fmt.Errorf("%w: pin flag required", errBadReq)
		}
		op.Pinned = *req.Pinned
	}
	_, err := board.Apply(b, op)
	return err
}

// handleRaw returns the board's verbatim markdown, the source for the web
// UI's whole-board editor (the browser's answer to the TUI's E).
func (s *Server) handleRaw(w http.ResponseWriter, r *http.Request) {
	path := s.resolve(r.PathValue("id"))
	if path == "" {
		http.NotFound(w, r)
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]string{"content": string(data)})
}

// handleHistory returns the board's journaled edits as line-diff summaries,
// newest first — the "what changed while I was away" timeline. Undone entries
// are not history; they were reverted.
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	path := s.resolve(r.PathValue("id"))
	if path == "" {
		http.NotFound(w, r)
		return
	}
	type entryJSON struct {
		Time    string   `json:"ts"`
		Author  string   `json:"author"`
		Added   []string `json:"added,omitempty"`
		Removed []string `json:"removed,omitempty"`
	}
	entries := board.History(path)
	out := make([]entryJSON, 0, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		added, removed := board.DiffLines(e.Before, e.After)
		out = append(out, entryJSON{Time: e.Time, Author: e.Author, Added: added, Removed: removed})
	}
	writeJSON(w, map[string]any{"entries": out})
}

// ---- side notes ----

// noteFile validates a [[name]] side-note link and returns its file path.
// Only plain side notes are servable: path-form links ([[with/slash]], [[~/x]])
// address arbitrary files relative to the session cwd, and exposing those over
// HTTP would turn the board server into a general file server — the browser
// renders them inert instead.
func noteFile(b *board.Board, name string) (string, error) {
	if name == "" || board.IsPathLink(name) || strings.Contains(name, "..") || strings.ContainsAny(name, "\\") {
		return "", fmt.Errorf("%w: not a side-note name", errBadReq)
	}
	return board.ResolveLink(b, name), nil
}

func (s *Server) handleNoteGet(w http.ResponseWriter, r *http.Request) {
	path := s.resolve(r.PathValue("id"))
	if path == "" {
		http.NotFound(w, r)
		return
	}
	b, err := board.Load(path)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	name := r.PathValue("name")
	notePath, err := noteFile(b, name)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	absNote := notePath
	if a, aerr := filepath.Abs(notePath); aerr == nil {
		absNote = a
	}
	data, err := os.ReadFile(notePath)
	if os.IsNotExist(err) {
		// Same seed the TUI uses when opening a missing side note — but created
		// lazily on first save, not on read.
		writeJSON(w, map[string]any{"name": name, "content": "# " + name + "\n", "exists": false, "path": absNote})
		return
	}
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"name": name, "content": string(data), "exists": true, "path": absNote})
}

func (s *Server) handleNotePut(w http.ResponseWriter, r *http.Request) {
	path := s.resolve(r.PathValue("id"))
	if path == "" {
		http.NotFound(w, r)
		return
	}
	b, err := board.Load(path)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	notePath, err := noteFile(b, r.PathValue("name"))
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var req struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, http.StatusBadRequest, "bad JSON: "+err.Error())
		return
	}
	if err := os.MkdirAll(filepath.Dir(notePath), 0o755); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Atomic replace, same discipline as board writes (notes have no lock:
	// they are single-writer in practice, and a torn read is still prevented).
	tmp := notePath + ".tmp"
	if err := os.WriteFile(tmp, []byte(req.Content), 0o644); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.Rename(tmp, notePath); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- live updates ----

// handleEvents is a server-sent-events stream that emits "changed" whenever
// the board file's mtime or size moves — the browser refetches the JSON on
// each event. Mtime polling (rather than a shared fsnotify watcher) keeps each
// connection self-contained; 500ms latency is plenty for a status board.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	path := s.resolve(r.PathValue("id"))
	if path == "" {
		http.NotFound(w, r)
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	fmt.Fprint(w, ": connected\n\n")
	fl.Flush()

	// Also watch the session status sidecar so the footer (model / context /
	// activity) refreshes live, without a separate poll endpoint.
	statusPath := ""
	if b, err := board.Load(path); err == nil {
		statusPath = board.StatusPath(b.Frontmatter.Session)
	}
	// The watch snapshot is part of the stamp so a delivery (snapshot advance)
	// refreshes the client's receipt markers even though the board is unchanged.
	stamp := func() string {
		return fileStamp(path) + "|" + fileStamp(statusPath) + "|" + fileStamp(board.WatchSnapshotPath(path))
	}
	last := stamp()
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		case <-tick.C:
			if cur := stamp(); cur != last {
				last = cur
				fmt.Fprint(w, "data: changed\n\n")
				fl.Flush()
			}
		}
	}
}

// fileStamp condenses a file's change identity (mtime + size) into one
// comparable string; "" means the file is missing.
func fileStamp(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d-%d", fi.ModTime().UnixNano(), fi.Size())
}

// handleKeymap serves the keybindings help table from the single source of
// truth (internal/keymap); the web help overlay builds its DOM from this so the
// keymap is never hand-copied into board.html.
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
