# Research: Tmux Elimination — Subprocess-Based Child Agent Spawning

**Date:** 2026-04-28  
**Agent:** ghost  
**Task:** QUM-195 (parent), QUM-314 (tracker)

---

## Executive Summary

Tmux is **not** dead in sprawl. It remains the process container for ALL child agents even in TUI mode. When `spawn` fires, the current path is:

```
spawn MCP call
  → supervisor.Real.Spawn()
  → agentops.Spawn()
  → deps.TmuxRunner.NewWindow(session, name, env, shellCmd)
  → tmux window running: sprawl agent-loop <name>
```

The infrastructure to fix this **already exists** in the codebase. `internal/agentloop/process.go` (`Process` struct) already manages a Claude Code subprocess via stdin/stdout pipes — no PTY, no tmux — and this is exactly how weave (the root agent) runs in TUI mode. Extending it to child agents completes the original QUM-195 goal of "all agents are goroutine-supervised subprocesses."

---

## 1. Current State: Where tmux Lives

### 1.1 `internal/agentops/spawn.go` — The Core Problem

`agentops.Spawn()` (line 228) calls:

```go
if err := deps.TmuxRunner.NewWindow(childrenSession, agentName, env, shellCmd); err != nil {
    // try session creation...
    if err := deps.TmuxRunner.NewSessionWithWindow(childrenSession, agentName, env, shellCmd); err != nil {
        // retry...
    }
}
```

The `shellCmd` is:
```go
shellCmd := fmt.Sprintf("cd %s && %s", tmux.ShellQuote(worktreePath), tmux.BuildShellCmd(sprawlPath, []string{"agent-loop", agentName}))
```

This creates a tmux window running `sprawl agent-loop <name>` in the agent's worktree.

### 1.2 `internal/agentops/kill.go` — Kill Uses tmux

`Kill()` calls `agent.GracefulShutdown()` which uses `TmuxRunner` to send signals to the agent's tmux window. The `KillDeps` struct has a `TmuxRunner` field.

### 1.3 `internal/agentops/retire.go` — Retire Uses tmux

`RetireDeps` also has a `TmuxRunner` field used for killing the agent process.

### 1.4 `internal/supervisor/real.go:67` — tmux Required at Startup

```go
func NewReal(cfg Config) (*Real, error) {
    tmuxPath, err := tmux.FindTmux()
    if err != nil {
        return nil, fmt.Errorf("tmux is required but not found")
    }
    tmuxRunner := &tmux.RealRunner{TmuxPath: tmuxPath}
    // ...
    spawnDeps: &agentops.SpawnDeps{
        TmuxRunner: tmuxRunner,
        // ...
    },
    killDeps: &agentops.KillDeps{
        TmuxRunner: tmuxRunner,
        // ...
    },
```

This means **tmux must be installed** even when using `sprawl enter` (TUI mode). The supervisor refuses to start without it.

### 1.5 `internal/state/AgentState` — tmux Fields in State

```go
type AgentState struct {
    // ...
    TmuxSession string `json:"tmux_session"`
    TmuxWindow  string `json:"tmux_window"`
    // ...
}
```

These fields are written by `agentops.Spawn()` and read by kill/retire. They become unused after the process-container migration.

---

## 2. What Already Exists (The Good News)

### 2.1 `internal/agentloop/process.go` — Process Manager (No tmux)

The `Process` struct manages a Claude Code subprocess via stdin/stdout pipes with **no PTY, no tmux**:

```go
type Process struct {
    config   ProcessConfig
    starter  CommandStarter
    writer   MessageWriter
    waitFn   WaitFunc
    cancelFn CancelFunc
    // ...
}

func (p *Process) Launch(ctx context.Context) error { ... }
func (p *Process) SendPrompt(ctx context.Context, prompt string) (*protocol.ResultMessage, error) { ... }
func (p *Process) Kill() error { ... }
func (p *Process) Stop(_ context.Context) error { ... }
```

This is used today for the weave root agent in TUI mode (`cmd/enter.go`).

### 2.2 `internal/agentloop/real_starter.go` — Subprocess Launcher

`RealCommandStarter.Start()` launches `claude` directly using `exec.CommandContext`, with `StdinPipe()`/`StdoutPipe()` — no PTY. Already proven to work.

### 2.3 `cmd/agentloop.go` — Full Agent Loop Logic

The `runAgentLoop()` function handles everything a child agent needs:
- Claude Code subprocess startup
- Initial prompt delivery
- Work lock (sync with merge operations)
- Kill sentinel file polling (`<name>.kill`)
- Wake file polling (`<name>.wake`)
- Task queue processing
- Message delivery (async and interrupt)
- Crash recovery with `--resume`
- Signal handling (SIGTERM/SIGINT)

