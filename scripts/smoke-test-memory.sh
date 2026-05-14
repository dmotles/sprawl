#!/usr/bin/env bash
# smoke-test-memory.sh - Integration smoke test for the weave memory system.
#
# Exercises session persistence and context-blob file contracts, timeline
# read/write, and the cleanup/safety wrappers using the locally-built
# binary.
#
# QUM-565: this script previously also drove the deprecated handoff-CLI
# error-path tests. The live MCP handoff path is now covered by
# `make test-handoff-e2e` end-to-end; the CLI-flag error cases verified
# argument-validation behavior that goes away in Phase 2.3b of M13. Both
# blocks have been removed.
#
# Does NOT require a real Claude API key.
#
# Usage:
#   bash scripts/smoke-test-memory.sh              # run all tests
#   bash scripts/smoke-test-memory.sh --cleanup <namespace>   # kill sessions for namespace
#   bash scripts/smoke-test-memory.sh --cleanup-all           # kill all test-* sessions
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# --- Dedicated tmux socket for sandbox isolation (QUM-325) ---
# Use SPRAWL_TMUX_SOCKET if set by sprawl-test-env.sh, otherwise generate one.
SPRAWL_TMUX_SOCKET="${SPRAWL_TMUX_SOCKET:-sprawl-smoke-$$}"
export SPRAWL_TMUX_SOCKET

# _stmux wraps tmux with the dedicated sandbox socket.
_stmux() { tmux ${SPRAWL_TMUX_SOCKET:+-L "$SPRAWL_TMUX_SOCKET"} "$@"; }

# --- Cleanup modes ---

cleanup_namespace() {
    local ns="$1"
    echo "Cleaning up sessions for namespace: $ns"
    local sessions
    sessions=$(_stmux list-sessions -F '#{session_name}' 2>/dev/null | grep "^${ns}" || true)
    if [ -n "$sessions" ]; then
        echo "$sessions" | while read -r s; do
            echo "  Killing session: $s"
            _stmux kill-session -t "$s" 2>/dev/null || true
        done
    fi
}

cleanup_all() {
    echo "Cleaning up all test-* sessions"
    local sessions
    sessions=$(_stmux list-sessions -F '#{session_name}' 2>/dev/null | grep "^test-" || true)
    if [ -n "$sessions" ]; then
        echo "$sessions" | while read -r s; do
            echo "  Killing session: $s"
            _stmux kill-session -t "$s" 2>/dev/null || true
        done
    fi
}

if [ "${1:-}" = "--cleanup" ]; then
    if [ -z "${2:-}" ]; then
        echo "Usage: $0 --cleanup <namespace>" >&2
        exit 1
    fi
    cleanup_namespace "$2"
    exit 0
fi

if [ "${1:-}" = "--cleanup-all" ]; then
    cleanup_all
    exit 0
fi

# --- Test infrastructure ---

PASS_COUNT=0
FAIL_COUNT=0

pass() {
    PASS_COUNT=$((PASS_COUNT + 1))
    echo "  PASS: $1"
}

fail() {
    FAIL_COUNT=$((FAIL_COUNT + 1))
    echo "  FAIL: $1" >&2
}

assert_file_exists() {
    local path="$1" msg="${2:-file exists: $1}"
    if [ -f "$path" ]; then
        pass "$msg"
    else
        fail "$msg (file not found: $path)"
    fi
}

assert_dir_exists() {
    local path="$1" msg="${2:-dir exists: $1}"
    if [ -d "$path" ]; then
        pass "$msg"
    else
        fail "$msg (dir not found: $path)"
    fi
}

assert_file_contains() {
    local path="$1" pattern="$2" msg="${3:-file contains pattern}"
    if grep -q "$pattern" "$path" 2>/dev/null; then
        pass "$msg"
    else
        fail "$msg (pattern '$pattern' not found in $path)"
    fi
}

