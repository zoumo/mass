#!/usr/bin/env bash
set -euo pipefail

# Multi-agent collaboration E2E setup script.
# Starts mass, registers 3 agents, creates workspace+agents via `massctl compose`,
# then launches cmux with agent-run chat panes.

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
E2E_DIR="$SCRIPT_DIR"

MASS="$PROJECT_ROOT/bin/mass"
MASSCTL="$PROJECT_ROOT/bin/massctl"

# Use /tmp short path to avoid macOS 104-char Unix socket path limit.
ROOT_DIR="/tmp/mass-e2e-$$"
SOCKET="$ROOT_DIR/mass.sock"

WORKSPACE_NAME="agentd-e2e"

# ── Prerequisite checks ──────────────────────────────────────────────────
for bin in "$MASS" "$MASSCTL"; do
  if [[ ! -x "$bin" ]]; then
    echo "ERROR: $bin not found. Run 'make build' first." >&2
    exit 1
  fi
done

for cmd in cmux bunx; do
  if ! command -v "$cmd" &>/dev/null; then
    echo "ERROR: $cmd not found. Please install it first." >&2
    exit 1
  fi
done

# ── Cleanup on exit ──────────────────────────────────────────────────────
cleanup() {
  echo ""
  echo "Cleaning up..."
  if [[ -S "$SOCKET" ]]; then
    for agent in codex claude-code gsd-pi; do
      "$MASSCTL" --socket "$SOCKET" agentrun stop --workspace "$WORKSPACE_NAME" --name "$agent" 2>/dev/null || true
    done
    sleep 1
  fi
  pkill -f "$ROOT_DIR" 2>/dev/null || true
  rm -rf "$ROOT_DIR"
  echo "Cleanup done."
}
trap cleanup EXIT

CTL="$MASSCTL --socket $SOCKET"

# ── Step 1: Start mass ──────────────────────────────────────────────────
MASS_LOG="$E2E_DIR/mass.log"
echo "==> Starting mass (root=$ROOT_DIR, log=$MASS_LOG)..."
"$MASS" server --root "$ROOT_DIR" >"$MASS_LOG" 2>&1 &
MASS_PID=$!
echo "    mass PID: $MASS_PID"

echo "==> Waiting for ARI socket..."
for i in $(seq 1 50); do
  if [[ -S "$SOCKET" ]]; then
    echo "    Socket ready."
    break
  fi
  if [[ $i -eq 50 ]]; then
    echo "ERROR: Socket not ready after 5s" >&2
    exit 1
  fi
  sleep 0.1
done

# ── Step 2: Register agent templates ─────────────────────────────────────
echo "==> Registering agent templates..."
$CTL agent apply -f "$E2E_DIR/agents/codex.yaml"
$CTL agent apply -f "$E2E_DIR/agents/claude.yaml"
$CTL agent apply -f "$E2E_DIR/agents/gsd-pi.yaml"

# ── Step 3: Create workspace + agent runs ────────────────────────────────
echo "==> Creating workspace and agent runs (massctl compose)..."
$CTL compose -f "$E2E_DIR/compose.yaml"

# ── Helpers ──────────────────────────────────────────────────────────────

# cmux_split <direction> <from_surface>
#   Splits from_surface in the given direction and prints the new surface ref.
cmux_split() {
  local out
  out=$(cmux new-split "$1" --workspace "$CMUX_WS" --surface "$2")
  echo "$out" | grep -o 'surface:[0-9]*' | head -1
}

# cmux_send <surface> <text>
#   Sends text + Enter to the given surface.
cmux_send() {
  cmux send --workspace "$CMUX_WS" --surface "$1" "$2"
  cmux send-key --workspace "$CMUX_WS" --surface "$1" Enter
  sleep 0.3
}

# ── Step 5: Launch cmux workspace with 4 panes ───────────────────────────
echo "==> Launching cmux workspace with 4 panes..."

CMUX_WS_OUT=$(cmux new-workspace --name "mass-e2e" --cwd "$PROJECT_ROOT")
CMUX_WS=$(echo "$CMUX_WS_OUT" | grep -o 'workspace:[0-9]*' | head -1)
echo "    cmux workspace: $CMUX_WS"

# Grab the initial surface ID via list-pane-surfaces.
INIT_SURFACE=$(cmux list-pane-surfaces --workspace "$CMUX_WS" | grep -o 'surface:[0-9]*' | head -1)

# Create 2x2 layout: split right, then split each column down.
S_CODEX="$INIT_SURFACE"
S_CLAUDE=$(cmux_split right "$INIT_SURFACE")
S_GSDPI=$(cmux_split down  "$S_CLAUDE")
S_CTL=$(cmux_split down    "$INIT_SURFACE")

cmux rename-tab --workspace "$CMUX_WS" "mass-e2e"

# Top-left: codex | Top-right: claude-code | Bottom-right: gsd-pi
cmux_send "$S_CODEX"  "$MASSCTL --socket '$SOCKET' agentrun chat --workspace '$WORKSPACE_NAME' --name codex 2>>'$ROOT_DIR/chat-codex.log'"
cmux_send "$S_CLAUDE" "$MASSCTL --socket '$SOCKET' agentrun chat --workspace '$WORKSPACE_NAME' --name claude-code 2>>'$ROOT_DIR/chat-claude.log'"
cmux_send "$S_GSDPI"  "$MASSCTL --socket '$SOCKET' agentrun chat --workspace '$WORKSPACE_NAME' --name gsd-pi 2>>'$ROOT_DIR/chat-gsdpi.log'"

# Bottom-left: ctl (pre-configured control terminal)
cmux_send "$S_CTL" "set -x SOCKET '$SOCKET'"
cmux_send "$S_CTL" "set -x WS '$WORKSPACE_NAME'"
cmux_send "$S_CTL" "alias ctl '$MASSCTL --socket \$SOCKET'"

echo ""
echo "=========================================="
echo "  E2E environment ready!"
echo "=========================================="
echo ""
echo "  Pane layout (2x2):"
echo "    Top-left:     codex (code review)"
echo "    Top-right:    claude-code (review verification)"
echo "    Bottom-left:  ctl (pre-configured control terminal)"
echo "    Bottom-right: gsd-pi (fix execution)"
echo ""
echo "  Logs:"
echo "    mass:          $MASS_LOG"
echo "    chat-codex:    $ROOT_DIR/chat-codex.log"
echo "    chat-claude:   $ROOT_DIR/chat-claude.log"
echo "    chat-gsdpi:    $ROOT_DIR/chat-gsdpi.log"
echo ""
echo "  Bottom-left pane has SOCKET, WS, and ctl alias ready."
echo "  Example commands:"
echo "    ctl agentrun list --workspace \$WS"
echo "    ctl agentrun prompt --workspace \$WS --name codex --text '...' --wait"
echo "    ctl agentrun get --workspace \$WS --name codex"
echo ""
echo "  Press Ctrl-C to stop and cleanup."
echo "=========================================="
echo ""

# Keep the script alive so cleanup trap fires on Ctrl-C.
wait "$MASS_PID" 2>/dev/null || true