**Key insight:** The kill sentinel file mechanism (`<name>.kill`) means `Kill()` doesn't need tmux to stop a child. Writing the sentinel file is sufficient. The agent-loop polls for it every iteration.

### 2.4 PTY Not Required

The research in `docs/research/claude-stream-json-protocol.md` confirms: Claude Code works over plain pipes in `--input-format stream-json --output-format stream-json` mode. There's a note about stdout buffering (4-8KB buffer when piped), but the `result` and `session_state_changed` messages flush reliably. This is fine for agent operation.

---

## 3. The Subprocess Spawner Design

To eliminate tmux as a process container, `agentops.Spawn()` needs to replace:

```go
deps.TmuxRunner.NewWindow(childrenSession, agentName, env, shellCmd)
```

with:

```go
cmd := exec.Command(sprawlPath, "agent-loop", agentName)
cmd.Dir = worktreePath
cmd.Env = buildEnv(env)
cmd.Stdout = os.Stdout  // or a pipe to TUI observer
cmd.Stderr = os.Stderr
err = cmd.Start()
// store cmd.Process.Pid in agentState
```

The child process runs the same `sprawl agent-loop <name>` command — just as a direct Go subprocess instead of inside a tmux window.

### 3.1 PID Tracking

The spawned process PID needs to be:
1. Stored in `AgentState` (new field: `ProcessPID int`) for persistence across supervisor restarts
2. Held in-memory in the supervisor for fast access

This enables `Kill()` to send `SIGTERM` directly:
```go
// Write sentinel (agent-loop checks this)
os.WriteFile(killPath, []byte("kill"), 0644)
// Also send SIGTERM if PID is known
if agentState.ProcessPID > 0 {
    syscall.Kill(agentState.ProcessPID, syscall.SIGTERM)
}
```

### 3.2 Process Supervision

The supervisor needs a goroutine that `Wait()`s on each child process and handles exits:
- Normal exit: agent called `sprawl report done` and loop exited cleanly
- Crash: restart with `--resume` (the agent-loop already does this internally)
- Kill: SIGTERM sent by supervisor

### 3.3 Concurrency

Multiple child agents run as concurrent Go subprocesses. Each has its own `*exec.Cmd` handle. The `os.Process` handles are goroutine-safe. The supervisor can maintain:

```go
type ChildProcess struct {
    Cmd *exec.Cmd
    Done <-chan error
}

// In-memory registry in supervisor
childProcs map[string]*ChildProcess  // keyed by agent name
mu sync.Mutex
```

---

## 4. Phased Plan

The plan is designed so each phase is independently verifiable (user can confirm no new tmux windows after Phase 1).

### Phase 1: Subprocess Spawn (Replace tmux.NewWindow)

**Key change:** `agentops.Spawn()` replaces `TmuxRunner.NewWindow()` with `exec.Command("sprawl agent-loop <name>")`.

**Files to change:**
- `internal/agentops/spawn.go` — replace tmux window creation with subprocess launch
- `internal/agentops/spawn_deps.go` (or modify `SpawnDeps`) — replace `TmuxRunner` with a `SubprocessRunner` interface
- `internal/state/state.go` — add `ProcessPID int` field to `AgentState`
- `internal/supervisor/real.go` — wire new subprocess deps

**Verification:** Spawn an agent via `sprawl enter` → `spawn`, confirm:
1. No new tmux windows: `tmux list-windows` shows only the weave window
2. New process visible: `ps aux | grep "sprawl agent-loop"`
3. Agent functions normally (receives messages, reports status)

### Phase 2: Subprocess Kill/Retire (Remove TmuxRunner from kill/retire)

**Key change:** `Kill()` writes sentinel file + sends SIGTERM via PID. No tmux needed.

**Files to change:**
- `internal/agentops/kill.go` — remove `TmuxRunner`, use PID-based kill
- `internal/agent/shutdown.go` (if exists) or `agent.GracefulShutdown` — remove tmux calls
- `internal/agentops/retire.go` — remove `TmuxRunner` from `RetireDeps`
- `internal/supervisor/real.go` — update kill/retire deps construction

**Verification:** `kill <agent>` terminates agent process, state shows "killed", no tmux interaction.

### Phase 3: Remove tmux from supervisor.NewReal()

**Key change:** Drop `tmux.FindTmux()` requirement from `NewReal()`.

**Files to change:**
- `internal/supervisor/real.go` — remove tmux.FindTmux() call and tmux.RealRunner construction

