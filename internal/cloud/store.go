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
	"strings"
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
	memory := path == ":memory:" || strings.Contains(path, ":memory:") || strings.Contains(path, "mode=memory")
	dsn := path
	if !memory {
		// _txlock=immediate makes every write tx take the WAL write lock up front,
		// so two writers to different boards contend deterministically (waiting out
		// busy_timeout) instead of one hitting SQLITE_BUSY mid-transaction after a
		// deferred read. Writes still serialize per board via the lock map.
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		// Pragmas ride in the DSN so EVERY pooled connection applies them (an
		// Exec after Open only touches one connection of the pool). busy_timeout
		// makes readers/writers wait out a held write lock instead of erroring.
		dsn = path + sep + "_txlock=immediate" +
			"&_pragma=busy_timeout(5000)" +
			"&_pragma=journal_mode(WAL)" +
			"&_pragma=foreign_keys(ON)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if memory {
		// modernc.org/sqlite is a single connection-pool; a memory db must not be
		// closed/reopened between statements, so cap to one open connection which
		// also keeps WAL writes serialized at the driver level. Each connection to
		// ":memory:" is a *separate* database, so a pool would fracture the data.
		db.SetMaxOpenConns(1)
	} else {
		// WAL supports one writer + many concurrent readers. A small read pool lets
		// /history (and /healthz, and every other read) run alongside a write
		// instead of queuing behind the single connection — the M8 head-of-line
		// blocking fix. Writers still serialize (per-board lock + immediate txlock).
		db.SetMaxOpenConns(8)
	}
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
    before   TEXT NOT NULL DEFAULT '',
    after    TEXT NOT NULL DEFAULT '',
    added    TEXT NOT NULL DEFAULT '',
    removed  TEXT NOT NULL DEFAULT '',
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
		// M9: the ops journal moved from full before/after snapshots to stored
		// line-diffs. Old dbs lack the added/removed columns; add them (benign
		// duplicate error on an already-migrated db).
		`ALTER TABLE ops ADD COLUMN added TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE ops ADD COLUMN removed TEXT NOT NULL DEFAULT ''`,
	} {
		_, _ = db.Exec(alter)
	}
	s := &Store{db: db, locks: map[string]*sync.Mutex{}, subs: map[string][]chan int64{}}
	// M9: convert any fat full-snapshot journal rows (pre-M9) to line-diffs and
	// prune each board to journalKeep rows. Guarded by user_version so it runs at
	// most once per db; a fresh db already writes the diff format.
	if err := s.migrateJournal(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// journalKeep caps how many ops rows are retained per board — the /history
// window. Diffs are small, so this bounds both disk and the unbounded row
// growth the M8 soak surfaced (208k rows for 154KB of live board).
const journalKeep = 500

// pruneEvery amortizes journal pruning: writers prune once every pruneEvery
// versions rather than on each write, so at most journalKeep+pruneEvery rows
// accumulate per board between prunes.
const pruneEvery = 64

// migrateJournal is the one-time (per db) M9 conversion of the ops journal from
// full before/after snapshots to stored line-diffs, plus a prune to journalKeep
// rows per board. It is idempotent via PRAGMA user_version: a fresh db and an
// already-migrated db skip the work.
func (s *Store) migrateJournal() error {
	var uv int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&uv); err != nil {
		return err
	}
	if uv >= journalSchemaVersion {
		return nil
	}
	// Convert legacy rows (those still carrying a full snapshot) to diffs.
	rows, err := s.db.Query(`SELECT id, before, after FROM ops WHERE before<>'' OR after<>''`)
	if err != nil {
		return err
	}
	type conv struct {
		id             int64
		added, removed string
	}
	var todo []conv
	for rows.Next() {
		var id int64
		var before, after string
		if err := rows.Scan(&id, &before, &after); err != nil {
			rows.Close()
			return err
		}
		added, removed := board.DiffLines(before, after)
		todo = append(todo, conv{id: id, added: joinLines(added), removed: joinLines(removed)})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	for _, c := range todo {
		if _, err := tx.Exec(`UPDATE ops SET added=?, removed=?, before='', after='' WHERE id=?`,
			c.added, c.removed, c.id); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// Prune every board down to journalKeep rows.
	boardIDs, err := s.opsBoardIDs()
	if err != nil {
		return err
	}
	for _, id := range boardIDs {
		if _, err := s.db.Exec(pruneSQL, id, id, journalKeep); err != nil {
			return err
		}
	}
	if _, err := s.db.Exec(fmt.Sprintf(`PRAGMA user_version=%d`, journalSchemaVersion)); err != nil {
		return err
	}
	// Reclaim the space the fat snapshots occupied.
	_, _ = s.db.Exec(`VACUUM`)
	return nil
}

// journalSchemaVersion marks the diff-journal format in PRAGMA user_version.
const journalSchemaVersion = 1

// pruneSQL keeps only the journalKeep newest ops rows for one board.
const pruneSQL = `DELETE FROM ops WHERE board_id=? AND id NOT IN (
    SELECT id FROM ops WHERE board_id=? ORDER BY id DESC LIMIT ?)`

// opsBoardIDs returns the distinct board ids present in the ops journal.
func (s *Store) opsBoardIDs() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT board_id FROM ops`)
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

// joinLines encodes a diff line list for storage. Diff lines are never blank
// (DiffLines skips whitespace-only lines) so a newline join round-trips exactly;
// the empty string encodes "no lines".
func joinLines(lines []string) string { return strings.Join(lines, "\n") }

// splitLines decodes a stored diff line list. Empty string decodes to nil.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
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
		if err := s.txWrite(func(tx *sql.Tx) error {
			if _, err := tx.Exec(
				`INSERT INTO boards(id, content, version, created, updated) VALUES(?,?,?,?,?)`,
				id, rendered, 1, now, now); err != nil {
				return err
			}
			return journalTx(tx, id, 1, author, "", rendered)
		}); err != nil {
			return 0, err
		}
		s.notify(id, 1)
		return 1, nil
	}
	if gerr != nil {
		return 0, gerr
	}
	version = cur + 1
	if err := s.writeAndJournal(id, rendered, version, now, author, before); err != nil {
		return 0, err
	}
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
	if err := s.writeAndJournal(id, rendered, version, now, author, before); err != nil {
		return ApplyResult{}, err
	}
	s.notify(id, version)
	return ApplyResult{Note: note, NodeID: op.ID, Version: version}, nil
}

// ---- history / journal ----

// txWrite runs fn inside a single transaction, committing on success and
// rolling back on any error. It keeps a version bump and its journal row atomic:
// either both land or neither does. Callers already hold the per-board lock, so
// the tx never races another writer for the same board.
func (s *Store) txWrite(fn func(tx *sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// writeAndJournal atomically bumps an existing board's content/version and
// appends the matching ops journal row in one transaction.
func (s *Store) writeAndJournal(id, rendered string, version int, now int64, author, before string) error {
	return s.txWrite(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE boards SET content=?, version=?, updated=? WHERE id=?`,
			rendered, version, now, id); err != nil {
			return err
		}
		return journalTx(tx, id, version, author, before, rendered)
	})
}

