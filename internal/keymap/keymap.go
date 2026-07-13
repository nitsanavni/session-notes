// Package keymap is the single source of truth for session-notes keybindings.
//
// Every help surface — the TUI footer hint line, the TUI `?` overlay, the web
// help overlay, and the README keys table — is generated from the tables here.
// Nothing else should hand-maintain a copy of the keymap. The actual key
// handlers (internal/tui/*.go dispatch, board.html key handlers) stay
// hand-written; this package documents them and a drift test asserts every
// key listed here is actually handled.
//
// To change help text or add a key: edit ONLY this file (and wire the handler),
// then run `go generate ./...` to refresh README.md.
package keymap

import (
	"fmt"
	"strings"
)

// Binding is one documented key (or key pair). Keys is the display form; Short
// is the terse footer label (empty omits it from footer lines); Long is the
// descriptive help text used by the overlays and the README. Group buckets
// related keys (nav/fold/edit/search/novelty/meta). TUI/Web say which frontends
// bind it; TUIFooter/WebFooter opt it into each (curated) footer hint line.
type Binding struct {
	Keys      string
	Short     string
	Long      string
	Group     string
	TUI       bool
	Web       bool
	TUIFooter bool
	WebFooter bool
}

// Outline holds the bindings active in the outline (list) view — the primary
// view. Order is the render order for the `?` overlay and the README table.
var Outline = []Binding{
	{Keys: "j / k, ↓ / ↑", Short: "j/k/arrows move", Long: "move cursor", Group: "nav", TUI: true, Web: true, TUIFooter: true, WebFooter: true},
	{Keys: "tab / shift-tab", Short: "tab section", Long: "next / previous section", Group: "nav", TUI: true, Web: true, TUIFooter: true, WebFooter: true},
	{Keys: "1 … 9", Short: "1-9 jump", Long: "jump to the Nth section", Group: "nav", TUI: true, Web: true, TUIFooter: true},
	{Keys: "g / G", Short: "g/G top/bottom", Long: "jump to first / last visible stop", Group: "nav", TUI: true, Web: true, TUIFooter: true},
	{Keys: "/", Short: "/ search", Long: "incremental search: a ranked fuzzy results panel opens as you type (the board doesn't move); ↑/↓ (ctrl+p/ctrl+n) pick a row, enter jumps to it and closes search; matches highlighted while live", Group: "search", TUI: true, Web: true, TUIFooter: true, WebFooter: true},
	{Keys: "n / N", Short: "n/N next/prev", Long: "next / previous match (document order, wrapping); with no active search, re-runs the last term from where you left off", Group: "search", TUI: true, Web: true, TUIFooter: true},
	{Keys: "tab (in search)", Long: "toggle search scope: working set (no Archive/Log) ↔ everything", Group: "search", TUI: true, Web: true},
	{Keys: "a", Short: "a add", Long: "add item to the current section", Group: "edit", TUI: true, Web: true, TUIFooter: true, WebFooter: true},
	{Keys: "A", Short: "A section", Long: "add sections (multi-select overlay)", Group: "edit", TUI: true, Web: true, TUIFooter: true, WebFooter: true},
	{Keys: "R", Short: "R reply", Long: "reply in thread (sibling on a reply; starts the thread on an item)", Group: "edit", TUI: true, Web: true, TUIFooter: true, WebFooter: true},
	{Keys: "F", Short: "F fork", Long: "fork a sub-thread under the message at the cursor", Group: "edit", TUI: true, Web: true, TUIFooter: true, WebFooter: true},
	{Keys: "space", Short: "space status", Long: "cycle status [ ] → [>] → [x]", Group: "edit", TUI: true, Web: true, TUIFooter: true, WebFooter: true},
	{Keys: "b", Short: "b blocked", Long: "toggle blocked [?]", Group: "edit", TUI: true, Web: true, TUIFooter: true},
	{Keys: "!", Short: "! urgent", Long: "toggle urgent (!!)", Group: "edit", TUI: true, Web: true, TUIFooter: true, WebFooter: true},
	{Keys: "p", Short: "p pin", Long: "toggle pin (!pin) — re-injected into Claude's context on a cadence", Group: "edit", TUI: true, Web: true, TUIFooter: true, WebFooter: true},
	{Keys: "d", Short: "d archive", Long: "archive item (or whole section) into ## Archive", Group: "edit", TUI: true, Web: true, TUIFooter: true, WebFooter: true},
	{Keys: "D", Short: "D delete", Long: "hard-delete item (or whole section) from the file", Group: "edit", TUI: true, Web: true, TUIFooter: true},
	{Keys: "enter", Short: "enter fold", Long: "fold toggle: on a section header collapse/expand it; on an item with children fold/unfold its subtree", Group: "fold", TUI: true, Web: true, TUIFooter: true, WebFooter: true},
	{Keys: "l / →", Short: "l expand/descend", Long: "on a section: expand and land on the first (or last-visited) item; on an item: unfold if folded, else descend to the first child", Group: "fold", TUI: true, Web: true, TUIFooter: true},
	{Keys: "h / ←", Short: "h ascend", Long: "on a section: collapse it; on an item: step out to the parent (top-level item → section header) — never folds", Group: "fold", TUI: true, Web: true, TUIFooter: true},
	{Keys: "z", Short: "z zoom", Long: "focus-fold (zoom): collapse everything except the path to the cursor; z again restores", Group: "fold", TUI: true, Web: true, TUIFooter: true, WebFooter: true},
	{Keys: "f", Short: "f re-root", Long: "re-root the outline on the selected node (true zoom): renders it as root with a breadcrumb; esc or a breadcrumb steps back out. Shares the zoom root with the map view and the #node-id URL (web only)", Group: "fold", Web: true},
	{Keys: "w", Short: "w wrap", Long: "wrap the item at the cursor (toggle single truncated line ↔ full multi-line block, session-only)", Group: "fold", TUI: true, Web: true, TUIFooter: true, WebFooter: true},
	{Keys: "e", Short: "e edit", Long: "edit item inline (the bullet line only); on a section header, rename it", Group: "edit", TUI: true, Web: true, TUIFooter: true, WebFooter: true},
	{Keys: "E", Short: "E editor", Long: "open the whole board in $EDITOR / raw markdown (3-way merged on save)", Group: "edit", TUI: true, Web: true, TUIFooter: true, WebFooter: true},
	{Keys: "T", Short: "T title", Long: "edit the board title (frontmatter title:; empty clears it)", Group: "edit", TUI: true, Web: true, TUIFooter: true, WebFooter: true},
	{Keys: "o", Short: "o open link", Long: "open the item's [[linked note]] in $EDITOR (chooser if it has several)", Group: "edit", TUI: true, Web: true, TUIFooter: true, WebFooter: true},
	{Keys: "O", Short: "O copy link path", Long: "copy the absolute path of the item's [[linked note]] to the clipboard (to open it in another editor); creates the note if missing", Group: "edit", TUI: true, Web: true, TUIFooter: true},
	{Keys: "L", Short: "L log", Long: "quick log entry", Group: "edit", TUI: true, Web: true, TUIFooter: true},
	{Keys: "y", Short: "y copy path", Long: "copy the board file path to the clipboard (shown in status)", Group: "meta", TUI: true, Web: true, TUIFooter: true},
	{Keys: "m", Short: "m map", Long: "toggle the mindmap view", Group: "meta", TUI: true, Web: true, TUIFooter: true, WebFooter: true},
	{Keys: "s", Short: "s share", Long: "share this node: mint a scoped attach link + browser URL for the selected node (cloud server, admin token only; hidden otherwise)", Group: "meta", Web: true},
	{Keys: "W", Short: "W width", Long: "cycle the outline width — narrow / wide / full (web only; drag a side grip for a custom width)", Group: "meta", Web: true, WebFooter: true},
	{Keys: "u", Short: "u undo", Long: "undo last change (shared journal timeline — CLI edits too)", Group: "meta", TUI: true, Web: true, TUIFooter: true, WebFooter: true},
	{Keys: "ctrl+r", Short: "ctrl+r redo", Long: "redo", Group: "meta", TUI: true, Web: true, TUIFooter: true},
	{Keys: "H", Short: "H history", Long: "history: shared edit journal (who changed what), read-only", Group: "novelty", TUI: true, Web: true, TUIFooter: true},
	{Keys: "V", Short: "V recents", Long: "recent changes: out-of-band edits newest-first; enter jumps to the change", Group: "novelty", TUI: true, Web: true, TUIFooter: true},
	{Keys: "r", Short: "r reload", Long: "reload from disk (TUI only; the web view live-updates)", Group: "meta", TUI: true, TUIFooter: true},
	{Keys: "U", Short: "U restart", Long: "restart in place when the binary was updated on disk (TUI only; the footer nags when a fresh deploy is detected)", Group: "meta", TUI: true},
	{Keys: "B", Short: "B boards", Long: "back to the board picker", Group: "meta", TUI: true, Web: true, TUIFooter: true, WebFooter: true},
	{Keys: "?", Short: "? help", Long: "toggle this help", Group: "meta", TUI: true, Web: true, TUIFooter: true, WebFooter: true},
	{Keys: "q / esc", Short: "q quit", Long: "quit (esc; ctrl+c always quits)", Group: "meta", TUI: true, TUIFooter: true},
}

