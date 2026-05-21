#!/usr/bin/env bash
# scripts/e2e-tests/merge-reuse.sh — QUM-511/QUM-489 regression guard.
# Migrated from scripts/test-merge-reuse-e2e.sh (which remains in place).
# Pure shell — no claude required.

test_metadata() {
    echo ""
}

test_run() {
    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-merge-reuse-e2e"
    e2e_install_cleanup_traps

    echo "  SPRAWL_ROOT=$SPRAWL_ROOT"
    echo "  SPRAWL_BIN=$SPRAWL_BIN"

    # --- Bootstrap repo ---
    git -C "$SPRAWL_ROOT" init -b main --quiet
    git -C "$SPRAWL_ROOT" config user.name "Test"
    git -C "$SPRAWL_ROOT" config user.email "test@test"
    echo "base" > "$SPRAWL_ROOT/base.txt"
    echo ".sprawl/" > "$SPRAWL_ROOT/.gitignore"
    git -C "$SPRAWL_ROOT" add base.txt .gitignore
    git -C "$SPRAWL_ROOT" commit -q -m "initial"

    mkdir -p "$SPRAWL_ROOT/.sprawl/agents"
    echo "weave" > "$SPRAWL_ROOT/.sprawl/root-name"

    local HEAD_BEFORE
    HEAD_BEFORE=$(git -C "$SPRAWL_ROOT" rev-parse HEAD)
    echo "  HEAD_BEFORE=$HEAD_BEFORE"

    # --- Hand-craft agent engX on branch B1 with one commit ---
    local AGENT_NAME="engX"
    local AGENT_WT="$SPRAWL_ROOT/.sprawl/worktrees/$AGENT_NAME"
    mkdir -p "$(dirname "$AGENT_WT")"

    git -C "$SPRAWL_ROOT" worktree add -b B1 "$AGENT_WT" >/dev/null
    git -C "$AGENT_WT" config user.name "engX"
    git -C "$AGENT_WT" config user.email "engx@test"
    echo "foo content" > "$AGENT_WT/foo.txt"
    git -C "$AGENT_WT" add foo.txt
    git -C "$AGENT_WT" commit -q -m "engX adds foo on B1"

    cat > "$SPRAWL_ROOT/.sprawl/agents/${AGENT_NAME}.json" <<EOF
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

    cat > "$SPRAWL_ROOT/.sprawl/agents/weave.json" <<EOF
{
  "name": "weave",
  "type": "weave",
  "family": "weave",
  "parent": "root",
  "prompt": "",
  "branch": "main",
  "worktree": "$SPRAWL_ROOT",
  "status": "active",
  "created_at": "2026-05-07T00:00:00Z"
}
EOF

    export SPRAWL_AGENT_IDENTITY="weave"

    echo ""
    echo "=== Step 3: sprawl merge engX (first time, B1 → main) ==="
    cd "$SPRAWL_ROOT"
    "$SPRAWL_BIN" merge --no-validate "$AGENT_NAME" 2>&1 | sed 's/^/    /' || {
        echo "FAIL: first merge returned non-zero" >&2
        return 1
    }

    local HEAD1
    HEAD1=$(git -C "$SPRAWL_ROOT" rev-parse HEAD)
    echo "  HEAD1=$HEAD1"
    if [ "$HEAD1" = "$HEAD_BEFORE" ]; then
        echo "FAIL: integration HEAD did not advance after first merge" >&2
        return 1
    fi
    if ! git -C "$SPRAWL_ROOT" show HEAD --stat | grep -q "foo.txt"; then
        echo "FAIL: first merge did not include foo.txt" >&2
        git -C "$SPRAWL_ROOT" show HEAD --stat >&2
        return 1
    fi
    echo "  PASS: first merge advanced HEAD and includes foo.txt"

    echo ""
    echo "=== Step 4: simulate delegate reuse — engX checks out B2 with new commit ==="
    git -C "$AGENT_WT" checkout -q -b B2
    echo "bar content" > "$AGENT_WT/bar.txt"
    git -C "$AGENT_WT" add bar.txt
    git -C "$AGENT_WT" commit -q -m "engX adds bar on B2 (delegate reuse)"

    local STATE_BRANCH
    STATE_BRANCH=$(grep '"branch"' "$SPRAWL_ROOT/.sprawl/agents/${AGENT_NAME}.json" | head -1 | sed 's/.*"branch": *"\([^"]*\)".*/\1/')
    if [ "$STATE_BRANCH" != "B1" ]; then
        echo "FAIL: test setup broken — state branch is $STATE_BRANCH, expected B1" >&2
        return 1
    fi
    echo "  state.branch is still '$STATE_BRANCH' (stale, simulating delegate)"
    echo "  agent worktree HEAD is now on:"
    git -C "$AGENT_WT" rev-parse --abbrev-ref HEAD | sed 's/^/    /'

    echo ""
    echo "=== Step 5: sprawl merge engX (after delegate-style branch swap) ==="
    local MERGE_OUTPUT
    MERGE_OUTPUT=$("$SPRAWL_BIN" merge --no-validate "$AGENT_NAME" 2>&1 || {
        echo "FAIL: second merge returned non-zero" >&2
        return 1
    })
    echo "$MERGE_OUTPUT" | sed 's/^/    /'

    local HEAD2
    HEAD2=$(git -C "$SPRAWL_ROOT" rev-parse HEAD)
    echo "  HEAD2=$HEAD2"

    if [ "$HEAD2" = "$HEAD1" ]; then
        echo "" >&2
        echo "FAIL (QUM-511 reproduced): integration HEAD did NOT advance after delegate-style branch swap." >&2
        echo "       merge silently no-op'd because it read stale agentState.Branch=B1 instead of" >&2
        echo "       resolving the agent worktree's current branch (B2)." >&2
        return 1
    fi

    if ! git -C "$SPRAWL_ROOT" show HEAD --stat | grep -q "bar.txt"; then
        echo "FAIL: second merge did not include bar.txt from B2" >&2
        git -C "$SPRAWL_ROOT" show HEAD --stat >&2
        return 1
    fi

    if echo "$MERGE_OUTPUT" | grep -q "Nothing to merge"; then
        echo "FAIL: merge reported 'Nothing to merge' but B2 has new commits" >&2
        return 1
    fi

    if ! echo "$MERGE_OUTPUT" | grep -q "B2"; then
        echo "FAIL: merge stderr summary should mention resolved branch B2" >&2
        echo "      output was:" >&2
        echo "$MERGE_OUTPUT" >&2
        return 1
    fi

    echo ""
    echo "=== PASS: merge correctly followed agent worktree to B2 ==="
    echo "OK"
    return 0
}