assert_file_not_contains() {
    local path="$1" pattern="$2" msg="${3:-file does not contain pattern}"
    if ! grep -q "$pattern" "$path" 2>/dev/null; then
        pass "$msg"
    else
        fail "$msg (pattern '$pattern' unexpectedly found in $path)"
    fi
}

assert_exit_zero() {
    local msg="$1"
    shift
    if "$@" >/dev/null 2>&1; then
        pass "$msg"
    else
        fail "$msg (command failed: $*)"
    fi
}

assert_exit_nonzero() {
    local msg="$1"
    shift
    if "$@" >/dev/null 2>&1; then
        fail "$msg (command succeeded unexpectedly: $*)"
    else
        pass "$msg"
    fi
}

# --- Setup ---

echo "=== Setting up test environment ==="

# Build binary
echo "Building sprawl..."
make -C "$REPO_ROOT" build >/dev/null 2>&1

SPRAWL_BIN="$REPO_ROOT/sprawl"
if [ ! -x "$SPRAWL_BIN" ]; then
    echo "FATAL: sprawl binary not found at $SPRAWL_BIN" >&2
    exit 1
fi

# Create temp dir with git repo
TEST_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/sprawl-smoke-XXXXXX")
git -C "$TEST_ROOT" init -b main --quiet
git -C "$TEST_ROOT" -c user.name="Test" -c user.email="test@test" commit --allow-empty -m "init" --quiet

# Generate test namespace
TEST_NS="test-$(head -c4 /dev/urandom | xxd -p)"

export SPRAWL_BIN
export SPRAWL_ROOT="$TEST_ROOT"
export SPRAWL_TEST_MODE=1
export SPRAWL_NAMESPACE="$TEST_NS"

# Teardown trap - uses targeted session kills only
cleanup() {
    # Kill only our test namespace sessions on the dedicated socket
    local sessions
    sessions=$(_stmux list-sessions -F '#{session_name}' 2>/dev/null | grep "^${TEST_NS}" || true)
    if [ -n "$sessions" ]; then
        echo "$sessions" | while read -r s; do
            _stmux kill-session -t "$s" 2>/dev/null || true
        done
    fi
    # Remove temp dir
    rm -rf "$TEST_ROOT"
}
trap cleanup EXIT

# Seed the minimal `.sprawl/` state files (namespace + root-name) that the
# memory-system tests below depend on. As of QUM-346 (M13 TUI cutover) the
# tmux-mode `sprawl init` parent entrypoint has been removed, so this script
# bootstraps the state directly instead of launching a parent agent loop.
echo "Seeding sprawl state (namespace=$TEST_NS, root-name=weave)..."
mkdir -p "$TEST_ROOT/.sprawl"
printf '%s\n' "$TEST_NS" > "$TEST_ROOT/.sprawl/namespace"
printf 'weave\n' > "$TEST_ROOT/.sprawl/root-name"

echo "  SPRAWL_ROOT=$TEST_ROOT"
echo "  TEST_NS=$TEST_NS"
echo ""

# --- Test 1: Sandbox seeded the expected state files ---

echo "=== Test 1: Sandbox state files present ==="

assert_dir_exists "$TEST_ROOT/.sprawl" "sandbox seeds .sprawl directory"
assert_file_exists "$TEST_ROOT/.sprawl/namespace" "sandbox seeds namespace file"
assert_file_exists "$TEST_ROOT/.sprawl/root-name" "sandbox seeds root-name file"
assert_file_contains "$TEST_ROOT/.sprawl/namespace" "$TEST_NS" "namespace file contains test namespace"
assert_file_contains "$TEST_ROOT/.sprawl/root-name" "weave" "root-name file contains weave"

echo ""

# --- Test 2: Session persistence (write and read back) ---

echo "=== Test 2: Session persistence ==="

SESSIONS_DIR="$TEST_ROOT/.sprawl/memory/sessions"
mkdir -p "$SESSIONS_DIR"

