# M4 Manager Agent Smoke Test

> **Note:** This document references historical milestones (M4, M7) that have been completed. Kept for reference.

This document describes how to validate the manager agent type end-to-end for Milestone 4. Full automated E2E testing with real Claude agents is deferred to M7. For M4, we rely on:

1. **Unit tests** covering mechanical parts (prompt construction, spawn, agent-loop routing, merge).
2. **Manual smoke test** for human validation of the full lifecycle.

---

## 1. Unit Test Coverage Checklist

These tests should already pass via `go test ./...`. They cover the mechanical plumbing without requiring a live Claude instance.

| Area | Test | File |
|------|------|------|
| Prompt construction | `BuildManagerPrompt` returns correct content with agent name, parent, branch, family, and environment | `internal/agent/prompt_test.go` |
| Spawn accepts manager | `TestSpawn_ManagerType_HappyPath` -- spawns manager, verifies state has `type: "manager"` | `cmd/spawn_test.go` |
| Spawn registers manager | `TestSpawn_ManagerInSupportedTypes` -- confirms `manager` is in `supportedTypes` map | `cmd/spawn_test.go` |
| Agent-loop routes manager | Agent-loop calls `BuildManagerPrompt` when `agentState.Type == "manager"` | `cmd/agentloop.go` (line 72-84), tested in `cmd/agentloop_test.go` |
| Merge by manager | `TestMerge_HappyPath` and related tests -- validates merge works when caller is agent's parent | `cmd/merge_test.go` |
| Report done | `TestReportDone_HappyPath` -- validates status update, state persistence, parent notification | `cmd/report_test.go` |
| Report problem | `TestReportProblem_HappyPath` -- validates escalation path | `cmd/report_test.go` |

**Run all unit tests:**

```bash
make validate
```

All packages should report `ok`. No failures expected.

---

## 2. Manual Smoke Test Procedure

### Prerequisites

- Built `sprawl` binary (`make build`)
- `tmux` installed and available on `$PATH`
- A Git repository to test in (can be this repo or a throwaway)
- No existing sprawl session running (or use a different namespace)

### Step 1: Initialize sprawl

```bash
./sprawl init
```

**Verify:**
- `.sprawl/` directory created
- Namespace file exists at `.sprawl/namespace`

### Step 2: Start root session

```bash
./sprawl start
```

**Verify:**
- tmux session created (check with `tmux list-sessions`)
- Root agent is active

### Step 3: Spawn a manager agent

From the root agent session (or via CLI):

```bash
sprawl spawn agent --family engineering --type manager \
  --branch "test/manager-smoke" \
  --prompt "Coordinate a simple task: create a hello.txt file."
```

**Verify state file** at `.sprawl/agents/<manager-name>.json`:

```json
{
  "name": "<allocated-name>",
  "type": "manager",
  "family": "engineering",
  "parent": "<root-agent-name>",
  "prompt": "Coordinate a simple task: create a hello.txt file.",
  "branch": "<branch-name>",
  "worktree": "<repo-root>/.sprawl/worktrees/<manager-name>",
  "tmux_session": "<namespace>-children",
  "status": "active",
  "created_at": "<RFC3339 timestamp>",
  "tree_path": "root><manager-name>"
}
```

**Verify tmux window:**

```bash
tmux list-windows -t <namespace>-children
```

The manager should have a window named after its allocated agent name.

### Step 4: Verify manager prompt (SYSTEM.md)

Check the system prompt written for the manager:

```bash
cat .sprawl/agents/<manager-name>/SYSTEM.md
```

**Expected content should include:**
- `Your name is <manager-name>.`
- `You are a Manager agent in Sprawl`
- `Your parent (manager) is <root-name>.`
- `You work in your own git worktree on branch <branch-name>.`
- `As an engineering manager, your domain informs...`
- Environment section with working directory, git branch, platform, shell

### Step 5: Verify worktree created

```bash
git worktree list
```

