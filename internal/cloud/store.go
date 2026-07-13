// Package cloud is the M3 server backend: a "cloud mode" `session-notes server`
// that lets any Claude session anywhere attach to a board (or subtree) with the
// same Tree semantics as a local file. Storage is a markdown blob per board in
// SQLite (WAL, pure-Go modernc.org/sqlite, no cgo); boards are small so this
// reuses board.Parse/Render/Apply/EnsureIDs/undo wholesale. Node-granular
// storage is deferred — the board.Tree interface is the seam that allows
// swapping later.
package cloud

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nitsanavni/session-notes/internal/board"

	_ "modernc.org/sqlite"
)

// ErrConflict is returned by ApplyOp when the caller's base version no longer
// matches the stored version — a concurrent write landed first. The HTTP layer
// maps it to 409 so the client refetches and reapplies its surgical op.
var ErrConflict = errors.New("version conflict")

// ErrNoBoard is returned when an addressed board id does not exist.
var ErrNoBoard = errors.New("no such board")

// Store is the SQLite-backed board storage. Writes to a single board are
// serialized through a per-board mutex (locks map) so board.Apply always sees a
// consistent read-modify-write, mirroring the file backend's flock discipline.
type Store struct {
	db    *sql.DB
	mu    sync.Mutex              // guards locks
	locks map[string]*sync.Mutex  // board id -> write serializer
	subs  map[string][]chan int64 // board id -> version-bump subscribers
}

// Open opens (creating if needed) the SQLite database at path, enables WAL, and
// ensures the schema. Pass ":memory:" for tests.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// modernc.org/sqlite is a single connection-pool; a memory db must not be
	// closed/reopened between statements, so cap to one open connection which
	// also keeps WAL writes serialized at the driver level.
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("pragma %q: %w", pragma, err)
		}
	}
	schema := `
CREATE TABLE IF NOT EXISTS boards (
    id      TEXT PRIMARY KEY,
    content TEXT NOT NULL,
    version INTEGER NOT NULL,
    created INTEGER NOT NULL,
    updated INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS tokens (
    token_hash TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    subject    TEXT NOT NULL DEFAULT '',
    admin      INTEGER NOT NULL DEFAULT 0,
    created    INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS grants (
    subject      TEXT NOT NULL,
    board_id     TEXT NOT NULL,
    root_node_id TEXT NOT NULL DEFAULT '',
    perms        TEXT NOT NULL,
    created      INTEGER NOT NULL,
    PRIMARY KEY (subject, board_id)
);
CREATE TABLE IF NOT EXISTS ops (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    board_id TEXT NOT NULL,
    version  INTEGER NOT NULL,
    author   TEXT NOT NULL,
    before   TEXT NOT NULL,
    after    TEXT NOT NULL,
    ts       INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ops_board ON ops(board_id, id);
`
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, err
	}
	// Migrate pre-M4 tokens tables that predate the subject/admin columns.
	// SQLite has no "ADD COLUMN IF NOT EXISTS"; a duplicate-column error is
	// expected and benign on an already-migrated db.
	for _, alter := range []string{
		`ALTER TABLE tokens ADD COLUMN subject TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE tokens ADD COLUMN admin INTEGER NOT NULL DEFAULT 0`,
	} {
		_, _ = db.Exec(alter)
	}
	return &Store{db: db, locks: map[string]*sync.Mutex{}, subs: map[string][]chan int64{}}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// lockFor returns the per-board write serializer, creating it on first use.
func (s *Store) lockFor(id string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.locks[id]
	if l == nil {
		l = &sync.Mutex{}
		s.locks[id] = l
	}
	return l
}

// ---- boards ----

// CreateBoard inserts a new board with the given markdown content (ids are
// assigned on the way in so the first read is already stable). It errors if the
// id already exists.
func (s *Store) CreateBoard(id, content string) error {
	if id == "" {
		return errors.New("board id required")
	}
	b := board.Parse(content)
	b.EnsureIDs()
	now := time.Now().UnixNano()
	_, err := s.db.Exec(
		`INSERT INTO boards(id, content, version, created, updated) VALUES(?,?,?,?,?)`,
		id, b.Render(), 1, now, now)
	return err
}

// BoardExists reports whether a board with id is stored.
func (s *Store) BoardExists(id string) bool {
	var n int
	_ = s.db.QueryRow(`SELECT 1 FROM boards WHERE id=?`, id).Scan(&n)
	return n == 1
}

// Get returns the stored markdown content and version for a board.
func (s *Store) Get(id string) (content string, version int, err error) {
	err = s.db.QueryRow(`SELECT content, version FROM boards WHERE id=?`, id).Scan(&content, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, ErrNoBoard
	}
	return content, version, err
}

