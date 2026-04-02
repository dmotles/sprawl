#!/usr/bin/env bash
# dendra-test-env.sh - Set up an isolated dendra test environment.
#
# Creates a temp directory with a git repo, builds the dendra binary,
# and initializes dendra in detached mode with a test namespace.
#
# Usage:
#   bash scripts/dendra-test-env.sh          # print env vars
#   eval "$(bash scripts/dendra-test-env.sh)"  # export into current shell
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# Build the binary
echo "Building dendra..." >&2
make -C "$REPO_ROOT" build >&2

DENDRA_BIN="$REPO_ROOT/dendra"
if [ ! -x "$DENDRA_BIN" ]; then
    echo "FAIL: dendra binary not found or not executable at $DENDRA_BIN" >&2
    exit 1
fi

# Create temp directory
TEST_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/dendra-test-XXXXXX")

# Init git repo
git -C "$TEST_ROOT" init -b main --quiet
git -C "$TEST_ROOT" -c user.name="Test" -c user.email="test@test" commit --allow-empty -m "init" --quiet

# Generate test namespace (test- prefix + 8 hex chars)
TEST_NS="test-$(head -c4 /dev/urandom | xxd -p)"

# Run dendra init --detached in the temp dir
echo "Initializing dendra in $TEST_ROOT with namespace $TEST_NS..." >&2
(
    cd "$TEST_ROOT"
    DENDRA_BIN="$DENDRA_BIN" \
    DENDRA_TEST_MODE=1 \
    "$DENDRA_BIN" init --detached --namespace "$TEST_NS"
) >&2

# Print export statements (can be eval'd)
cat <<EOF
export DENDRA_BIN="$DENDRA_BIN"
export DENDRA_ROOT="$TEST_ROOT"
export DENDRA_TEST_MODE=1
export DENDRA_NAMESPACE="$TEST_NS"
export TEST_NS="$TEST_NS"
export TEST_ROOT="$TEST_ROOT"
EOF

# Print info to stderr
cat >&2 <<EOF

Test environment ready:
  DENDRA_BIN=$DENDRA_BIN
  DENDRA_ROOT=$TEST_ROOT
  DENDRA_TEST_MODE=1
  DENDRA_NAMESPACE=$TEST_NS
  Session: ${TEST_NS}sensei
  Attach:  tmux attach-session -t ${TEST_NS}sensei
  Cleanup: tmux kill-session -t ${TEST_NS}sensei && rm -rf $TEST_ROOT
EOF