**Verify:**
- Entry for `.sprawl/worktrees/<manager-name>` exists
- It is on a branch derived from the specified base branch

### Step 6: Observe manager behavior (decomposition)

Watch the manager's tmux window. The manager should:
1. Read its initial prompt
2. Decompose the task into subtasks
3. Begin spawning child agents (engineers, researchers, etc.)

**Verify child agents spawned:**

```bash
ls .sprawl/agents/
```

You should see JSON files for both the manager and any children it spawned. Each child state file should have `"parent": "<manager-name>"`.

### Step 7: Observe child agent work

Watch child agent tmux windows. Each should:
1. Receive its task prompt
2. Work in its own worktree on its own branch
3. Report done via `sprawl report done "<summary>"`

**Verify child reports:**

```bash
cat .sprawl/agents/<child-name>.json | grep last_report
```

Expected: `"last_report_type": "done"`, `"status": "done"`

### Step 8: Observe manager verification and merge

After children report done, the manager should:
1. Receive notification of child completion
2. Verify the child's work (run tests in child's worktree)
3. Merge child's branch into its integration branch via `sprawl merge <child-name>`

**Verify merge occurred:**

```bash
cd .sprawl/worktrees/<manager-name>
git log --oneline -5
```

You should see a squash merge commit with format:
```
<child-name>: <report-message>
```

**Verify child cleaned up:**
- Child state file removed from `.sprawl/agents/`
- Child worktree removed from `.sprawl/worktrees/`
- Child branch deleted

### Step 9: Observe manager reporting done

After all children are merged and final validation passes, the manager should:

```bash
sprawl report done "All subtasks completed and merged."
```

**Verify manager state:**

```bash
cat .sprawl/agents/<manager-name>.json | grep -E '"status"|last_report'
```

Expected:
- `"status": "done"`
- `"last_report_type": "done"`

### Step 10: Root merges manager

From the root session:

```bash
sprawl merge <manager-name>
```

**Verify:**
- Manager's integration branch squash-merged into root's branch
- Manager state file removed
- Manager worktree cleaned up
- Manager branch deleted

**Final verification:**

```bash
git log --oneline -5  # Should show squash merge commit
ls .sprawl/agents/    # Manager and children should be gone
git worktree list     # Manager worktree should be gone
```

---

## 3. Expected Lifecycle Summary

```
root
 |
 +-- spawn manager (type=manager, family=engineering)
      |
      |  [Manager decomposes task]
      |
      +-- spawn child-1 (type=engineer)
      +-- spawn child-2 (type=engineer)
      |
      |  [Children work independently in their worktrees]
      |
      |  child-1: sprawl report done "..."
      |  child-2: sprawl report done "..."
      |
      |  [Manager verifies each child's work]
      |  [Manager merges each child: sprawl merge child-1, sprawl merge child-2]
      |
      |  [Manager runs final validation on integration branch]
      |
      |  manager: sprawl report done "All work integrated and validated."
      |
 root: sprawl merge manager
```

At each stage, the key artifacts to check are:

| Stage | State File (`status`) | Worktree | Branch | tmux Window |
|-------|----------------------|----------|--------|-------------|
| After spawn | `active` | Created | Created | Running |
| After report done | `done` | Still exists | Still exists | Still running |
| After merge | Deleted | Deleted | Deleted | Destroyed |

---

## 4. Semi-Automated Smoke Test Script

The following script validates the plumbing without requiring a real Claude instance. It sets up a temporary sprawl environment, spawns a manager, and validates state files and prompt content.

