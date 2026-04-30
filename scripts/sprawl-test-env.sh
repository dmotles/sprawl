#!/usr/bin/env bash
# sprawl-test-env.sh - Set up an isolated sprawl test environment.
#
# Creates a temp directory with a git repo, builds the sprawl binary, and
# seeds the minimal `.sprawl/` state files (namespace, root-name) that
# downstream commands need. As of QUM-346 (M13 TUI cutover) the tmux-mode
# `sprawl init` parent entrypoint has been removed, so this script no longer
# launches a parent agent loop — sandbox callers that need a live agent
# session should `sprawl enter` (TUI) directly.
#
# Usage:
#   bash scripts/sprawl-test-env.sh          # print env vars
#   eval "$(bash scripts/sprawl-test-env.sh)"  # export into current shell
#
# SAFETY: This script refuses to run from inside a .sprawl/worktrees/ path
# and asserts that the sandbox root lives under /tmp/ before printing any
# cleanup trap. See the 2026-04-21 incident writeup for why.
set -euo pipefail

# Refuse to run from inside a worktree — an agent's cwd being under
# .sprawl/worktrees/ means stray `rm -rf $SPRAWL_ROOT` in that shell could
# later resolve against the real repo. Force the caller to cd to /tmp first.
CWD_CHECK="$(pwd -P)"
case "$CWD_CHECK" in
    */.sprawl/worktrees/*)
        echo "FAIL: refusing to run sprawl-test-env.sh from inside a worktree ($CWD_CHECK)." >&2
        echo "      cd to /tmp (or any path outside .sprawl/worktrees/) and retry." >&2
        exit 1
        ;;
esac

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# Build the binary
echo "Building sprawl..." >&2
make -C "$REPO_ROOT" build >&2

SPRAWL_BIN="$REPO_ROOT/sprawl"
if [ ! -x "$SPRAWL_BIN" ]; then
    echo "FAIL: sprawl binary not found or not executable at $SPRAWL_BIN" >&2
    exit 1
fi

# Create temp directory. Allow override via SPRAWL_TEST_ROOT_OVERRIDE for
# safety testing of the /tmp assertion; production callers should not set it.
if [ -n "${SPRAWL_TEST_ROOT_OVERRIDE:-}" ]; then
    TEST_ROOT="$SPRAWL_TEST_ROOT_OVERRIDE"
else
    TEST_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/sprawl-test-XXXXXX")
fi

# Canonicalize and assert TEST_ROOT is under /tmp/. This is the last line of
# defense against the cleanup trap nuking something it shouldn't.
TEST_ROOT_REAL="$(cd "$TEST_ROOT" 2>/dev/null && pwd -P || echo "$TEST_ROOT")"
case "$TEST_ROOT_REAL" in
    /tmp/*) ;;
    *)
        echo "FATAL: sandbox TEST_ROOT must be under /tmp/ (got: $TEST_ROOT_REAL)." >&2
        echo "       Refusing to continue — a cleanup trap here could destroy real data." >&2
        exit 1
        ;;
esac
# Belt-and-suspenders: explicitly reject known-dangerous paths.
case "$TEST_ROOT_REAL" in
    /|/home|/home/*|/root|/root/*|/etc|/etc/*|/usr|/usr/*|/var|/var/*)
        echo "FATAL: sandbox TEST_ROOT resolves to a protected system path: $TEST_ROOT_REAL" >&2
        exit 1
        ;;
esac

# Init git repo
git -C "$TEST_ROOT" init -b main --quiet
git -C "$TEST_ROOT" -c user.name="Test" -c user.email="test@test" commit --allow-empty -m "init" --quiet

# Generate test namespace (test- prefix + 8 hex chars)
TEST_NS="test-$(head -c4 /dev/urandom | xxd -p)"

# Seed the minimal `.sprawl/` state files that downstream sprawl commands
# read (namespace + root-name). This replaces the legacy `sprawl init
# --detached` invocation that QUM-346 removed; no tmux session is created
# here. Tests that need a live agent should launch `sprawl enter` themselves.
echo "Seeding sprawl state in $TEST_ROOT with namespace $TEST_NS..." >&2
mkdir -p "$TEST_ROOT/.sprawl"
printf '%s\n' "$TEST_NS" > "$TEST_ROOT/.sprawl/namespace"
printf 'weave\n' > "$TEST_ROOT/.sprawl/root-name"

# Emit shell code to be eval'd by the caller. Installs:
#   - exported env vars (SPRAWL_BIN, SPRAWL_ROOT, ...)
#   - sprawl_sandbox_destroy: sanctioned manual teardown
#   - an EXIT trap that auto-cleans on shell exit
# Both the function and trap reassert the /tmp/ guard before deleting.
cat <<EOF
export SPRAWL_BIN="$SPRAWL_BIN"
export SPRAWL_ROOT="$TEST_ROOT_REAL"
export SPRAWL_TEST_MODE=1
export SPRAWL_NAMESPACE="$TEST_NS"
export SPRAWL_TMUX_SOCKET="sprawl-sandbox-$TEST_NS"
export TEST_NS="$TEST_NS"
export TEST_ROOT="$TEST_ROOT_REAL"

# _stmux wraps tmux with the dedicated sandbox socket.
_stmux() { tmux \${SPRAWL_TMUX_SOCKET:+-L "\$SPRAWL_TMUX_SOCKET"} "\$@"; }

sprawl_sandbox_destroy() {
    local root="\${SPRAWL_ROOT:-}"
    local ns="\${SPRAWL_NAMESPACE:-}"
    if [ -z "\$root" ]; then
        echo "sprawl_sandbox_destroy: SPRAWL_ROOT unset, nothing to do" >&2
        return 0
    fi
    case "\$root" in
        /tmp/*) ;;
        *)
            echo "sprawl_sandbox_destroy: REFUSING — SPRAWL_ROOT not under /tmp/: \$root" >&2
            return 1
            ;;
    esac
    if [ -n "\$ns" ]; then
        _stmux kill-session -t "\$ns" 2>/dev/null || true
    fi
    rm -rf -- "\$root"
    unset SPRAWL_ROOT SPRAWL_NAMESPACE SPRAWL_TMUX_SOCKET TEST_ROOT TEST_NS SPRAWL_TEST_MODE SPRAWL_BIN
    trap - EXIT
    echo "sprawl_sandbox_destroy: cleaned up \$root" >&2
}

_sprawl_sandbox_cleanup_trap() {
    local root="\${SPRAWL_ROOT:-}"
    [ -z "\$root" ] && return 0
    case "\$root" in
        /tmp/*) ;;
        *)
            echo "sprawl sandbox EXIT trap: REFUSING — SPRAWL_ROOT not under /tmp/: \$root" >&2
            return 0
            ;;
    esac
    [ -n "\${SPRAWL_NAMESPACE:-}" ] && _stmux kill-session -t "\$SPRAWL_NAMESPACE" 2>/dev/null || true
    rm -rf -- "\$root"
}
trap _sprawl_sandbox_cleanup_trap EXIT
EOF

# Print info to stderr
cat >&2 <<EOF

Test environment ready:
  SPRAWL_BIN=$SPRAWL_BIN
  SPRAWL_ROOT=$TEST_ROOT_REAL
  SPRAWL_TEST_MODE=1
  SPRAWL_NAMESPACE=$TEST_NS
  Session: ${TEST_NS}
  SPRAWL_TMUX_SOCKET=sprawl-sandbox-${TEST_NS}
  Attach:  tmux -L sprawl-sandbox-${TEST_NS} attach-session -t ${TEST_NS}
  Cleanup: sprawl_sandbox_destroy   (or just exit the shell — auto-cleans)

SAFETY: Never run 'rm -rf \$SPRAWL_ROOT' by hand. Use sprawl_sandbox_destroy.
EOF
