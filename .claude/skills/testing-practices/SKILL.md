# Testing Practices

## Running Tests

Run all tests:

```bash
go test ./...
```

Run tests for a specific package:

```bash
go test ./cmd/...
go test ./internal/state/...
go test ./internal/tmux/...
go test ./internal/agent/...
go test ./internal/worktree/...
```

Run a specific test by name:

```bash
go test ./cmd/... -run TestSpawn_HappyPath
go test ./cmd/... -run TestKill
```

Use `-v` for verbose output:

```bash
go test -v ./...
```

## Dependency Injection Testing Pattern

This codebase uses a **struct-based dependency injection** pattern for testing CLI commands. Each command defines a `*Deps` struct that holds all external dependencies as fields — including interfaces, function values, and closures. The production code path wires in real implementations, while tests inject mocks.

### How it works

1. **Define a deps struct** for the command (e.g., `spawnDeps`, `killDeps`, `retireDeps`):

   ```go
   type killDeps struct {
       tmuxRunner   tmux.Runner       // interface
       getenv       func(string) string  // function value
       signalFunc   func(int, syscall.Signal) error
       sleepFunc    func(time.Duration)
       processAlive func(int) bool
   }
   ```

2. **The command's `run*` function** accepts the deps struct instead of calling globals directly:

   ```go
   func runKill(deps *killDeps, agentName string, force bool) error {
       // uses deps.tmuxRunner, deps.getenv, etc.
   }
   ```

3. **Tests create a helper** that builds the deps with mocks (e.g., `newTestKillDeps`, `newTestSpawnDeps`, `newTestRetireDeps`):

   ```go
   func newTestKillDeps(t *testing.T) (*killDeps, *killMockRunner, string, []int) {
       t.Helper()
       tmpDir := t.TempDir()
       runner := &killMockRunner{}
       deps := &killDeps{
           tmuxRunner: runner,
           getenv: func(key string) string { ... },
           signalFunc: func(pid int, sig syscall.Signal) error { ... },
           sleepFunc:  func(d time.Duration) {},           // no-op in tests
           processAlive: func(pid int) bool { return false },
       }
       return deps, runner, tmpDir, signaled
   }
   ```

4. **Mock structs** implement interfaces and record calls for assertions:

   ```go
   type killMockRunner struct {
       hasSession       bool
       killWindowCalled bool
       killWindowErr    error
       killWindowSession string
       killWindowWindow  string
       // ...
   }
   ```

### Key interfaces mocked in tests

- **`tmux.Runner`** — controls tmux sessions/windows (`HasSession`, `NewSession`, `NewSessionWithWindow`, `NewWindow`, `KillWindow`, `ListWindowPIDs`, `Attach`)
- **`agent.Launcher`** — finds Claude binary and builds args (`FindBinary`, `BuildArgs`)
- **`worktree.Creator`** — creates git worktrees (`Create`)

### What gets injected as function values (not interfaces)

- `getenv` — replaces `os.Getenv` so tests control environment variables
- `signalFunc` — replaces `syscall.Kill` so tests don't send real signals
- `sleepFunc` — replaces `time.Sleep` so tests run instantly
- `processAlive` — replaces process-existence checks
- `worktreeRemove` — replaces real `git worktree remove`
- `gitStatus` — replaces real `git status` checks
- `currentBranch` — replaces real `git branch` detection

### Test file conventions

- Each command file `cmd/foo.go` has a corresponding `cmd/foo_test.go`
- Mock types are defined at the top of each test file (e.g., `spawnMockRunner`, `killMockRunner`)
- Helper constructors follow the pattern `newTest<Command>Deps(t *testing.T)`
- Tests use `t.TempDir()` for isolated filesystem state
- The `state` package is used directly (not mocked) — tests create real state files in temp dirs

## Manual CLI Validation

Build the binary:

```bash
make build
```

This produces a `./sprawl` binary. Common commands to test:

