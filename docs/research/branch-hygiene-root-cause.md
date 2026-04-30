# Branch Hygiene Root Cause Analysis: Wave 1 Commits Landing on Main

## Executive Summary

**Root Cause Category: (B) Tool Issue — with elements of (E) Combination**

All 10 Wave 1 issue commits landed directly on `main` because the TUI-mode
supervisor is a **single shared instance** with `CallerName: "weave"` hardcoded.
Every MCP tool call — from any agent in the tree — is executed as if weave made
it. This means:

1. **Spawn is broken for managers**: When a manager spawns a child, the child's
   `Parent` field is set to `"weave"`, not the manager. Managers have no
   authority over their own children.

2. **Merge always targets main**: When any agent calls the merge MCP tool, the
   supervisor resolves `callerName = "weave"`, loads weave's worktree (the repo
   root, on `main`), and merges into `main`.

3. **Managers cannot merge their children**: Even if a manager tried, the parent
   check in `merge.go` would fail because `child.Parent = "weave"` ≠
   `callerName` (the manager's actual identity is never used).

The manager integration branches (`dmotles/wave1-test-ci`, `dmotles/wave1-tui-ux`,
`dmotles/wave1-infra-mcp`) ended up with zero unique commits because no merge
operation ever targeted them.

## Detailed Analysis

### 1. The Shared Supervisor Problem

**File**: `cmd/enter.go`, lines 195-214

```go
sup, err := supervisor.NewReal(supervisor.Config{
    SprawlRoot: sprawlRoot,
    CallerName: "weave",  // ← hardcoded to root agent
})
// ...
mcpServer := sprawlmcp.New(sup)
childBridge := host.NewMCPBridge()
childBridge.Register("sprawl", mcpServer)
sup.SetChildMCPConfig(
    backend.InitSpec{
        MCPServerNames: []string{"sprawl"},
        ToolBridge:     childBridge,
    },
    sprawlmcp.MCPToolNames(),
)
```

A single supervisor is created with `CallerName: "weave"`. A single MCP server
is created from that supervisor. The **same** MCP server is wired into **all**
child agent runtimes via `SetChildMCPConfig`. Every agent in the tree — weave,
managers, engineers, researchers — shares this one supervisor and one MCP server.

### 2. How CallerName Propagates

**File**: `internal/supervisor/real.go`, lines 75-83

```go
supervisorGetenv := func(key string) string {
    switch key {
    case "SPRAWL_AGENT_IDENTITY":
        return cfg.CallerName  // always "weave"
    case "SPRAWL_ROOT":
        return cfg.SprawlRoot
    default:
        return os.Getenv(key)
    }
}
```

This `supervisorGetenv` is injected into `spawnDeps`, `mergeDeps`, and
`retireDeps`. Every agentops function that calls `deps.Getenv("SPRAWL_AGENT_IDENTITY")`
gets `"weave"` regardless of which agent initiated the MCP tool call.

### 3. Impact on Spawn

**File**: `internal/agentops/spawn.go`, lines 87-88

```go
parentName := deps.Getenv("SPRAWL_AGENT_IDENTITY")
```

When tower (a manager) calls `spawn({...})`, the MCP tool goes through the
shared supervisor. `parentName` resolves to `"weave"`. The child's state is
persisted with `Parent: "weave"` (line 199).

**Consequence**: The child agent's system prompt says "Your parent (manager) is
weave" — not tower. The child reports to weave, not the manager. The manager
has no parent relationship with its own children.

### 4. Impact on Merge

**File**: `internal/agentops/merge.go`, lines 44, 91-94, 115

```go
callerName := deps.Getenv("SPRAWL_AGENT_IDENTITY")  // "weave"
// ...
callerWorktree := sprawlRoot  // fallback
if a, err := deps.LoadAgent(sprawlRoot, callerName); err == nil {
    callerWorktree = a.Worktree  // weave has no agent state → stays sprawlRoot
}
// ...
targetBranch, err := deps.CurrentBranch(callerWorktree)  // main
```

When any agent calls `merge({agent: "<child>"})`:

1. `callerName = "weave"` (from supervisorGetenv)
2. `LoadAgent("weave")` fails (weave is the root, has no agent state file)
3. `callerWorktree` falls back to `sprawlRoot` (the repo root)
4. `CurrentBranch(sprawlRoot)` returns `"main"`
5. The child's branch is squash-merged into `main`

**Parent check at line 62**: `agentState.Parent != callerName` — since the
child's parent was recorded as "weave" (see §3), this check passes when the
supervisor processes the merge. But it would **fail** if the manager tried to
merge via CLI (where `SPRAWL_AGENT_IDENTITY` is the manager's actual name).

### 5. Impact on Messaging

**File**: `internal/supervisor/real.go`, line 251

```go
return messages.Send(r.sprawlRoot, r.callerName, agentName, subject, body)
```

Messages sent via the MCP `send_async` tool are always stamped as from "weave",
not the actual caller. This further confuses the agent hierarchy.

### 6. What Happened in Wave 1

Reconstructed timeline:

1. Weave spawned three managers (tower, forge, bastion) with integration branches
2. Each manager was given its own worktree and branch
3. When managers spawned children via MCP tools, the children were all recorded
   with `Parent: "weave"` (not the manager)
4. Children did their work and reported done — to weave, not their manager
5. Merges were executed (likely by weave or by managers whose merge calls were
   processed as weave) → all targeted `main`
6. The three integration branches never received any commits

**Evidence from git**: All three Wave 1 branches are behind main with zero
unique commits:
- `dmotles/wave1-test-ci`: 10 commits behind main, 0 ahead
- `dmotles/wave1-tui-ux`: 10 commits behind main, 0 ahead
- `dmotles/wave1-infra-mcp`: 4 commits behind main, 0 ahead

The 10 issue commits on main are squash-merge commits (standard `sprawl merge`
output format), confirming they were merged via the tool, not raw git.

## What the Prompts Say vs What Happens

The manager prompt (`prompt_child_sections.go`, line 199) explicitly says:

> You work in your own git worktree on branch %s. This is your integration branch
> where sub-agent work is merged into.

And the integration section says:

> Use merge({agent: "<agent>"}) to land work on your integration branch.

The prompts are **correct** in their instructions. The **tool implementation**
does not honor the caller's identity, making these instructions impossible to
follow.

## Root Cause Classification

| Category | Verdict | Notes |
|----------|---------|-------|
| (A) Process/prompt issue | No | Prompts correctly instruct managers about branch hygiene |
| **(B) Tool issue** | **YES — Primary** | Shared supervisor with hardcoded CallerName breaks spawn parent recording and merge target resolution |
| (C) Worktree issue | No | Worktrees are created correctly on the right branches |
| (D) Agent behavior | Partial | Agents followed instructions but tools betrayed them |
| (E) Combination | Yes | B is the root cause; D is a consequence (agents can't work around broken tools) |

## Recommended Fix

The supervisor needs **per-caller identity** for MCP tool calls. Options:

### Option A: Per-Agent MCP Server (recommended)

Give each child agent its own supervisor (or at minimum its own `Getenv`
closure) with the correct `CallerName`. The MCP server already supports this
pattern — the issue is that `enter.go` creates one and shares it.

### Option B: Pass Caller Identity Through MCP Context

Add the calling agent's identity to the MCP tool call context. The MCP server
would extract it and override `CallerName` for that specific operation. This
avoids creating N supervisors but requires plumbing the caller identity through
the MCP protocol.

### Option C: Thread-Local / Request-Scoped Identity

Use a request-scoped mechanism (e.g., context.Value) to pass the caller's
identity from the MCP bridge to the supervisor for each tool call.

**Option A** is cleanest architecturally — each agent should have its own
supervisor instance with correct identity. The supervisor is lightweight
(no goroutines, just dependency holders) so creating one per agent is cheap.

## Reflections

### Surprising findings
- The bug is architectural, not behavioral. The agents and prompts are doing the
  right thing — the shared supervisor makes it impossible for managers to function
  as managers.
- Messages are also affected (`r.callerName` is always "weave"), meaning managers
  can't even communicate as themselves.
- The parent check in merge.go actually works as designed — it correctly prevents
  unauthorized merges. The problem is that the identity is wrong.

### Open questions
- Were there error messages from managers trying to merge that got swallowed or
  ignored? (Activity logs are gone, so we can't check.)
- Did managers try CLI fallback (`sprawl merge` via Bash)? If so, that would
  have worked correctly (CLI uses `os.Getenv` which returns the real identity).
  But the TUI prompt steers agents toward MCP tools.
- Is the messaging identity issue causing other problems beyond Wave 1?

### What I'd investigate next
- Check if any other supervisor operations are affected (retire, delegate, kill)
- Verify that the `baseBranch` in spawn (line 116 of spawn.go) is correct —
  children branch off SPRAWL_ROOT's current branch, not the parent's integration
  branch. This might be by design (latest shared state) or another issue.
- Look at whether the runtime launcher's `buildRunnerDeps` (which does set
  per-agent identity for the agent loop) could be leveraged for MCP identity too.
- Test the fix with a sandbox Wave 2 deployment.