// Map holds the bindings active in the mindmap view. Some mirror the outline
// (undo, search, history); several are map-specific (f/esc focus, M log column).
var Map = []Binding{
	{Keys: "h / j / k / l, arrows", Long: "move focus (left/down/up/right through the map)", Group: "nav", TUI: true, Web: true},
	{Keys: "g / G", Long: "first / last sibling", Group: "nav", TUI: true, Web: true},
	{Keys: "tab / shift-tab", Long: "next / previous section", Group: "nav", TUI: true, Web: true},
	{Keys: "1 … 9", Long: "jump to the Nth section", Group: "nav", TUI: true, Web: true},
	{Keys: "enter", Long: "cycle fold: collapsed → default → replies-shown → collapsed", Group: "fold", TUI: true, Web: true},
	{Keys: "f", Long: "focus into the subtree (breadcrumbs)", Group: "fold", TUI: true, Web: true},
	{Keys: "esc", Long: "focus out one level (unfocused map: leave the view)", Group: "fold", TUI: true, Web: true},
	{Keys: "z", Long: "focus-fold (zoom): collapse everything except the path to the focus; z again restores", Group: "fold", TUI: true, Web: true},
	{Keys: "w", Long: "expand the focused node (toggle)", Group: "fold", TUI: true, Web: true},
	{Keys: "e", Long: "edit item · rename section · edit board title (on the center)", Group: "edit", TUI: true, Web: true},
	{Keys: "a", Long: "add child · add a section (on the center)", Group: "edit", TUI: true, Web: true},
	{Keys: "A", Long: "add a sibling", Group: "edit", TUI: true, Web: true},
	{Keys: "space", Long: "cycle status (5-state: none → open → [>] → [x] → [?] → none)", Group: "edit", TUI: true, Web: true},
	{Keys: "b", Long: "toggle blocked [?] on the focused item", Group: "edit", TUI: true, Web: true},
	{Keys: "!", Long: "toggle urgent (!!) on the focused item", Group: "edit", TUI: true, Web: true},
	{Keys: "p", Long: "toggle pin (!pin) on the focused item", Group: "edit", TUI: true, Web: true},
	{Keys: "d", Long: "archive the focused item or whole section into ## Archive", Group: "edit", TUI: true, Web: true},
	{Keys: "D", Long: "hard-delete the focused item or whole section", Group: "edit", TUI: true, Web: true},
	{Keys: "o", Long: "open the focused item's [[linked note]] (chooser if several)", Group: "edit", TUI: true, Web: true},
	{Keys: "O", Long: "copy the focused item's [[linked note]] absolute path to the clipboard", Group: "edit", TUI: true, Web: true},
	{Keys: "L", Long: "quick log entry", Group: "edit", TUI: true, Web: true},
	{Keys: "T", Long: "edit the board title", Group: "edit", TUI: true, Web: true},
	{Keys: "E", Long: "open the board in $EDITOR / raw markdown", Group: "edit", TUI: true, Web: true},
	{Keys: "M", Long: "toggle the Log column (hidden by default in the map)", Group: "meta", TUI: true, Web: true},
	{Keys: "s", Long: "share the focused node: mint a scoped attach link (cloud admin only)", Group: "meta", Web: true},
	{Keys: "/", Long: "incremental search (same as the outline)", Group: "search", TUI: true, Web: true},
	{Keys: "n / N", Long: "next / previous match", Group: "search", TUI: true, Web: true},
	{Keys: "H", Long: "history: shared edit journal, read-only", Group: "novelty", TUI: true, Web: true},
	{Keys: "V", Long: "recent-changes feed (enter jumps to the changed node)", Group: "novelty", TUI: true, Web: true},
	{Keys: "u / ctrl+r", Long: "undo / redo (shared journal)", Group: "meta", TUI: true, Web: true},
	{Keys: "y", Long: "copy the board file path", Group: "meta", TUI: true, Web: true},
	{Keys: "r", Long: "reload from disk (TUI only)", Group: "meta", TUI: true},
	{Keys: "U", Long: "restart in place when the binary was updated on disk (TUI only)", Group: "meta", TUI: true},
	{Keys: "S", Long: "surprise recorder: a nav move surprised you? note it (TUI dev only)", Group: "meta", TUI: true},
	{Keys: "B", Long: "back to the board picker (TUI)", Group: "meta", TUI: true},
	{Keys: "m / q", Long: "back to the outline", Group: "meta", TUI: true, Web: true},
	{Keys: "?", Long: "toggle this help", Group: "meta", TUI: true, Web: true},
}