```bash
# Initialize the root agent session
./sprawl init

# Spawn a child agent
./sprawl spawn engineering engineer "implement feature X"

# Kill an agent (graceful SIGTERM then SIGKILL)
./sprawl kill alice

# Force-kill an agent (immediate SIGKILL)
./sprawl kill --force alice

# Retire an agent (kill + remove worktree + delete state)
./sprawl retire alice

# Retire with cascade (retire agent and all descendants)
./sprawl retire --cascade alice
```

## Validating Agent Behavior

When testing the full system (not unit tests), verify these artifacts:

### tmux sessions

```bash
# List all tmux sessions
tmux list-sessions

# List windows in a session
tmux list-windows -t sprawl-root

# Attach to observe an agent
tmux attach -t sprawl-root
```

### Agent state files

```bash
# State files live in .sprawl/agents/
ls .sprawl/agents/
cat .sprawl/agents/alice.json

# Each JSON file contains: name, type, family, parent, prompt, branch,
# worktree path, tmux session/window, status, and timestamps
```

### Git worktrees

```bash
# Worktrees are created under .sprawl/worktrees/<agent-name>/
ls .sprawl/worktrees/

# Each agent works on branch sprawl/<agent-name>
git worktree list

# Check for uncommitted changes in an agent's worktree
git -C .sprawl/worktrees/alice status
```

## Testing Pyramid

### Unit tests (fast, isolated, mocked)

The bulk of testing happens here. All external dependencies (tmux, git, filesystem, process signals) are mocked via the dependency injection pattern described above. These tests verify:

- Happy-path logic for each command
- Error handling (missing env vars, exhausted name pool, tmux failures, worktree failures)
- State transitions (active → killed, active → retiring → deleted)
- Edge cases (already-killed agents, agents with children, dirty worktrees)
- Signal sequencing (SIGTERM before SIGKILL, force skip)

Run with: `go test ./...`

### Integration-style tests (use real `state` package)

The command tests use the real `state` package to read/write JSON files in `t.TempDir()`. This validates that state serialization and file operations work correctly end-to-end, without needing to mock the filesystem.

### Manual validation (tmux + git + real agents)

Full system behavior — agent spawning, tmux session management, worktree lifecycle, inter-agent communication — must be validated manually. These involve real tmux processes, real git operations, and real Claude Code instances, which cannot be meaningfully unit-tested.

## Common Pitfalls

### Don't test with real tmux or git in unit tests

The mock pattern exists specifically to avoid depending on tmux being installed or git repos being available in CI. If your test calls real `tmux` or `git` commands, it will:

- Fail in environments without tmux
- Create real sessions/worktrees that pollute the system
- Be slow and flaky

Always use the deps struct to inject mocks.

### Mock structs must implement the full interface

When creating a new mock (e.g., for `tmux.Runner`), you must implement **every method** in the interface, even ones your specific test doesn't exercise. Unimplemented methods should return zero values. See `spawnMockRunner` and `killMockRunner` for examples.

### Use `t.Helper()` in test setup functions

All `newTest*Deps` helpers call `t.Helper()` so that test failure messages point to the actual test function, not the helper.

### Use `t.TempDir()` for state isolation

Never write state files to a shared directory. Each test gets its own temp dir via `t.TempDir()`, which is automatically cleaned up.

### Function values vs interfaces

Use interfaces when the dependency has multiple related methods (like `tmux.Runner`). Use function values when the dependency is a single operation (like `getenv`, `signalFunc`, `sleepFunc`). This keeps mocks simple — you don't need a struct just to mock `os.Getenv`.

### The `state` package is intentionally NOT mocked

Tests use the real `state.SaveAgent`/`state.LoadAgent` functions against temp directories. This gives confidence that JSON serialization works correctly without adding a layer of indirection.

### Watch for mock runner reuse across tests

Each test file defines its own mock runner type (e.g., `spawnMockRunner` vs `killMockRunner`) because different commands use different subsets of the `tmux.Runner` interface. The `retireMockRunner` is a type alias for `killMockRunner` since they need the same methods.
