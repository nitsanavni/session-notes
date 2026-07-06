#!/usr/bin/env bash
# Installer for session-notes.
#
# - Builds the session-notes binary and installs it to ~/.local/bin.
# - Creates the ~/.claude/boards state directories.
# - Merges the SessionStart / SessionEnd / UserPromptSubmit hooks into a
#   Claude Code settings.json (creating it if needed, backing it up first).
#   By default hooks go to ./.claude/settings.json (project scope — only
#   sessions in the current directory get boards). Pass --global to install
#   them in ~/.claude/settings.json for all sessions.
#
# Safe to re-run: every step is idempotent.
set -euo pipefail

SCOPE=project
for arg in "$@"; do
  case "$arg" in
    --global) SCOPE=global ;;
    *) echo "usage: install.sh [--global]" >&2; exit 1 ;;
  esac
done

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GO_BIN="$HOME/.local/go/bin/go"
INSTALL_BIN_DIR="$HOME/.local/bin"
BINARY_PATH="$INSTALL_BIN_DIR/session-notes"
CLAUDE_DIR="$HOME/.claude"
BOARDS_DIR="$CLAUDE_DIR/boards"
if [ "$SCOPE" = global ]; then
  SETTINGS_DIR="$CLAUDE_DIR"
else
  SETTINGS_DIR="$PWD/.claude"
fi
SETTINGS_FILE="$SETTINGS_DIR/settings.json"

log()  { printf '==> %s\n' "$*"; }
warn() { printf 'warning: %s\n' "$*" >&2; }

# --- 1. Build & install the binary ------------------------------------------

if [ ! -x "$GO_BIN" ]; then
  echo "error: go toolchain not found at $GO_BIN" >&2
  echo "       install Go there, or edit install.sh to point at your go binary." >&2
  exit 1
fi

mkdir -p "$INSTALL_BIN_DIR"

log "Building session-notes ($GO_BIN build -o $BINARY_PATH .)"
(
  cd "$REPO_DIR"
  PATH="$HOME/.local/go/bin:$PATH" "$GO_BIN" build -o "$BINARY_PATH" .
)
log "Installed binary to $BINARY_PATH"

case ":$PATH:" in
  *":$INSTALL_BIN_DIR:"*) ;;
  *)
    warn "$INSTALL_BIN_DIR is not on your PATH."
    warn "Add this to your shell profile: export PATH=\"$INSTALL_BIN_DIR:\$PATH\""
    ;;
esac

# --- 2. Board state directories ----------------------------------------------

mkdir -p "$BOARDS_DIR/panes" "$BOARDS_DIR/.state"
log "Created $BOARDS_DIR/{panes,.state}"

# --- 3. Merge hooks into settings.json ---------------------------------------

log "Hook scope: $SCOPE ($SETTINGS_FILE)"
mkdir -p "$SETTINGS_DIR"

HOOK_CMD_START="\$HOME/.local/bin/session-notes hook session-start"
HOOK_CMD_END="\$HOME/.local/bin/session-notes hook session-end"
HOOK_CMD_PROMPT="\$HOME/.local/bin/session-notes hook prompt-submit"

if [ -f "$SETTINGS_FILE" ]; then
  BACKUP_FILE="$SETTINGS_FILE.bak.$(date +%Y%m%d%H%M%S)"
  cp "$SETTINGS_FILE" "$BACKUP_FILE"
  log "Backed up existing settings to $BACKUP_FILE"
else
  log "No existing settings.json; creating a new one"
  echo '{}' > "$SETTINGS_FILE"
fi

merge_settings() {
  local settings_file="$1" start_cmd="$2" end_cmd="$3" prompt_cmd="$4"
  local tmp_file
  tmp_file="$(mktemp "${settings_file}.tmp.XXXXXX")"

  if command -v jq >/dev/null 2>&1; then
    jq \
      --arg start_cmd "$start_cmd" \
      --arg end_cmd "$end_cmd" \
      --arg prompt_cmd "$prompt_cmd" \
      '
      def ensure_hook(event; cmd):
        (.hooks[event] // []) as $arr
        | .hooks[event] =
          (if any($arr[]?.hooks[]?.command == cmd; .) then
             $arr
           else
             $arr + [{"hooks": [{"type": "command", "command": cmd}]}]
           end);
      ensure_hook("SessionStart"; $start_cmd)
      | ensure_hook("SessionEnd"; $end_cmd)
      | ensure_hook("UserPromptSubmit"; $prompt_cmd)
      ' "$settings_file" > "$tmp_file"
  else
    python3 - "$settings_file" "$tmp_file" "$start_cmd" "$end_cmd" "$prompt_cmd" <<'PYEOF'
import json
import sys

settings_file, tmp_file, start_cmd, end_cmd, prompt_cmd = sys.argv[1:6]

with open(settings_file) as f:
    data = json.load(f)

hooks = data.setdefault("hooks", {})

def ensure_hook(event, cmd):
    entries = hooks.setdefault(event, [])
    for entry in entries:
        for h in entry.get("hooks", []):
            if h.get("command") == cmd:
                return
    entries.append({"hooks": [{"type": "command", "command": cmd}]})

ensure_hook("SessionStart", start_cmd)
ensure_hook("SessionEnd", end_cmd)
ensure_hook("UserPromptSubmit", prompt_cmd)

with open(tmp_file, "w") as f:
    json.dump(data, f, indent=2)
    f.write("\n")
PYEOF
  fi

  mv "$tmp_file" "$settings_file"
}

if command -v jq >/dev/null 2>&1; then
  log "Merging hooks into $SETTINGS_FILE (jq)"
elif command -v python3 >/dev/null 2>&1; then
  log "Merging hooks into $SETTINGS_FILE (python3, jq not found)"
else
  echo "error: neither jq nor python3 is available to merge settings.json" >&2
  exit 1
fi

merge_settings "$SETTINGS_FILE" "$HOOK_CMD_START" "$HOOK_CMD_END" "$HOOK_CMD_PROMPT"
log "Hooks merged (idempotent — safe to re-run)"

# --- 4. Next steps -------------------------------------------------------

cat <<EOF

session-notes installed.

Add these to your ~/.tmux.conf — popup on prefix+g, persistent split pane on
prefix+G (right) / prefix+C-g (below), multi-session dashboard on prefix+D:

  bind-key g display-popup -E -w 80% -h 80% "session-notes --pane '#{pane_id}'"
  bind-key G split-window -h -l 40% "session-notes --pane '#{pane_id}'"
  bind-key C-g split-window -v -l 30% "session-notes --pane '#{pane_id}'"
  bind-key D display-popup -E -w 90% -h 90% "session-notes --dash"

Then reload tmux's config:

  tmux source-file ~/.tmux.conf

EOF
