#!/usr/bin/env bash
# sprawl-test-env.sh - Set up an isolated sprawl test environment.
#
# Creates a temp directory with a git repo, builds the sprawl binary,
# and initializes sprawl in detached mode with a test namespace.
#
# Usage:
#   bash scripts/sprawl-test-env.sh          # print env vars
#   eval "$(bash scripts/sprawl-test-env.sh)"  # export into current shell
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# Build the binary
echo "Building sprawl..." >&2
make -C "$REPO_ROOT" build >&2

SPRAWL_BIN="$REPO_ROOT/dendra"
if [ ! -x "$SPRAWL_BIN" ]; then
    echo "FAIL: dendra binary not found or not executable at $SPRAWL_BIN" >&2
    exit 1
fi

# Create temp directory
TEST_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/sprawl-test-XXXXXX")

# Init git repo
git -C "$TEST_ROOT" init -b main --quiet
git -C "$TEST_ROOT" -c user.name="Test" -c user.email="test@test" commit --allow-empty -m "init" --quiet

# Generate test namespace (test- prefix + 8 hex chars)
TEST_NS="test-$(head -c4 /dev/urandom | xxd -p)"

# Run sprawl init --detached in the temp dir
echo "Initializing sprawl in $TEST_ROOT with namespace $TEST_NS..." >&2
(
    cd "$TEST_ROOT"
    SPRAWL_BIN="$SPRAWL_BIN" \
    SPRAWL_TEST_MODE=1 \
    "$SPRAWL_BIN" init --detached --namespace "$TEST_NS"
) >&2

# Print export statements (can be eval'd)
cat <<EOF
export SPRAWL_BIN="$SPRAWL_BIN"
export SPRAWL_ROOT="$TEST_ROOT"
export SPRAWL_TEST_MODE=1
export SPRAWL_NAMESPACE="$TEST_NS"
export TEST_NS="$TEST_NS"
export TEST_ROOT="$TEST_ROOT"
EOF

# Print info to stderr
cat >&2 <<EOF

Test environment ready:
  SPRAWL_BIN=$SPRAWL_BIN
  SPRAWL_ROOT=$TEST_ROOT
  SPRAWL_TEST_MODE=1
  SPRAWL_NAMESPACE=$TEST_NS
  Session: ${TEST_NS}neo
  Attach:  tmux attach-session -t ${TEST_NS}neo
  Cleanup: tmux kill-session -t ${TEST_NS}neo && rm -rf $TEST_ROOT
EOF
