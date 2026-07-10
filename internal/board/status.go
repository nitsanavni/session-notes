package board

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// SessionStatus is the compact sidecar written to
// ~/.claude/boards/.state/<session>.status. It reflects the live Claude Code
// session behind a board: which model is running, how full the context window
// is, session cost, and the last activity kind. It is written by two
// independent sources — the statusline hook (model/context/cost) and the
// lifecycle hooks (activity) — so writes MERGE rather than overwrite (see
// UpdateStatus).
type SessionStatus struct {
	// Model is the model's human display name (e.g. "Opus").
	Model string `json:"model,omitempty"`
	// ContextPct is the percentage of the context window used (0–100), or a
	// negative value when unknown.
	ContextPct float64 `json:"context_pct"`
	// CostUSD is the estimated session cost in US dollars, when reported.
	CostUSD float64 `json:"cost_usd,omitempty"`
	// Activity is the last recorded lifecycle kind: "working", "idle", or
	// "ended".
	Activity string `json:"activity,omitempty"`
	// Updated is the RFC3339 timestamp of the last write.
	Updated string `json:"updated,omitempty"`
}

// StatusStaleAfter is how old a status sidecar may be before the UIs treat it
// as stale (session likely gone) and hide it.
const StatusStaleAfter = 5 * time.Minute

// StatusPath returns the status sidecar path for a session id.
func StatusPath(sessionID string) string {
	return filepath.Join(StateDir(), sessionID+".status")
}

// ReadStatus loads the status sidecar for a session id. ok is false when the
// file is missing or unparseable (callers then render no status).
func ReadStatus(sessionID string) (*SessionStatus, bool) {
	if sessionID == "" {
		return nil, false
	}
	data, err := os.ReadFile(StatusPath(sessionID))
	if err != nil {
		return nil, false
	}
	var s SessionStatus
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, false
	}
	return &s, true
}

// UpdateStatus reads the current status (or an empty one), applies mut, stamps
// Updated with the current time, and writes the sidecar back atomically. It
// never returns an error — a status write must never fail a hook — and merges
// with whatever the other writer left so the statusline and lifecycle hooks
// don't clobber each other's fields. When ContextPct is untouched it defaults
// to -1 (unknown) for a fresh record.
func UpdateStatus(sessionID string, mut func(*SessionStatus)) {
	if sessionID == "" {
		return
	}
	s, ok := ReadStatus(sessionID)
	if !ok {
		s = &SessionStatus{ContextPct: -1}
	}
	mut(s)
	s.Updated = time.Now().Format(time.RFC3339)

	data, err := json.Marshal(s)
	if err != nil {
		return
	}
	if err := os.MkdirAll(StateDir(), 0o755); err != nil {
		return
	}
	path := StatusPath(sessionID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

// Age returns how long ago the status was updated, or a very large duration
// when the timestamp is missing/unparseable (so it reads as stale).
func (s *SessionStatus) Age(now time.Time) time.Duration {
	t, err := time.Parse(time.RFC3339, s.Updated)
	if err != nil {
		return 1<<62 - 1
	}
	return now.Sub(t)
}

// Fresh reports whether the status is recent enough to display.
func (s *SessionStatus) Fresh(now time.Time) bool {
	return s.Age(now) < StatusStaleAfter && s.Age(now) >= -time.Minute
}

// HasContext reports whether a usable context percentage is present.
func (s *SessionStatus) HasContext() bool {
	return s.ContextPct >= 0
}
