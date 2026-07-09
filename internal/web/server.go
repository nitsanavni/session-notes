// Package web serves the session-notes web UI: the same boards the TUI shows,
// rendered in a browser for people who don't live in tmux. It is a thin HTTP
// frontend over the board package — every write goes through the same
// EditUnderLock (advisory flock + atomic rename) the TUI, hooks, and edit CLI
// use, so a browser edit can never clobber a concurrent edit from Claude or
// the terminal.
package web

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nitsanavni/session-notes/internal/board"
)

//go:embed assets/index.html assets/board.html
var assets embed.FS

// Liveness thresholds, mirroring the TUI dashboard's classification.
const (
	activeWindow = 2 * time.Minute
	idleWindow   = 2 * time.Hour
)

// Server holds the web UI's state: boards registered explicitly (paths outside
// the boards dir), an optional home board that "/" redirects to, and a
// per-board undo history of this server's own writes.
type Server struct {
	extra map[string]string // board id -> explicit path
	home  string            // board id "/" redirects to ("" = dashboard)

	// mu serializes web edits and guards the history stacks. Web-originated
	// writes are rare and human-paced; one lock keeps undo bookkeeping simple.
	mu   sync.Mutex
	hist map[string][]histEntry // board path -> undo stack (oldest first)
	redo map[string][]histEntry // board path -> redo stack
}

// histEntry is one web edit: the full board content before and after. Undo
// only applies when the board still reads exactly `after` — an intervening
// write from Claude or the TUI refuses the undo instead of clobbering it.
type histEntry struct{ before, after string }

// histLimit bounds the per-board undo stack, mirroring the TUI's depth.
const histLimit = 100

// New returns an empty server; boards in board.BoardsDir() are always visible.
func New() *Server {
	return &Server{
		extra: map[string]string{},
		hist:  map[string][]histEntry{},
		redo:  map[string][]histEntry{},
	}
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

// Handler returns the routing table.
func (s *Server) Handler() http.Handler {
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
	s.mu.Lock()
	canUndo, canRedo := len(s.hist[path]) > 0, len(s.redo[path]) > 0
	s.mu.Unlock()
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
		return "gone", ""
	}
	d := now.Sub(mtime)
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
	case d < 48*time.Hour:
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
	// Base is set-content's optimistic lock: the verbatim content the client
	// loaded into its editor. The write applies only if the board still reads
	// exactly Base; otherwise 409, and the user's edit stays in their editor.
	Base string `json:"base"`
}

var (
	errNotFound = errors.New("item not found (board changed?)")
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

	s.mu.Lock()
	defer s.mu.Unlock()

	var err error
	switch req.Op {
	case "undo":
		err = s.timeTravel(path, s.hist, s.redo, func(e histEntry) (string, string) { return e.after, e.before })
	case "redo":
		err = s.timeTravel(path, s.redo, s.hist, func(e histEntry) (string, string) { return e.before, e.after })
	default:
		var entry histEntry
		err = board.EditUnderLock(path, "", func(content string) (string, error) {
			var out string
			if req.Op == "set-content" {
				// Whole-board raw replace (the TUI's E): optimistic-locked on
				// the content the editor loaded, so a concurrent write from
				// Claude or the TUI is never silently overwritten.
				if content != req.Base {
					return "", fmt.Errorf("%w: the board changed while you were editing — copy your text, reload, and re-apply", errConflict)
				}
				out = req.Text
				if out != "" && !strings.HasSuffix(out, "\n") {
					out += "\n"
				}
			} else {
				b := board.Parse(content)
				b.Path = path
				if aerr := applyEdit(b, req); aerr != nil {
					return "", aerr
				}
				out = b.Render()
			}
			entry = histEntry{before: content, after: out}
			return out, nil
		})
		if err == nil && entry.before != entry.after {
			s.hist[path] = append(s.hist[path], entry)
			if len(s.hist[path]) > histLimit {
				s.hist[path] = s.hist[path][1:]
			}
			s.redo[path] = nil // a fresh edit invalidates the redo line
		}
	}

	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, errNotFound), errors.Is(err, errConflict):
		httpErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, errBadReq):
		httpErr(w, http.StatusBadRequest, err.Error())
	default:
		httpErr(w, http.StatusInternalServerError, err.Error())
	}
}