```bash
#!/usr/bin/env bash
set -euo pipefail

# -- Configuration --
SPRAWL_BIN="${SPRAWL_BIN:-./sprawl}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
TMPDIR_BASE="${TMPDIR:-/tmp}"

# -- Setup --
WORK_DIR=$(mktemp -d "${TMPDIR_BASE}/sprawl-smoke-XXXXXX")
trap 'rm -rf "$WORK_DIR"' EXIT

echo "=== M4 Manager Smoke Test ==="
echo "Work dir: $WORK_DIR"
echo "Binary:   $SPRAWL_BIN"
echo ""

# Initialize a git repo
cd "$WORK_DIR"
git init -b main
git commit --allow-empty -m "initial commit"

# Set up sprawl structure
export SPRAWL_ROOT="$WORK_DIR"
export SPRAWL_AGENT_IDENTITY="root"

mkdir -p .sprawl/agents

# -- Test 1: Build succeeds --
echo "[1/5] Checking binary exists..."
if [ ! -x "$SPRAWL_BIN" ]; then
  echo "FAIL: sprawl binary not found or not executable at $SPRAWL_BIN"
  exit 1
fi
echo "  PASS"

# -- Test 2: Simulate manager state creation --
echo "[2/5] Creating mock manager state..."
MANAGER_NAME="alice"
MANAGER_BRANCH="test/integration"

# Create the branch
git checkout -b "$MANAGER_BRANCH"
git checkout main

# Create manager state file (simulates what spawn would create)
cat > .sprawl/agents/${MANAGER_NAME}.json <<AGENT_EOF
{
  "name": "$MANAGER_NAME",
  "type": "manager",
  "family": "engineering",
  "parent": "root",
  "prompt": "Coordinate test task",
  "branch": "$MANAGER_BRANCH",
  "worktree": "$WORK_DIR/.sprawl/worktrees/$MANAGER_NAME",
  "tmux_session": "test-children",
  "status": "active",
  "created_at": "2026-04-02T00:00:00Z",
  "tree_path": "root>$MANAGER_NAME"
}
AGENT_EOF

# Validate state file is valid JSON and has correct type
TYPE=$(python3 -c "import json,sys; d=json.load(open(sys.argv[1])); print(d['type'])" .sprawl/agents/${MANAGER_NAME}.json)
if [ "$TYPE" != "manager" ]; then
  echo "  FAIL: state type is '$TYPE', expected 'manager'"
  exit 1
fi
echo "  PASS: state file has type=manager"

# -- Test 3: Validate prompt construction --
echo "[3/5] Checking prompt construction..."
mkdir -p .sprawl/agents/${MANAGER_NAME}

# Use go test to run prompt tests specifically
cd "$SCRIPT_DIR/../.."
go test ./internal/agent/ -run TestBuildManagerPrompt -v 2>&1 | tail -5
PROMPT_EXIT=${PIPESTATUS[0]}
cd "$WORK_DIR"

if [ "$PROMPT_EXIT" -ne 0 ]; then
  echo "  FAIL: prompt construction tests failed"
  exit 1
fi
echo "  PASS: prompt construction tests pass"

# -- Test 4: Validate spawn accepts manager type --
echo "[4/5] Checking spawn tests for manager type..."
cd "$SCRIPT_DIR/../.."
go test ./cmd/ -run TestSpawn_ManagerType -v 2>&1 | tail -5
SPAWN_EXIT=${PIPESTATUS[0]}
cd "$WORK_DIR"

if [ "$SPAWN_EXIT" -ne 0 ]; then
  echo "  FAIL: spawn manager tests failed"
  exit 1
fi
echo "  PASS: spawn accepts manager type"

# -- Test 5: Validate merge tests pass --
echo "[5/5] Checking merge tests..."
cd "$SCRIPT_DIR/../.."
go test ./cmd/ -run TestMerge_HappyPath -v 2>&1 | tail -5
MERGE_EXIT=${PIPESTATUS[0]}
cd "$WORK_DIR"

if [ "$MERGE_EXIT" -ne 0 ]; then
  echo "  FAIL: merge tests failed"
  exit 1
fi
echo "  PASS: merge tests pass"

echo ""
echo "=== All smoke tests passed ==="
```

**Usage:**

```bash
make build
bash docs/testing/m4-manager-smoke-test.sh
```

> **Note:** This script validates the plumbing (state files, prompt construction, spawn/merge mechanics) but does NOT test real Claude interactions. Full E2E testing with live agents is planned for M7.
