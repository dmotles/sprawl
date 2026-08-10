#!/usr/bin/env bash

# QUM-1186 lane 5: THIS SCRIPT NO LONGER RUNS.
#
# It is a pre-matrix standalone duplicate, kept during the matrix soak as a
# fallback. QUM-1186 deleted the `report_status` tool and the `last_report_*`
# state fields this script probes with, so its assertions have lost their
# subject. It is not migrated, because migrating it would mean maintaining a
# second copy of every probe change forever; and it is not deleted here,
# because deleting these six is separately tracked and dragging it into this
# slice would put a Makefile and test-leak-resistance-e2e.sh rework inside an
# already-wide diff.
#
# What it must NOT do is run. It has no `MIN_ASSERTIONS` floor, no aggregator
# and no rc-77 path, so a partial run of it reports green — which is the exact
# defect this slice exists to remove. A script that exits before doing anything
# cannot drift out of sync with the row that replaced it and cannot false-green.
#
# Its prose describes a delegate-driven branch swap; the tool is deleted.
echo "SKIP: ${0##*/} is superseded by matrix row 'merge-reuse' — its coverage lives there now." >&2
echo "  Run instead:  make test-e2e-matrix-merge-reuse" >&2
echo "  Deleting this file and its Makefile target is tracked separately (QUM-1186 lane 5 hand-off)." >&2
exit 77

# test-merge-reuse-e2e.sh — End-to-end regression guard for QUM-511 / QUM-489.
#
# QUM-511: After `delegate` reuses an agent on a NEW branch, `sprawl merge X`
# silently no-ops because `internal/agentops/merge.go` reads the spawn-time
# `agentState.Branch` from state — which is now stale — instead of resolving
# the agent worktree's actual current branch.
#
# QUM-489: `internal/sprawlmcp/server.go::toolMerge` flattens
# `result.WasNoOp == true` to "Merged agent X", hiding the QUM-511 bug from
# any agent that calls merge through MCP.
#
# This script reproduces QUM-511 directly with shell + the sprawl CLI (no
# claude binary required — we craft agent state and worktrees by hand,
# which is faster and CI-friendly):
#
#   1. Create sandbox repo, weave root, integration branch.
#   2. Hand-craft agent "engX" with a worktree on branch "B1" + a commit.
#   3. As weave: `sprawl merge engX`. Assert HEAD advanced; record HEAD1.
#   4. In engX worktree: `git checkout -b B2` and add a NEW commit, but
#      DO NOT update agent state JSON (Branch field still says "B1").
#      This is the post-delegate state.
#   5. As weave: `sprawl merge engX`. Assert HEAD advanced past HEAD1
#      AND `git show HEAD --stat` includes the file from B2.
#      Pre-fix (red): step 5 will be a silent no-op (HEAD == HEAD1) because
#      merge reads Branch="B1" from state, finds no new commits on B1, and
#      reports success without merging anything from B2.
#
# Exit 0 on success (post-fix). Exit 1 on bug repro (pre-fix).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# --- Build sprawl (or use SPRAWL_BIN override) ---
if [ -n "${SPRAWL_BIN:-}" ] && [ -x "$SPRAWL_BIN" ]; then
    echo "=== Using SPRAWL_BIN=$SPRAWL_BIN ==="
else
    echo "=== Building sprawl ==="
    make -C "$REPO_ROOT" build >/dev/null
    SPRAWL_BIN="$REPO_ROOT/sprawl"
fi
if [ ! -x "$SPRAWL_BIN" ]; then
    echo "FATAL: sprawl binary not found at $SPRAWL_BIN" >&2
    exit 1
fi

