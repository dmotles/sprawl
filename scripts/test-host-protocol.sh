#!/bin/bash
# test-host-protocol.sh — Integration tests for the Claude Code host protocol engine.
#
# Runs the hosttest binary through several scenarios to validate end-to-end
# protocol behavior. Requires: claude binary in PATH, valid API key.
#
# Usage:
#   bash scripts/test-host-protocol.sh
#   bash scripts/test-host-protocol.sh --skip-mcp   # skip MCP tests
#
# Exit codes:
#   0 — all tests passed
#   1 — one or more tests failed

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# Build the hosttest binary
echo "=== Building hosttest ==="
cd "$REPO_ROOT"
go build -o hosttest ./cmd/hosttest/
trap 'rm -f "$REPO_ROOT/hosttest"' EXIT

HOSTTEST="$REPO_ROOT/hosttest"
SKIP_MCP=false
FAILURES=0
PASSED=0

for arg in "$@"; do
  case "$arg" in
    --skip-mcp) SKIP_MCP=true ;;
  esac
done

pass() {
  echo "  PASS: $1"
  PASSED=$((PASSED + 1))
}

fail() {
  echo "  FAIL: $1"
  FAILURES=$((FAILURES + 1))
}

# --- Test 1: Simple prompt ---
echo ""
echo "=== Test 1: Simple prompt ==="
echo "Sending a simple math question and verifying result."

if timeout 60 "$HOSTTEST" "Say exactly the word 'pong' and nothing else." 2>/dev/null | grep -q '\[result'; then
  pass "simple prompt received result"
else
  fail "simple prompt did not receive result"
fi

# --- Test 2: Tool-using prompt ---
echo ""
echo "=== Test 2: Tool use (Bash) ==="
echo "Sending a prompt that triggers Bash tool use."

if timeout 120 "$HOSTTEST" "Use the Bash tool to run 'echo hello-from-hosttest'. Do not use any other tools." 2>/dev/null | grep -q 'can_use_tool'; then
  pass "tool use triggered can_use_tool"
else
  fail "tool use did not trigger can_use_tool"
fi

# --- Test 3: Exit code on success ---
echo ""
echo "=== Test 3: Exit code ==="

if timeout 60 "$HOSTTEST" "Say ok" 2>/dev/null; then
  pass "exit code 0 on success"
else
  fail "non-zero exit code on success"
fi

# --- Test 4: MCP bridge with dummy server ---
if [ "$SKIP_MCP" = false ]; then
  echo ""
  echo "=== Test 4: MCP bridge ==="
  echo "Registering dummy echo MCP server and testing tool discovery."

  if timeout 120 "$HOSTTEST" --test-mcp "Call the echo tool from the test-echo server with the message 'hello from hosttest'. Report what it returned." 2>/dev/null | grep -q 'mcp_message'; then
    pass "MCP bridge handled mcp_message"
  else
    fail "MCP bridge did not handle mcp_message"
  fi
else
  echo ""
  echo "=== Test 4: MCP bridge (SKIPPED) ==="
fi

# --- Test 5: Clean shutdown (no leaked processes) ---
echo ""
echo "=== Test 5: Clean shutdown ==="

# Run hosttest and check that the claude process is cleaned up
BEFORE=$(pgrep -c claude 2>/dev/null || echo 0)
timeout 60 "$HOSTTEST" "Say done" 2>/dev/null || true
sleep 1
AFTER=$(pgrep -c claude 2>/dev/null || echo 0)

if [ "$AFTER" -le "$BEFORE" ]; then
  pass "no leaked claude processes"
else
  fail "leaked claude processes (before=$BEFORE, after=$AFTER)"
fi

# --- Summary ---
echo ""
echo "========================================="
echo "Results: $PASSED passed, $FAILURES failed"
echo "========================================="

if [ "$FAILURES" -gt 0 ]; then
  exit 1
fi
exit 0