// FooterHints renders the terse `·`-joined hint line for a footer. tui selects
// the TUI footer subset (and its Short labels); otherwise the web subset.
func FooterHints(tui bool) string {
	var parts []string
	for _, b := range Outline {
		if b.Short == "" {
			continue
		}
		if tui && b.TUIFooter {
			parts = append(parts, b.Short)
		} else if !tui && b.WebFooter {
			parts = append(parts, b.Short)
		}
	}
	return strings.Join(parts, " · ")
}

// TUIRows returns the [key, description] pairs for the TUI `?` overlay: the
// outline bindings, then a "Map view" separator, then the map bindings. A pair
// whose first element is "" is a section header (its second element is the
// header text).
func TUIRows() [][2]string {
	var rows [][2]string
	for _, b := range Outline {
		if b.TUI {
			rows = append(rows, [2]string{b.Keys, b.Long})
		}
	}
	rows = append(rows, [2]string{"", "Map view"})
	for _, b := range Map {
		if b.TUI {
			rows = append(rows, [2]string{b.Keys, b.Long})
		}
	}
	return rows
}

// WebPayload is the JSON shape served to the web help overlay: outline and map
// sections, each a list of {keys, desc} for the bindings that frontend binds.
type WebPayload struct {
	Outline []WebRow `json:"outline"`
	Map     []WebRow `json:"map"`
	Footer  string   `json:"footer"`
}

