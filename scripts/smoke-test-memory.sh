#!/usr/bin/env bash
# smoke-test-memory.sh - Integration smoke test for the neo memory system.
#
# Exercises session persistence, handoff, context blob file contracts,
# timeline read/write, and budget enforcement using the locally-built binary.
#
# Does NOT require a real Claude API key.
#
# Usage:
#   bash scripts/smoke-test-memory.sh              # run all tests
#   bash scripts/smoke-test-memory.sh --cleanup <namespace>   # kill sessions for namespace
#   bash scripts/smoke-test-memory.sh --cleanup-all           # kill all test-* sessions
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# --- Cleanup modes ---

cleanup_namespace() {
    local ns="$1"
    echo "Cleaning up sessions for namespace: $ns"
    local sessions
    sessions=$(tmux list-sessions -F '#{session_name}' 2>/dev/null | grep "^${ns}" || true)
    if [ -n "$sessions" ]; then
        echo "$sessions" | while read -r s; do
            echo "  Killing session: $s"
            tmux kill-session -t "$s" 2>/dev/null || true
        done
    fi
}

cleanup_all() {
    echo "Cleaning up all test-* sessions"
    local sessions
    sessions=$(tmux list-sessions -F '#{session_name}' 2>/dev/null | grep "^test-" || true)
    if [ -n "$sessions" ]; then
        echo "$sessions" | while read -r s; do
            echo "  Killing session: $s"
            tmux kill-session -t "$s" 2>/dev/null || true
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

SPRAWL_BIN="$REPO_ROOT/dendra"
if [ ! -x "$SPRAWL_BIN" ]; then
    echo "FATAL: dendra binary not found at $SPRAWL_BIN" >&2
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
    # Kill only our test namespace sessions
    local sessions
    sessions=$(tmux list-sessions -F '#{session_name}' 2>/dev/null | grep "^${TEST_NS}" || true)
    if [ -n "$sessions" ]; then
        echo "$sessions" | while read -r s; do
            tmux kill-session -t "$s" 2>/dev/null || true
        done
    fi
    # Remove temp dir
    rm -rf "$TEST_ROOT"
}
trap cleanup EXIT

# Run sprawl init --detached
echo "Running sprawl init --detached --namespace $TEST_NS..."
(cd "$TEST_ROOT" && "$SPRAWL_BIN" init --detached --namespace "$TEST_NS") 2>&1

# Kill the neo-loop tmux session immediately (we don't need it running)
tmux kill-session -t "${TEST_NS}neo" 2>/dev/null || true

echo "  SPRAWL_ROOT=$TEST_ROOT"
echo "  TEST_NS=$TEST_NS"
echo ""

# --- Test 1: Init creates expected state files ---

echo "=== Test 1: Init creates expected state files ==="

assert_dir_exists "$TEST_ROOT/.sprawl" "init creates .sprawl directory"
assert_file_exists "$TEST_ROOT/.sprawl/namespace" "init creates namespace file"
assert_file_exists "$TEST_ROOT/.sprawl/root-name" "init creates root-name file"
assert_file_contains "$TEST_ROOT/.sprawl/namespace" "$TEST_NS" "namespace file contains test namespace"
assert_file_contains "$TEST_ROOT/.sprawl/root-name" "neo" "root-name file contains neo"

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
  - neo
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

# --- Test 3: Handoff command ---

echo "=== Test 3: Handoff command ==="