// journalTx appends one ops journal row using the given transaction. It stores
// the edit as a line-diff (added/removed) rather than full before/after
// snapshots — the M9 fix for the O(ops × board_size) journal bloat the M8 soak
// found (943 MB journal for 154 KB of live board). /history renders exactly
// these line-diffs, so no fidelity is lost. Every pruneEvery-th version it also
// prunes the board's journal to journalKeep rows.
func journalTx(tx *sql.Tx, id string, version int, author, before, after string) error {
	if author == "" {
		author = "remote"
	}
	added, removed := board.DiffLines(before, after)
	if _, err := tx.Exec(`INSERT INTO ops(board_id, version, author, added, removed, ts) VALUES(?,?,?,?,?,?)`,
		id, version, author, joinLines(added), joinLines(removed), time.Now().UnixNano()); err != nil {
		return err
	}
	if version%pruneEvery == 0 {
		if _, err := tx.Exec(pruneSQL, id, id, journalKeep); err != nil {
			return err
		}
	}
	return nil
}

// HistoryEntry is one journaled write, stored as a line-diff.
type HistoryEntry struct {
	ID      int64
	Version int
	Author  string
	Added   []string
	Removed []string
	TS      time.Time
}

// History returns a board's journaled writes, newest first, capped at limit
// (limit<=0 = journalKeep). When before>0 only rows with a smaller op id are
// returned — the pagination cursor, so /history can page backward without
// scanning the whole journal.
func (s *Store) History(id string, limit, before int) ([]HistoryEntry, error) {
	if limit <= 0 {
		limit = journalKeep
	}
	query := `SELECT id, version, author, added, removed, ts FROM ops WHERE board_id=?`
	args := []any{id}
	if before > 0 {
		query += ` AND id<?`
		args = append(args, before)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HistoryEntry
	for rows.Next() {
		var e HistoryEntry
		var added, removed string
		var ts int64
		if err := rows.Scan(&e.ID, &e.Version, &e.Author, &added, &removed, &ts); err != nil {
			return nil, err
		}
		e.Added = splitLines(added)
		e.Removed = splitLines(removed)
		e.TS = time.Unix(0, ts)
		out = append(out, e)
	}
	return out, rows.Err()
}

// AuthorsSince returns the authors of every journalled op with version greater
// than sinceVersion, oldest first. It feeds the SSE ?ignoreAuthor filter: when
// every author of the ops that produced a change equals the ignored name, the
// change is the watcher's own and no event is delivered.
func (s *Store) AuthorsSince(id string, sinceVersion int) ([]string, error) {
	rows, err := s.db.Query(`SELECT author FROM ops WHERE board_id=? AND version>? ORDER BY version`, id, sinceVersion)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		out = append(out, a)
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

// TokenInfo is a token's metadata (never the secret or its hash): what `server
// token list` shows so an operator can audit and revoke by name.
type TokenInfo struct {
	Name    string
	Subject string
	Admin   bool
	Created time.Time
}

// ListTokens returns every registered token's metadata, oldest first. The token
// secret is never stored (only its SHA-256 hash) so it cannot be listed — this
// is purely for auditing and choosing what to revoke.
func (s *Store) ListTokens() ([]TokenInfo, error) {
	rows, err := s.db.Query(`SELECT name, subject, admin, created FROM tokens ORDER BY created`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TokenInfo
	for rows.Next() {
		var t TokenInfo
		var admin int
		var created int64
		if err := rows.Scan(&t.Name, &t.Subject, &admin, &created); err != nil {
			return nil, err
		}
		t.Admin = admin != 0
		t.Created = time.Unix(0, created)
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeToken deletes every token registered under name and returns how many
// rows were removed (0 = no such token). Revocation is immediate: the next
// request bearing a revoked secret fails the ValidToken full-table scan and 401s.
func (s *Store) RevokeToken(name string) (int, error) {
	res, err := s.db.Exec(`DELETE FROM tokens WHERE name=?`, name)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
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