// WebRow is one help-overlay row for the web frontend.
type WebRow struct {
	Keys string `json:"keys"`
	Desc string `json:"desc"`
}

// Web builds the payload the web help overlay consumes (web-bound bindings only).
func Web() WebPayload {
	var p WebPayload
	for _, b := range Outline {
		if b.Web {
			p.Outline = append(p.Outline, WebRow{Keys: b.Keys, Desc: b.Long})
		}
	}
	for _, b := range Map {
		if b.Web {
			p.Map = append(p.Map, WebRow{Keys: b.Keys, Desc: b.Long})
		}
	}
	p.Footer = FooterHints(false)
	return p
}

// Markdown renders the README keybindings table (TUI outline view) between the
// generated markers. It is the body written by `session-notes docs keys
// --markdown` and asserted by the keymap drift test.
func Markdown() string {
	var b strings.Builder
	b.WriteString("| Key | Action |\n")
	b.WriteString("|-----|--------|\n")
	for _, bd := range Outline {
		if !bd.TUI {
			continue
		}
		fmt.Fprintf(&b, "| %s | %s |\n", mdKeys(bd.Keys), mdEscape(bd.Long))
	}
	return b.String()
}

// mdEscape escapes the pipe characters that would otherwise break a table cell.
func mdEscape(s string) string {
	return strings.ReplaceAll(s, "|", "\\|")
}

// mdKeys renders a Keys display string as backtick-wrapped tokens, keeping the
// " / " and ", " separators outside the code spans (e.g. "j / k, ↓ / ↑" →
// "`j` / `k`, `↓` / `↑`").
func mdKeys(s string) string {
	s = strings.ReplaceAll(s, " / ", "` / `")
	s = strings.ReplaceAll(s, ", ", "`, `")
	return "`" + s + "`"
}