# --- Sandbox under /tmp/ ---
SANDBOX=$(mktemp -d "${TMPDIR:-/tmp}/sprawl-merge-reuse-e2e-XXXXXX")
SANDBOX_REAL="$(cd "$SANDBOX" 2>/dev/null && pwd -P || echo "$SANDBOX")"
case "$SANDBOX_REAL" in
    /tmp/*) ;;
    *)
        echo "FATAL: sandbox not under /tmp/; aborting" >&2
        exit 1
        ;;
esac
SANDBOX="$SANDBOX_REAL"
trap 'rm -rf "$SANDBOX"' EXIT

echo "  SANDBOX=$SANDBOX"
echo "  SPRAWL_BIN=$SPRAWL_BIN"

# --- Bootstrap repo ---
git -C "$SANDBOX" init -b main --quiet
git -C "$SANDBOX" config user.name "Test"
git -C "$SANDBOX" config user.email "test@test"
echo "base" > "$SANDBOX/base.txt"
echo ".sprawl/" > "$SANDBOX/.gitignore"
git -C "$SANDBOX" add base.txt .gitignore
git -C "$SANDBOX" commit -q -m "initial"

mkdir -p "$SANDBOX/.sprawl/agents"
echo "weave" > "$SANDBOX/.sprawl/root-name"

# Weave runs on the same "main" branch — that's the integration target.
# Record the integration HEAD before any merges.
HEAD_BEFORE=$(git -C "$SANDBOX" rev-parse HEAD)
echo "  HEAD_BEFORE=$HEAD_BEFORE"

# --- Hand-craft agent engX on branch B1 with one commit ---
AGENT_NAME="engX"
AGENT_WT="$SANDBOX/.sprawl/worktrees/$AGENT_NAME"
mkdir -p "$(dirname "$AGENT_WT")"

git -C "$SANDBOX" worktree add -b B1 "$AGENT_WT" >/dev/null
git -C "$AGENT_WT" config user.name "engX"
git -C "$AGENT_WT" config user.email "engx@test"
echo "foo content" > "$AGENT_WT/foo.txt"
git -C "$AGENT_WT" add foo.txt
git -C "$AGENT_WT" commit -q -m "engX adds foo on B1"

# Write agent state (mark engX as a child of weave on B1, status=done).
cat > "$SANDBOX/.sprawl/agents/${AGENT_NAME}.json" <<EOF
{
  "name": "$AGENT_NAME",
  "type": "engineer",
  "family": "engineering",
  "parent": "weave",
  "prompt": "irrelevant for this test",
  "branch": "B1",
  "worktree": "$AGENT_WT",
  "status": "done",
  "created_at": "2026-05-07T00:00:00Z"
}
EOF

# Weave's "agent state" — needs to exist so merge precondition passes.
cat > "$SANDBOX/.sprawl/agents/weave.json" <<EOF
{
  "name": "weave",
  "type": "weave",
  "family": "weave",
  "parent": "root",
  "prompt": "",
  "branch": "main",
  "worktree": "$SANDBOX",
  "status": "active",
  "created_at": "2026-05-07T00:00:00Z"
}
EOF

# --- Step 3: First merge as weave. Should land foo.txt on main. ---
export SPRAWL_ROOT="$SANDBOX"
export SPRAWL_AGENT_IDENTITY="weave"
export SPRAWL_BIN

echo ""
echo "=== Step 3: sprawl merge engX (first time, B1 → main) ==="
cd "$SANDBOX"
"$SPRAWL_BIN" merge --no-validate "$AGENT_NAME" 2>&1 | sed 's/^/    /' || {
    echo "FAIL: first merge returned non-zero" >&2
    exit 1
}

HEAD1=$(git -C "$SANDBOX" rev-parse HEAD)
echo "  HEAD1=$HEAD1"
if [ "$HEAD1" = "$HEAD_BEFORE" ]; then
    echo "FAIL: integration HEAD did not advance after first merge" >&2
    exit 1
fi
if ! git -C "$SANDBOX" show HEAD --stat | grep -q "foo.txt"; then
    echo "FAIL: first merge did not include foo.txt" >&2
    git -C "$SANDBOX" show HEAD --stat >&2
    exit 1
fi
echo "  PASS: first merge advanced HEAD and includes foo.txt"

# --- Step 4: Simulate delegate-style reuse on a fresh branch B2.
#     Crucially, DO NOT update the agent state JSON's "branch" field. ---

echo ""
echo "=== Step 4: simulate delegate reuse — engX checks out B2 with new commit ==="
git -C "$AGENT_WT" checkout -q -b B2
echo "bar content" > "$AGENT_WT/bar.txt"
git -C "$AGENT_WT" add bar.txt
git -C "$AGENT_WT" commit -q -m "engX adds bar on B2 (delegate reuse)"

# Confirm agent state STILL says branch=B1 (this is the bug surface).
STATE_BRANCH=$(grep '"branch"' "$SANDBOX/.sprawl/agents/${AGENT_NAME}.json" | head -1 | sed 's/.*"branch": *"\([^"]*\)".*/\1/')
if [ "$STATE_BRANCH" != "B1" ]; then
    echo "FAIL: test setup broken — state branch is $STATE_BRANCH, expected B1" >&2
    exit 1
fi
echo "  state.branch is still '$STATE_BRANCH' (stale, simulating delegate)"
echo "  agent worktree HEAD is now on:"
git -C "$AGENT_WT" rev-parse --abbrev-ref HEAD | sed 's/^/    /'

# --- Step 5: Second merge. Must pull in bar.txt from B2. ---
echo ""
echo "=== Step 5: sprawl merge engX (after delegate-style branch swap) ==="
MERGE_OUTPUT=$("$SPRAWL_BIN" merge --no-validate "$AGENT_NAME" 2>&1 || {
    echo "FAIL: second merge returned non-zero" >&2
    exit 1
})
echo "$MERGE_OUTPUT" | sed 's/^/    /'

HEAD2=$(git -C "$SANDBOX" rev-parse HEAD)
echo "  HEAD2=$HEAD2"

# --- Decisive assertions for QUM-511 / QUM-489 ---
if [ "$HEAD2" = "$HEAD1" ]; then
    echo "" >&2
    echo "FAIL (QUM-511 reproduced): integration HEAD did NOT advance after delegate-style branch swap." >&2
    echo "       merge silently no-op'd because it read stale agentState.Branch=B1 instead of" >&2
    echo "       resolving the agent worktree's current branch (B2)." >&2
    exit 1
fi

if ! git -C "$SANDBOX" show HEAD --stat | grep -q "bar.txt"; then
    echo "FAIL: second merge did not include bar.txt from B2" >&2
    git -C "$SANDBOX" show HEAD --stat >&2
    exit 1
fi

if echo "$MERGE_OUTPUT" | grep -q "Nothing to merge"; then
    echo "FAIL: merge reported 'Nothing to merge' but B2 has new commits" >&2
    exit 1
fi

if ! echo "$MERGE_OUTPUT" | grep -q "B2"; then
    echo "FAIL: merge stderr summary should mention resolved branch B2" >&2
    echo "      output was:" >&2
    echo "$MERGE_OUTPUT" >&2
    exit 1
fi

echo ""
echo "=== PASS: merge correctly followed agent worktree to B2 ==="
echo "OK"