// ListBoards returns every board id, oldest first.
func (s *Store) ListBoards() ([]string, error) {
	rows, err := s.db.Query(`SELECT id FROM boards ORDER BY created`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// SetContent overwrites a board's whole markdown blob (used by remote push /
// pull import). Creates the board if absent. Bumps the version and journals.
func (s *Store) SetContent(id, content, author string) (version int, err error) {
	l := s.lockFor(id)
	l.Lock()
	defer l.Unlock()

	before, cur, gerr := s.readLocked(id)
	b := board.Parse(content)
	b.EnsureIDs()
	rendered := b.Render()
	now := time.Now().UnixNano()
	if errors.Is(gerr, ErrNoBoard) {
		if _, err := s.db.Exec(
			`INSERT INTO boards(id, content, version, created, updated) VALUES(?,?,?,?,?)`,
			id, rendered, 1, now, now); err != nil {
			return 0, err
		}
		s.journal(id, 1, author, "", rendered)
		s.notify(id, 1)
		return 1, nil
	}
	if gerr != nil {
		return 0, gerr
	}
	version = cur + 1
	if _, err := s.db.Exec(`UPDATE boards SET content=?, version=?, updated=? WHERE id=?`,
		rendered, version, now, id); err != nil {
		return 0, err
	}
	s.journal(id, version, author, before, rendered)
	s.notify(id, version)
	return version, nil
}

// readLocked reads a board's content+version; caller holds the board lock.
func (s *Store) readLocked(id string) (content string, version int, err error) {
	err = s.db.QueryRow(`SELECT content, version FROM boards WHERE id=?`, id).Scan(&content, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, ErrNoBoard
	}
	return content, version, err
}

// ApplyResult reports the outcome of ApplyOp.
type ApplyResult struct {
	Note    string
	NodeID  string
	Version int
}

// ApplyOp serializes an op against a board: read-modify-write under the
// per-board lock through board.Apply + EnsureIDs, bumping the version and
// journaling the change. If base >= 0 and the stored version differs, it refuses
// with ErrConflict (the client refetches and reapplies). The author is recorded
// in the journal.
func (s *Store) ApplyOp(id string, op board.Op, base int, author string) (ApplyResult, error) {
	l := s.lockFor(id)
	l.Lock()
	defer l.Unlock()

	before, cur, err := s.readLocked(id)
	if err != nil {
		return ApplyResult{}, err
	}
	if base >= 0 && base != cur {
		return ApplyResult{Version: cur}, ErrConflict
	}
	b := board.Parse(before)
	note, aerr := board.Apply(b, op)
	if aerr != nil {
		return ApplyResult{}, aerr
	}
	b.EnsureIDs()
	rendered := b.Render()
	version := cur + 1
	now := time.Now().UnixNano()
	if _, err := s.db.Exec(`UPDATE boards SET content=?, version=?, updated=? WHERE id=?`,
		rendered, version, now, id); err != nil {
		return ApplyResult{}, err
	}
	s.journal(id, version, author, before, rendered)
	s.notify(id, version)
	return ApplyResult{Note: note, NodeID: op.ID, Version: version}, nil
}

// ---- history / journal ----

func (s *Store) journal(id string, version int, author, before, after string) {
	if author == "" {
		author = "remote"
	}
	_, _ = s.db.Exec(`INSERT INTO ops(board_id, version, author, before, after, ts) VALUES(?,?,?,?,?,?)`,
		id, version, author, before, after, time.Now().UnixNano())
}

// HistoryEntry is one journaled write.
type HistoryEntry struct {
	Version int
	Author  string
	Before  string
	After   string
	TS      time.Time
}

// History returns a board's journaled writes, oldest first.
func (s *Store) History(id string) ([]HistoryEntry, error) {
	rows, err := s.db.Query(`SELECT version, author, before, after, ts FROM ops WHERE board_id=? ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HistoryEntry
	for rows.Next() {
		var e HistoryEntry
		var ts int64
		if err := rows.Scan(&e.Version, &e.Author, &e.Before, &e.After, &ts); err != nil {
			return nil, err
		}
		e.TS = time.Unix(0, ts)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- version-bump pub/sub (SSE) ----

// Subscribe returns a channel that receives the new version on every write to
// the board, plus a cancel func. Buffered and coalescing-friendly: a slow
// consumer simply misses intermediate versions, never blocks a writer.
func (s *Store) Subscribe(id string) (<-chan int64, func()) {
	ch := make(chan int64, 1)
	s.mu.Lock()
	s.subs[id] = append(s.subs[id], ch)
	s.mu.Unlock()
	cancel := func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		cur := s.subs[id]
		for i, c := range cur {
			if c == ch {
				s.subs[id] = append(cur[:i], cur[i+1:]...)
				break
			}
		}
	}
	return ch, cancel
}

func (s *Store) notify(id string, version int) {
	s.mu.Lock()
	subs := append([]chan int64(nil), s.subs[id]...)
	s.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- int64(version):
		default:
			// consumer busy; it will refetch on its next wake — never block.
		}
	}
}

// ---- tokens ----

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateToken mints an admin bootstrap token (subject = name). This is the
// operator's own key: `session-notes server token create --name <n>`. Scoped,
// non-admin identities are minted through CreateTokenAs (used by the grant
// endpoint's --new-token path).
func (s *Store) CreateToken(name string) (string, error) {
	return s.CreateTokenAs(name, name, true)
}

// CreateTokenAs mints a bearer token bound to subject with the given admin flag,
// stores only its hash, and returns the plaintext token once (never recoverable
// afterward). A non-admin token with no grants can reach nothing.
func (s *Store) CreateTokenAs(name, subject string, admin bool) (string, error) {
	if subject == "" {
		subject = name
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := hex.EncodeToString(buf)
	a := 0
	if admin {
		a = 1
	}
	if _, err := s.db.Exec(`INSERT INTO tokens(token_hash, name, subject, admin, created) VALUES(?,?,?,?,?)`,
		hashToken(token), name, subject, a, time.Now().UnixNano()); err != nil {
		return "", err
	}
	return token, nil
}

// Identity is the resolved principal behind a bearer token.
type Identity struct {
	Subject string
	Admin   bool
}

// ValidToken reports whether token matches a stored token hash (constant-time
// per candidate row).
func (s *Store) ValidToken(token string) bool {
	_, ok := s.TokenIdentity(token)
	return ok
}

// TokenIdentity resolves a bearer token to its (subject, admin) identity,
// comparing constant-time per candidate row. ok is false for an unknown token.
func (s *Store) TokenIdentity(token string) (Identity, bool) {
	if token == "" {
		return Identity{}, false
	}
	want := hashToken(token)
	rows, err := s.db.Query(`SELECT token_hash, subject, admin FROM tokens`)
	if err != nil {
		return Identity{}, false
	}
	defer rows.Close()
	var found Identity
	ok := false
	for rows.Next() {
		var h, subject string
		var admin int
		if err := rows.Scan(&h, &subject, &admin); err != nil {
			return Identity{}, false
		}
		if subtle.ConstantTimeCompare([]byte(h), []byte(want)) == 1 {
			found = Identity{Subject: subject, Admin: admin != 0}
			ok = true
		}
	}
	return found, ok
}

// SubjectForTokenName returns the subject bound to the most recent token minted
// under name — used so `remote grant --token-name <n>` can target an existing
// token's identity.
func (s *Store) SubjectForTokenName(name string) (string, bool) {
	var subject string
	err := s.db.QueryRow(`SELECT subject FROM tokens WHERE name=? ORDER BY created DESC LIMIT 1`, name).Scan(&subject)
	if err != nil {
		return "", false
	}
	return subject, true
}

// ---- grants ----

// Grant is one access-control row: subject may act on board_id (optionally
// confined to the subtree rooted at Root) with perms read|write|admin.
type Grant struct {
	Subject string `json:"subject"`
	BoardID string `json:"board_id"`
	Root    string `json:"root,omitempty"`
	Perm    string `json:"perm"`
}

// AddGrant upserts a grant for (subject, board). A subject holds at most one
// grant per board, so re-granting updates root/perm in place.
func (s *Store) AddGrant(subject, boardID, root, perm string) error {
	if subject == "" || boardID == "" {
		return errors.New("subject and board required")
	}
	switch perm {
	case "read", "write", "admin":
	default:
		return fmt.Errorf("unknown perm %q", perm)
	}
	_, err := s.db.Exec(
		`INSERT INTO grants(subject, board_id, root_node_id, perms, created) VALUES(?,?,?,?,?)
         ON CONFLICT(subject, board_id) DO UPDATE SET root_node_id=excluded.root_node_id, perms=excluded.perms`,
		subject, boardID, root, perm, time.Now().UnixNano())
	return err
}

// RemoveGrant deletes a subject's grant on a board. Missing is not an error.
func (s *Store) RemoveGrant(subject, boardID string) error {
	_, err := s.db.Exec(`DELETE FROM grants WHERE subject=? AND board_id=?`, subject, boardID)
	return err
}

// GrantFor returns the subject's grant on a board, if any.
func (s *Store) GrantFor(subject, boardID string) (Grant, bool) {
	g := Grant{Subject: subject, BoardID: boardID}
	err := s.db.QueryRow(`SELECT root_node_id, perms FROM grants WHERE subject=? AND board_id=?`,
		subject, boardID).Scan(&g.Root, &g.Perm)
	if err != nil {
		return Grant{}, false
	}
	return g, true
}

// ListGrants returns every grant on a board, oldest first.
func (s *Store) ListGrants(boardID string) ([]Grant, error) {
	rows, err := s.db.Query(`SELECT subject, root_node_id, perms FROM grants WHERE board_id=? ORDER BY created`, boardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Grant
	for rows.Next() {
		g := Grant{BoardID: boardID}
		if err := rows.Scan(&g.Subject, &g.Root, &g.Perm); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// HasTokens reports whether any token is registered (a fresh db has none, so
// --insecure or `token create` is required before the API is reachable).
func (s *Store) HasTokens() bool {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(1) FROM tokens`).Scan(&n)
	return n > 0
}