**Verification:** Remove tmux binary from PATH, run `sprawl enter`, spawn and kill agents — all work.

### Phase 4: Purge TmuxSession/TmuxWindow from AgentState

**Key change:** Remove unused tmux fields from `AgentState`.

**Files to change:**
- `internal/state/state.go` — remove `TmuxSession` and `TmuxWindow` fields
- All code that sets these fields in `agentops.Spawn()`
- Any code that reads these fields

**Verification:** Fresh agent state files contain no `tmux_session`/`tmux_window` fields. `make validate` passes.

### Phase 5: Delete internal/tmux package

**Key change:** Remove `internal/tmux/` package and tmux dependency from go.mod.

**Prerequisite:** All Phase 2.1–2.3 (tmux CLI removal) must be complete too, since `cmd/init.go` and other tmux-mode commands reference `internal/tmux`.

**Verification:** `make validate` passes with no `tmux` in imports or go.mod.

---

## 5. Subprocess Output Visibility

When child agents ran in tmux windows, users could `tmux attach` to see their output. With subprocess spawning:

- **Logs:** Agent output goes to `.sprawl/agents/<name>/logs/<session-id>.log` (already implemented in `cmd/agentloop.go`)
- **Activity ring:** `.sprawl/agents/<name>/activity.ndjson` (already implemented)
- **TUI observation:** `PeekActivity()` reads from activity.ndjson — already works
- **TUI agent panel:** When an agent is selected in the tree, the TUI reads from activity.ndjson — no tmux needed

No loss of observability. The log files are actually better than tmux scrollback (persistent, structured).

---

## 6. Subprocess vs. In-Process Agent Loop

An alternative design would run the agent loop **in-process** as goroutines rather than as subprocesses. This would be more integrated but significantly more complex:

**Pros of in-process:**
- Zero IPC overhead
- Simpler process lifecycle management
- Cleaner integration with TUI

**Cons of in-process:**
- `cmd/agentloop.go` (`runAgentLoop`) is 900+ lines with complex state — hard to refactor safely
- Claude Code subprocess management already isolated well in `agentloop.Process`
- In-process goroutines would need careful isolation (stdout, log files, error handling)
- `os.Exit(1)` calls in agent-loop would kill the whole weave process

**Recommendation:** Stick with subprocess approach. It's safer, preserves isolation, and the subprocess-based approach is already proven for weave. The subprocess spawner is a much smaller change than an in-process redesign.

---

## 7. Open Questions

1. **Process group management:** Should child agent subprocesses be in their own process group so SIGTERM propagates correctly to their Claude subprocess children? May need `cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}`.

2. **Output routing in TUI:** Currently child agent output goes to the tmux window (visible to users who `tmux attach`). With subprocess spawning, stdout/stderr go... where? Options: (a) `/dev/null` (rely on log files), (b) pipe to TUI for real-time streaming, (c) named pipe or socket. The activity ring covers most needs; real-time streaming via TUI would be a bonus.

3. **Process restart on crash:** The `cmd/agentloop.go` handles crash recovery internally (restarts Claude with `--resume`). But if the `sprawl agent-loop` subprocess itself crashes, the supervisor needs to restart it. This is a new responsibility that tmux provided implicitly (tmux doesn't restart crashed windows, but the crash is visible).

4. **Supervisor restart survivability:** If weave crashes and restarts (`/handoff`), child agent subprocesses become orphaned. The new supervisor needs to re-attach to running children by PID on restart. This requires storing PIDs in `AgentState` durably.

---

## 8. Reflection

**Surprising:** The `internal/agentloop/process.go` infrastructure is remarkably complete. The `Process` struct already has `Start()`, `Kill()`, `Stop()`, `InterruptTurn()` — everything needed to manage a subprocess. The work is mostly **plumbing** (wire subprocess launch into `agentops.Spawn()`) rather than net-new engineering.

**Also surprising:** `supervisor.NewReal()` fails hard if tmux is not installed (`return nil, fmt.Errorf("tmux is required but not found")`). This means today, running `sprawl enter` on a tmux-free machine would crash immediately even though weave itself doesn't use tmux. Phase 3 fixes this.

**Open question that matters most:** Output routing for child agents. Tmux provides a free scrollback terminal; subprocess stdout needs to go somewhere useful. The activity ring + log files cover observability, but there may be user expectations around seeing "live" child output. This should be validated with the user before implementing.

**What I'd investigate next:** Read `cmd/enter.go` `makeRestartFunc` more carefully to understand how weave handles supervisor-restart on handoff — this pattern will inform how child process handles get re-acquired after a restart.