// timeTravel pops the top of `from` and writes its other side, but only when
// the board on disk still reads exactly the state this server last left it in
// — an intervening write from Claude or the TUI refuses with a conflict
// rather than silently discarding that write. A successful move lands the
// entry on `to`, so undo and redo are the same walk in opposite directions.
// Callers hold s.mu.
func (s *Server) timeTravel(path string, from, to map[string][]histEntry, pick func(histEntry) (expect, restore string)) error {
	stack := from[path]
	if len(stack) == 0 {
		return fmt.Errorf("%w: nothing to undo/redo", errBadReq)
	}
	entry := stack[len(stack)-1]
	expect, restore := pick(entry)
	err := board.EditUnderLock(path, "", func(content string) (string, error) {
		if content != expect {
			return "", fmt.Errorf("%w: the board changed since the last web edit; undo/redo refused", errConflict)
		}
		return restore, nil
	})
	if err != nil {
		return err
	}
	from[path] = stack[:len(stack)-1]
	to[path] = append(to[path], entry)
	return nil
}

func applyEdit(b *board.Board, req editReq) error {
	locate := func() (*board.Item, error) {
		if it := b.FindByRawInSection(req.Section, req.Raw); it != nil {
			return it, nil
		}
		if it, n := b.FindByTextInSection(req.Section, board.NormalizeItemText(req.Raw)); n == 1 {
			return it, nil
		}
		return nil, errNotFound
	}
	switch req.Op {
	case "add":
		if b.Section(req.Section) == nil {
			return fmt.Errorf("%w: no section %q", errBadReq, req.Section)
		}
		if strings.TrimSpace(req.Text) == "" {
			return fmt.Errorf("%w: empty text", errBadReq)
		}
		b.AddItem(req.Section, req.Text)
	case "reply":
		// In-thread semantics (the TUI's R): flat conversations. Fork nests.
		it, err := locate()
		if err != nil {
			return err
		}
		if strings.TrimSpace(req.Text) == "" {
			return fmt.Errorf("%w: empty text", errBadReq)
		}
		b.ReplyFlat(it, req.Text)
	case "fork":
		it, err := locate()
		if err != nil {
			return err
		}
		if strings.TrimSpace(req.Text) == "" {
			return fmt.Errorf("%w: empty text", errBadReq)
		}
		b.AddReply(it, req.Text)
	case "edit":
		it, err := locate()
		if err != nil {
			return err
		}
		if strings.TrimSpace(req.Text) == "" {
			return fmt.Errorf("%w: empty text", errBadReq)
		}
		it.Text = req.Text
	case "status":
		it, err := locate()
		if err != nil {
			return err
		}
		st, ok := parseStatus(req.Status)
		if !ok {
			return fmt.Errorf("%w: unknown status %q", errBadReq, req.Status)
		}
		it.SetStatus(st)
	case "urgent":
		it, err := locate()
		if err != nil {
			return err
		}
		if req.Urgent == nil {
			return fmt.Errorf("%w: urgent flag required", errBadReq)
		}
		if it.Urgent != *req.Urgent {
			it.ToggleUrgent()
		}
	case "archive":
		it, err := locate()
		if err != nil {
			return err
		}
		if !b.ArchiveItem(it) {
			return errNotFound
		}
	case "delete":
		it, err := locate()
		if err != nil {
			return err
		}
		if !b.Remove(it) {
			return errNotFound
		}
	case "log":
		if strings.TrimSpace(req.Text) == "" {
			return fmt.Errorf("%w: empty text", errBadReq)
		}
		author := req.Author
		if author == "" {
			author = "user"
		}
		b.AppendLog(author, req.Text)
	case "title":
		b.SetTitle(req.Text)
	case "add-section":
		title := strings.TrimSpace(req.Text)
		if title == "" {
			return fmt.Errorf("%w: empty section title", errBadReq)
		}
		if b.Section(title) != nil {
			return fmt.Errorf("%w: section %q already exists", errBadReq, title)
		}
		b.AddSection(title)
	case "rename-section":
		sec := b.Section(req.Section)
		if sec == nil {
			return errNotFound
		}
		if !b.RenameSection(sec, strings.TrimSpace(req.Text)) {
			return fmt.Errorf("%w: cannot rename to %q (empty or duplicate title)", errBadReq, req.Text)
		}
	case "archive-section":
		sec := b.Section(req.Section)
		if sec == nil {
			return errNotFound
		}
		if !b.ArchiveSection(sec) {
			return fmt.Errorf("%w: the %s section cannot be archived", errBadReq, req.Section)
		}
	case "delete-section":
		sec := b.Section(req.Section)
		if sec == nil {
			return errNotFound
		}
		b.RemoveSection(sec)
	default:
		return fmt.Errorf("%w: unknown op %q", errBadReq, req.Op)
	}
	return nil
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
	data, err := os.ReadFile(notePath)
	if os.IsNotExist(err) {
		// Same seed the TUI uses when opening a missing side note — but created
		// lazily on first save, not on read.
		writeJSON(w, map[string]any{"name": name, "content": "# " + name + "\n", "exists": false})
		return
	}
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"name": name, "content": string(data), "exists": true})
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

	last := fileStamp(path)
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
			if cur := fileStamp(path); cur != last {
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

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
