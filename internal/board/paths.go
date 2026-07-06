package board

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// BoardsDir returns ~/.claude/boards (overridable for tests via
// SESSION_NOTES_DIR).
func BoardsDir() string {
	if d := os.Getenv("SESSION_NOTES_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".claude", "boards")
}

// PanesDir returns the pane-mapping directory.
func PanesDir() string { return filepath.Join(BoardsDir(), "panes") }

// StateDir returns the hook state directory (.last-seen files).
func StateDir() string { return filepath.Join(BoardsDir(), ".state") }

// BoardPath returns the board file path for a session id.
func BoardPath(sessionID string) string {
	return filepath.Join(BoardsDir(), sessionID+".md")
}

// PaneMapping is panes/<pane-id>.json.
type PaneMapping struct {
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
	Board     string `json:"board"`
	Started   string `json:"started"`
}

// PaneMappingPath returns the mapping file path for a tmux pane id (e.g. "%5").
func PaneMappingPath(paneID string) string {
	return filepath.Join(PanesDir(), paneID+".json")
}

// ReadPaneMapping loads panes/<pane-id>.json.
func ReadPaneMapping(paneID string) (*PaneMapping, error) {
	data, err := os.ReadFile(PaneMappingPath(paneID))
	if err != nil {
		return nil, err
	}
	var m PaneMapping
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// WritePaneMapping writes panes/<pane-id>.json atomically.
func WritePaneMapping(paneID string, m *PaneMapping) error {
	if err := os.MkdirAll(PanesDir(), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	path := PaneMappingPath(paneID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Template returns the initial markdown for a fresh board.
func Template(sessionID, cwd string, started time.Time) string {
	return fmt.Sprintf(`---
session: %s
cwd: %s
started: %s
---

## Plan

## Threads

## Questions

## Ideas

## Log
- %02d:%02d start: session started in %s
`, sessionID, cwd, started.Format(time.RFC3339), started.Hour(), started.Minute(), cwd)
}
