#!/usr/bin/env bash
# test-sandbox-safety.sh — assert scripts/sprawl-test-env.sh refuses to run
# when its sandbox root would be outside /tmp/ or when the cwd is inside
# a .sprawl/worktrees/ path. Guards against the 2026-04-21 incident.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SCRIPT="$REPO_ROOT/scripts/sprawl-test-env.sh"

fail() {
    echo "FAIL: $1" >&2
    exit 1
}

# Test 1: non-/tmp SPRAWL_TEST_ROOT_OVERRIDE is rejected.
bogus_root="$(mktemp -d /var/tmp/sprawl-safety-XXXXXX 2>/dev/null || mktemp -d "$HOME/sprawl-safety-XXXXXX")"
trap 'rm -rf -- "$bogus_root"' EXIT
if (cd /tmp && SPRAWL_TEST_ROOT_OVERRIDE="$bogus_root" bash "$SCRIPT") >/tmp/safety-stdout.$$ 2>/tmp/safety-stderr.$$; then
    fail "script accepted non-/tmp SPRAWL_TEST_ROOT_OVERRIDE ($bogus_root)"
fi
if ! grep -q "must be under /tmp/" /tmp/safety-stderr.$$; then
    echo "--- stderr ---" >&2
    cat /tmp/safety-stderr.$$ >&2
    fail "expected '/tmp/' assertion error not seen"
fi
rm -f /tmp/safety-stdout.$$ /tmp/safety-stderr.$$
echo "PASS: non-/tmp override rejected"

# Test 2: running from inside .sprawl/worktrees/ is rejected.
fake_wt="$(mktemp -d /tmp/sprawl-safety-wt-XXXXXX)/.sprawl/worktrees/finn"
mkdir -p "$fake_wt"
if (cd "$fake_wt" && bash "$SCRIPT") >/tmp/safety-stdout.$$ 2>/tmp/safety-stderr.$$; then
    rm -rf -- "${fake_wt%/.sprawl/worktrees/finn}"
    fail "script accepted being run from inside .sprawl/worktrees/"
fi
if ! grep -q "refusing to run sprawl-test-env.sh from inside a worktree" /tmp/safety-stderr.$$; then
    echo "--- stderr ---" >&2
    cat /tmp/safety-stderr.$$ >&2
    rm -rf -- "${fake_wt%/.sprawl/worktrees/finn}"
    fail "expected worktree-refuse error not seen"
fi
rm -f /tmp/safety-stdout.$$ /tmp/safety-stderr.$$
rm -rf -- "${fake_wt%/.sprawl/worktrees/finn}"
echo "PASS: cwd-inside-worktree rejected"

echo "ALL PASS"