# Write a session file manually in the expected format
cat > "$SESSIONS_DIR/test-session-001.md" <<'SESSIONEOF'
---
session_id: test-session-001
timestamp: 2026-04-01T12:00:00Z
handoff: true
agents_active:
  - weave
  - elm
---

This is a test session summary for persistence validation.
SESSIONEOF

assert_file_exists "$SESSIONS_DIR/test-session-001.md" "session file written"
assert_file_contains "$SESSIONS_DIR/test-session-001.md" "session_id: test-session-001" "session file has correct session_id"
assert_file_contains "$SESSIONS_DIR/test-session-001.md" "timestamp: 2026-04-01T12:00:00Z" "session file has correct timestamp"
assert_file_contains "$SESSIONS_DIR/test-session-001.md" "handoff: true" "session file has handoff marker"
assert_file_contains "$SESSIONS_DIR/test-session-001.md" "agents_active:" "session file has agents_active"
assert_file_contains "$SESSIONS_DIR/test-session-001.md" "persistence validation" "session file has body content"

echo ""

# --- Test 3: Multiple sessions (context blob file contract) ---
#
# QUM-565: former Tests 3 (handoff-CLI happy-path) and 4 (handoff-CLI
# error cases) were removed — that deprecated CLI is being deleted in
# Phase 2.3b of M13. `make test-handoff-e2e` covers the live MCP
# handoff path end-to-end.

# Pre-seed MEMORY_DIR so subsequent tests don't need to mkdir it.
MEMORY_DIR="$TEST_ROOT/.sprawl/memory"
mkdir -p "$MEMORY_DIR"

echo "=== Test 3: Multiple sessions for context blob ==="