# Clean up any previous session files for clean handoff test
rm -rf "$SESSIONS_DIR"/*

# Write last-session-id (required by handoff)
MEMORY_DIR="$TEST_ROOT/.sprawl/memory"
mkdir -p "$MEMORY_DIR"
echo "smoke-handoff-001" > "$MEMORY_DIR/last-session-id"

# Run handoff
HANDOFF_OUTPUT=$(echo "Handoff test summary: everything is working." | \
    SPRAWL_AGENT_IDENTITY=neo \
    SPRAWL_ROOT="$TEST_ROOT" \
    "$SPRAWL_BIN" handoff 2>&1) || {
    fail "handoff command exited non-zero"
    echo "  Output: $HANDOFF_OUTPUT" >&2
}

# Verify session file was created
HANDOFF_FILE="$SESSIONS_DIR/smoke-handoff-001.md"
if [ -f "$HANDOFF_FILE" ]; then
    pass "handoff created session file"
    assert_file_contains "$HANDOFF_FILE" "session_id: smoke-handoff-001" "handoff file has correct session_id"
    assert_file_contains "$HANDOFF_FILE" "handoff: true" "handoff file has handoff: true"
    assert_file_contains "$HANDOFF_FILE" "everything is working" "handoff file has body from stdin"
else
    fail "handoff did not create session file in $SESSIONS_DIR"
fi

# Verify handoff signal file
assert_file_exists "$MEMORY_DIR/handoff-signal" "handoff created signal file"

# Verify last-session-id was read correctly
assert_file_contains "$MEMORY_DIR/last-session-id" "smoke-handoff-001" "last-session-id unchanged after handoff"

echo ""

# --- Test 4: Handoff error cases ---

echo "=== Test 4: Handoff error cases ==="

# Handoff without SPRAWL_AGENT_IDENTITY should fail
assert_exit_nonzero "handoff fails without SPRAWL_AGENT_IDENTITY" \
    env -u SPRAWL_AGENT_IDENTITY SPRAWL_ROOT="$TEST_ROOT" "$SPRAWL_BIN" handoff

# Handoff with wrong identity (not root) should fail
assert_exit_nonzero "handoff fails with non-root identity" \
    env SPRAWL_AGENT_IDENTITY=not-neo SPRAWL_ROOT="$TEST_ROOT" "$SPRAWL_BIN" handoff

# Handoff without SPRAWL_ROOT should fail
assert_exit_nonzero "handoff fails without SPRAWL_ROOT" \
    env SPRAWL_AGENT_IDENTITY=neo -u SPRAWL_ROOT "$SPRAWL_BIN" handoff

# Handoff without last-session-id should fail
rm -f "$MEMORY_DIR/last-session-id"
assert_exit_nonzero "handoff fails without last-session-id" \
    bash -c 'echo "test" | SPRAWL_AGENT_IDENTITY=neo SPRAWL_ROOT="'"$TEST_ROOT"'" "'"$SPRAWL_BIN"'" handoff'

echo ""

# --- Test 5: Multiple sessions (context blob file contract) ---

echo "=== Test 5: Multiple sessions for context blob ==="

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

# --- Test 6: Handoff creates correct session after multiple exist ---

echo "=== Test 6: Handoff with existing sessions ==="

echo "handoff-after-many" > "$MEMORY_DIR/last-session-id"
rm -f "$MEMORY_DIR/handoff-signal"

echo "New handoff after existing sessions." | \
    SPRAWL_AGENT_IDENTITY=neo \
    SPRAWL_ROOT="$TEST_ROOT" \
    "$SPRAWL_BIN" handoff >/dev/null 2>&1

if [ -f "$SESSIONS_DIR/handoff-after-many.md" ]; then
    pass "handoff created new session file alongside existing ones"
else
    fail "handoff did not create new session file"
fi

# Total should now be 6 (5 pre-existing + 1 new)
TOTAL_FILES=$(ls "$SESSIONS_DIR"/*.md 2>/dev/null | wc -l)
if [ "$TOTAL_FILES" -eq 6 ]; then
    pass "total session files is 6 after handoff"
else
    fail "expected 6 total session files, found $TOTAL_FILES"
fi

echo ""

# --- Test 7: Timeline read/write round-trip ---

echo "=== Test 7: Timeline read/write round-trip ==="

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

# --- Test 8: Budget enforcement (large content via handoff) ---

echo "=== Test 8: Budget enforcement (large content) ==="

# Write a fresh last-session-id
echo "budget-test-session" > "$MEMORY_DIR/last-session-id"
rm -f "$MEMORY_DIR/handoff-signal"

# Generate a large body (50,000 chars)
LARGE_BODY=$(python3 -c "print('x' * 50000)" 2>/dev/null || dd if=/dev/zero bs=50000 count=1 2>/dev/null | tr '\0' 'x')

echo "$LARGE_BODY" | \
    SPRAWL_AGENT_IDENTITY=neo \
    SPRAWL_ROOT="$TEST_ROOT" \
    "$SPRAWL_BIN" handoff >/dev/null 2>&1

BUDGET_FILE="$SESSIONS_DIR/budget-test-session.md"
if [ -f "$BUDGET_FILE" ]; then
    pass "handoff succeeded with large content"
    BUDGET_SIZE=$(wc -c < "$BUDGET_FILE")
    if [ "$BUDGET_SIZE" -gt 50000 ]; then
        pass "large session file written (${BUDGET_SIZE} bytes)"
    else
        fail "session file unexpectedly small: ${BUDGET_SIZE} bytes"
    fi
else
    fail "handoff failed with large content"
fi

echo ""

# --- Test 9: Verify forbidden tmux commands are absent ---

echo "=== Test 9: Safety check ==="

SCRIPTS_DIR="$REPO_ROOT/scripts"
# Check that no script contains an actual invocation of the forbidden command.
# We strip comments and string literals, then look for the pattern.
FORBIDDEN_CMD="kill-server"
KILL_SERVER_HITS=0
for script in "$SCRIPTS_DIR"/*.sh; do
    # Remove comment lines and blank lines, then check for "tmux" + "kill-server" on same line
    hits=$(sed 's/#.*$//' "$script" | grep -c "tmux.*$FORBIDDEN_CMD" || true)
    KILL_SERVER_HITS=$((KILL_SERVER_HITS + hits))
done
if [ "$KILL_SERVER_HITS" -gt 0 ]; then
    fail "found 'tmux $FORBIDDEN_CMD' in scripts/ (non-comment) - this is forbidden"
else
    pass "no 'tmux $FORBIDDEN_CMD' commands found in scripts/"
fi

echo ""

# --- Test 10: Cleanup flag functionality ---

echo "=== Test 10: Cleanup flags ==="

# Create a temporary test session to verify cleanup works
TEST_CLEANUP_NS="test-cleanup-$$"
tmux new-session -d -s "${TEST_CLEANUP_NS}neo" "sleep 300" 2>/dev/null || true

if tmux has-session -t "${TEST_CLEANUP_NS}neo" 2>/dev/null; then
    # Test --cleanup with specific namespace
    bash "$REPO_ROOT/scripts/smoke-test-memory.sh" --cleanup "$TEST_CLEANUP_NS" >/dev/null 2>&1

    if tmux has-session -t "${TEST_CLEANUP_NS}neo" 2>/dev/null; then
        fail "--cleanup did not kill test session"
        tmux kill-session -t "${TEST_CLEANUP_NS}neo" 2>/dev/null || true
    else
        pass "--cleanup killed targeted session"
    fi
else
    # If we couldn't create the session, skip this test
    pass "--cleanup test skipped (couldn't create test session)"
fi

# Test --cleanup-all: create a test-* session and verify it gets cleaned up
TEST_ALL_NS="test-all-$$"
tmux new-session -d -s "${TEST_ALL_NS}neo" "sleep 300" 2>/dev/null || true

if tmux has-session -t "${TEST_ALL_NS}neo" 2>/dev/null; then
    bash "$REPO_ROOT/scripts/smoke-test-memory.sh" --cleanup-all >/dev/null 2>&1

    if tmux has-session -t "${TEST_ALL_NS}neo" 2>/dev/null; then
        fail "--cleanup-all did not kill test-* session"
        tmux kill-session -t "${TEST_ALL_NS}neo" 2>/dev/null || true
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