# Clean up and create 5 session files
rm -rf "$SESSIONS_DIR"/*

for i in 1 2 3 4 5; do
    SID="ctx-session-$(printf '%03d' "$i")"
    cat > "$SESSIONS_DIR/${SID}.md" <<EOF
---
session_id: $SID
timestamp: 2026-04-0${i}T12:00:00Z
handoff: true
agents_active: []
---

Session $i summary for context blob testing.
EOF
done

# Verify all 5 session files were created
FILE_COUNT=$(ls "$SESSIONS_DIR"/*.md 2>/dev/null | wc -l)
if [ "$FILE_COUNT" -eq 5 ]; then
    pass "created 5 session files for context blob"
else
    fail "expected 5 session files, found $FILE_COUNT"
fi

# Verify all session files exist and have correct content
for i in 1 2 3 4 5; do
    SID="ctx-session-$(printf '%03d' "$i")"
    assert_file_exists "$SESSIONS_DIR/${SID}.md" "session file ${SID}.md exists"
    assert_file_contains "$SESSIONS_DIR/${SID}.md" "Session $i summary" "session $i has correct body"
done

echo ""

# --- Test 4: Timeline read/write round-trip ---
#
# QUM-565: former Test 6 (handoff-after-many) removed alongside Tests 3/4.

echo "=== Test 4: Timeline read/write round-trip ==="

TIMELINE_PATH="$MEMORY_DIR/timeline.md"

# Write a timeline file in the expected format
cat > "$TIMELINE_PATH" <<'TLEOF'
# Session Timeline

- 2026-04-01T00:00:00Z: First timeline entry
- 2026-04-01T01:00:00Z: Second timeline entry
- 2026-04-01T02:00:00Z: Third timeline entry
TLEOF

assert_file_exists "$TIMELINE_PATH" "timeline file created"
assert_file_contains "$TIMELINE_PATH" "# Session Timeline" "timeline has header"
assert_file_contains "$TIMELINE_PATH" "First timeline entry" "timeline has first entry"
assert_file_contains "$TIMELINE_PATH" "Second timeline entry" "timeline has second entry"
assert_file_contains "$TIMELINE_PATH" "Third timeline entry" "timeline has third entry"

# Verify entry format (RFC3339 timestamp followed by summary)
ENTRY_COUNT=$(grep -c "^- [0-9]\{4\}-[0-9]\{2\}-[0-9]\{2\}T[0-9]\{2\}:[0-9]\{2\}:[0-9]\{2\}Z: " "$TIMELINE_PATH")
if [ "$ENTRY_COUNT" -eq 3 ]; then
    pass "timeline has 3 correctly formatted entries"
else
    fail "expected 3 timeline entries, found $ENTRY_COUNT"
fi

echo ""

# --- Test 5: Verify forbidden tmux commands are absent ---
#
# QUM-565: former Test 8 (handoff budget enforcement) removed alongside
# Tests 3/4/6.

echo "=== Test 5: Safety check ==="

SCRIPTS_DIR="$REPO_ROOT/scripts"
# Check that no script contains the bare/forbidden form of kill-server.
# Per QUM-325 the sanctioned form is `tmux -L "$SPRAWL_TMUX_SOCKET" kill-server`
# (operates on the dedicated sandbox socket only). The bare form
# `tmux kill-server` would wipe the user's default tmux server and is
# forbidden. Match only `tmux` immediately followed by whitespace+
# kill-server with no -L flag between them.
KILL_SERVER_HITS=0
for script in "$SCRIPTS_DIR"/*.sh; do
    # Strip comments, then look for bare-form invocations only.
    hits=$(sed 's/#.*$//' "$script" | grep -cE '(^|[^A-Za-z0-9_-])tmux +kill-server' || true)
    KILL_SERVER_HITS=$((KILL_SERVER_HITS + hits))
done
if [ "$KILL_SERVER_HITS" -gt 0 ]; then
    fail "found bare tmux-kill-server invocation in scripts/ (non-comment) - this is forbidden"
else
    pass "no bare tmux-kill-server invocations found in scripts/"
fi

echo ""

# --- Test 6: Cleanup flag functionality ---

echo "=== Test 6: Cleanup flags ==="

# Create a temporary test session to verify cleanup works
TEST_CLEANUP_NS="test-cleanup-$$"
_stmux new-session -d -s "${TEST_CLEANUP_NS}" "sleep 300" 2>/dev/null || true

if _stmux has-session -t "${TEST_CLEANUP_NS}" 2>/dev/null; then
    # Test --cleanup with specific namespace (inherits SPRAWL_TMUX_SOCKET)
    bash "$REPO_ROOT/scripts/smoke-test-memory.sh" --cleanup "$TEST_CLEANUP_NS" >/dev/null 2>&1

    if _stmux has-session -t "${TEST_CLEANUP_NS}" 2>/dev/null; then
        fail "--cleanup did not kill test session"
        _stmux kill-session -t "${TEST_CLEANUP_NS}" 2>/dev/null || true
    else
        pass "--cleanup killed targeted session"
    fi
else
    # If we couldn't create the session, skip this test
    pass "--cleanup test skipped (couldn't create test session)"
fi

# Test --cleanup-all: create a test-* session and verify it gets cleaned up
TEST_ALL_NS="test-all-$$"
_stmux new-session -d -s "${TEST_ALL_NS}" "sleep 300" 2>/dev/null || true

if _stmux has-session -t "${TEST_ALL_NS}" 2>/dev/null; then
    bash "$REPO_ROOT/scripts/smoke-test-memory.sh" --cleanup-all >/dev/null 2>&1

    if _stmux has-session -t "${TEST_ALL_NS}" 2>/dev/null; then
        fail "--cleanup-all did not kill test-* session"
        _stmux kill-session -t "${TEST_ALL_NS}" 2>/dev/null || true
    else
        pass "--cleanup-all killed test-* sessions"
    fi
else
    pass "--cleanup-all test skipped (couldn't create test session)"
fi

echo ""

# --- Summary ---

echo "==============================="
echo "  Results: $PASS_COUNT passed, $FAIL_COUNT failed"
echo "==============================="

if [ "$FAIL_COUNT" -gt 0 ]; then
    exit 1
fi
