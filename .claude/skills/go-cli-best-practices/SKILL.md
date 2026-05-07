# Go CLI Best Practices for Sprawl

Reference skill for AI agents working in this Go CLI codebase. Covers project structure, cobra patterns, testing, and error handling as used in this repo.

---

## Project Structure

This repo follows the standard Go CLI layout:

```
main.go          # Entry point — calls cmd.Execute()
cmd/             # All cobra commands (one file per command + tests)
  root.go        # Root command definition + Execute()
  retire.go      # Sub-command CLI wiring (resolveDeps + RunE)
  retire_test.go # Tests for that command
internal/        # Internal packages (not importable by external code)
  agent/         # Agent name allocation, prompt building
  agentops/      # Reusable per-command business logic (Spawn, Retire, Kill, Merge, Report)
  agentloop/     # Per-runtime turn queue & activity feed
  backend/       # Pluggable LLM backend adapter (claude/, fakes for tests)
  supervisor/    # Same-process child-runtime registry & lifecycle
  runtime/       # Unified per-agent runtime instances
  state/         # Agent state persistence (JSON in .sprawl/agents/)
  messages/      # Maildir-style inter-agent message store
  worktree/      # Git worktree creation
```

**Key conventions:**
- One command per file in `cmd/`
- Each command file has a matching `_test.go`
- Business logic lives in `internal/` packages — most per-command logic now lives in `internal/agentops/` and the `cmd/` file is a thin shim that re-exports the deps type and calls into agentops
- The `cmd` package wires dependencies together

---

## Cobra Command Patterns

Library: [github.com/spf13/cobra](https://github.com/spf13/cobra) — see [Cobra User Guide](https://github.com/spf13/cobra/blob/main/site/content/user_guide.md)

### Root Command (`cmd/root.go`)

```go
var rootCmd = &cobra.Command{
    Use:   "sprawl",
    Short: "Tree-governance for AI agents",
    Long:  "Sprawl — a self-organizing AI agent orchestration system.",
}

func Execute() {
    if err := rootCmd.Execute(); err != nil {
        fmt.Fprintln(os.Stderr, err)
        os.Exit(1)
    }
}
```

### Sub-Commands — Always Use `RunE`

Always use `RunE` (not `Run`) so errors propagate to cobra's error handling instead of requiring manual `os.Exit`:

```go
// cmd/retire.go
var retireCmd = &cobra.Command{
    Use:   "retire <agent-name>",
    Short: "Deprecated offline cleanup; use sprawl enter + retire for live runtimes",
    Long:  "When no weave session is running, fully retire an agent by removing its persisted state and worktree artifacts. If `sprawl enter` is active, use the retire MCP tool from the live weave session instead.",
    Args:  cobra.ExactArgs(1),
    RunE: func(_ *cobra.Command, args []string) error {
        return runRetire(resolveRetireDeps(), args[0], retireCascade, retireForce, retireAbandon, retireMerge, retireYes)
    },
}
```

### Registering Commands with `init()`

```go
func init() {
    retireCmd.Flags().BoolVar(&retireCascade, "cascade", false, "Retire agent and all descendants bottom-up")
    retireCmd.Flags().BoolVar(&retireForce, "force", false, "Skip dirty worktree check and orphan children")
    retireCmd.Flags().BoolVar(&retireMerge, "merge", false, "Merge the agent's work into your branch before retiring")
    rootCmd.AddCommand(retireCmd)
}
```

### Positional Args Validation

Use cobra's built-in validators for positional args (real example: `cmd/retire.go`):

```go
var retireCmd = &cobra.Command{
    Use:  "retire <agent-name>",
    Args: cobra.ExactArgs(1),   // Enforces exactly 1 positional arg
    RunE: func(_ *cobra.Command, args []string) error {
        return runRetire(resolveRetireDeps(), args[0], retireCascade, retireForce, retireAbandon, retireMerge, retireYes)
    },
}
```

Common validators ([cobra docs](https://pkg.go.dev/github.com/spf13/cobra#PositionalArgs)):
- `cobra.NoArgs` — command takes no args
- `cobra.ExactArgs(n)` — exactly n args
- `cobra.MinimumNArgs(n)` — at least n args
- `cobra.MaximumNArgs(n)` — at most n args
- `cobra.RangeArgs(min, max)` — between min and max

### Flags — Package-Level Vars

This codebase stores flag values in package-level vars, bound in `init()` (real example: `cmd/retire.go`):

```go
var (
    retireCascade bool
    retireForce   bool
    retireMerge   bool
    retireYes     bool
)

func init() {
    retireCmd.Flags().BoolVar(&retireCascade, "cascade", false, "Retire agent and all descendants bottom-up")
    retireCmd.Flags().BoolVar(&retireForce, "force", false, "Skip dirty worktree check and orphan children")
    retireCmd.Flags().BoolVar(&retireMerge, "merge", false, "Merge the agent's work into your branch before retiring")
    rootCmd.AddCommand(retireCmd)
}
```

Flag types: `StringVar`, `BoolVar`, `IntVar`, `StringSliceVar`, etc.
See [pflag docs](https://pkg.go.dev/github.com/spf13/pflag) for the full list.

---

## Dependency Injection for Testability

**This is the most important pattern in this codebase.** Every command separates:

1. **A deps struct** — holds all external dependencies, almost always as **function values** (closures); reach for an interface only when the collaborator is stateful or has multiple methods (`worktree.Creator`, `backend.Adapter`, `supervisor.Supervisor`).
2. **A resolve function** — creates real deps from `os.Getenv` and the real git/state wrappers exported from `internal/agentops`.
3. **A run function** — pure business logic that takes deps as first arg. After QUM-400, that logic typically lives in `internal/agentops/` and the `cmd/` file is a thin shim.

Real example: `cmd/retire.go` + `internal/agentops/retire.go`.

```go
// 1. Deps struct lives in internal/agentops and is re-exported in cmd/.
//    See internal/agentops/retire.go::RetireDeps.
type RetireDeps struct {
    Getenv              func(string) string
    WorktreeRemove      func(repoRoot, worktreePath string, force bool) error
    GitStatus           func(worktreePath string) (string, error)
    RemoveAll           func(string) error
    GitBranchDelete     func(repoRoot, branchName string) error
    GitBranchIsMerged   func(repoRoot, branchName string) (bool, error)
    GitBranchSafeDelete func(repoRoot, branchName string) error
    DoMerge             func(cfg *merge.Config, deps *merge.Deps) (*merge.Result, error)
    NewMergeDeps        func() *merge.Deps
    LoadAgent           func(sprawlRoot, name string) (*state.AgentState, error)
    CurrentBranch       func(repoRoot string) (string, error)
    // ...
}

// In cmd/retire.go we just alias the type so existing tests keep their
// names (`*retireDeps`).
type retireDeps = agentops.RetireDeps

// 2. Resolve — wires real implementations from agentops's exported helpers.
//    See cmd/retire.go::resolveRetireDeps for the full thing.
func resolveRetireDeps() *retireDeps {
    return &retireDeps{
        Getenv:              os.Getenv,
        WorktreeRemove:      realWorktreeRemove,    // = agentops.RealWorktreeRemove
        GitStatus:           realGitStatus,         // = agentops.RealGitStatus
        RemoveAll:           os.RemoveAll,
        GitBranchDelete:     realGitBranchDelete,
        GitBranchIsMerged:   realGitBranchIsMerged,
        GitBranchSafeDelete: realGitBranchSafeDelete,
        DoMerge:             merge.Merge,
        LoadAgent:           state.LoadAgent,
        CurrentBranch:       gitCurrentBranch,
        // ...
    }
}

// 3. Run — testable business logic in agentops, called via a thin cmd/ shim.
//    See internal/agentops/retire.go::Retire and cmd/retire.go::runRetire.
func Retire(deps *RetireDeps, agentName string, cascade, force, abandon, mergeFirst, yes, noValidate bool) error {
    // All external calls go through deps.
}
```

The cobra `RunE` just wires it together:

```go
RunE: func(_ *cobra.Command, args []string) error {
    return runRetire(resolveRetireDeps(), args[0], retireCascade, retireForce, retireAbandon, retireMerge, retireYes)
},
```

For the simplest possible shape (no `agentops/` indirection, no interfaces, just an `io.Writer` and `getenv`), see `cmd/messages.go::messagesDeps` and its `resolveMessagesDeps`. For a deps struct that does pull in interfaces — `worktree.Creator` and a backend `RunScript` — see `internal/agentops/spawn.go::SpawnDeps`.

---

## Testing Patterns

This codebase uses **stdlib `testing` only** (no testify). Docs: [Go testing package](https://pkg.go.dev/testing)

### Test Helper for Deps Setup

Use `t.Helper()` in shared setup functions. Real example: `cmd/retire_test.go::newTestRetireDeps`.

```go
func newTestRetireDeps(t *testing.T) (*retireDeps, string) {
    t.Helper()
    tmpDir := t.TempDir()  // Auto-cleaned up
    deps := &retireDeps{
        Getenv: func(key string) string {
            if key == "SPRAWL_ROOT" {
                return tmpDir
            }
            return ""
        },
        WorktreeRemove:    func(repoRoot, worktreePath string, force bool) error { return os.RemoveAll(worktreePath) },
        GitStatus:         func(string) (string, error) { return "", nil },
        RemoveAll:         os.RemoveAll,
        GitBranchDelete:   func(repoRoot, branchName string) error { return nil },
        GitBranchIsMerged: func(repoRoot, branchName string) (bool, error) { return false, nil },
        DoMerge:           func(*merge.Config, *merge.Deps) (*merge.Result, error) { return &merge.Result{}, nil },
        NewMergeDeps:      func() *merge.Deps { return &merge.Deps{} },
        LoadAgent:         state.LoadAgent,
        CurrentBranch:     func(string) (string, error) { return "main", nil },
        // ...
    }
    return deps, tmpDir
}
```

### Override fields per-test rather than maintaining mock structs

The idiomatic style is: get the default deps from the helper, then mutate just the field that drives the scenario under test.

```go
func TestRetire_DirtyWorktree_Refuses(t *testing.T) {
    deps, tmpDir := newTestRetireDeps(t)
    deps.GitStatus = func(string) (string, error) { return "M file.go", nil }
    // ...
}
```

Mock structs only show up where deps takes an interface — see `cmd/mocks_test.go` for the shared `worktree.Creator` and `agent.Launcher` fakes used by `cmd/spawn_test.go`.

### Individual Test Functions (Not Table-Driven)

This codebase uses **one function per test case**, not table-driven tests. Each test is self-contained:

```go
func TestRetire_HappyPathDeletesState(t *testing.T) {
    deps, tmpDir := newTestRetireDeps(t)
    // ... seed agent state via state.SaveAgent ...
    if err := runRetire(deps, "alice", false, false, false, false, false); err != nil {
        t.Fatalf("runRetire() error: %v", err)
    }
    if _, err := state.LoadAgent(tmpDir, "alice"); err == nil {
        t.Fatal("expected agent state to be deleted")
    }
}

func TestRetire_InvalidAgentNameReturnsError(t *testing.T) {
    deps, _ := newTestRetireDeps(t)
    err := runRetire(deps, "../evil", false, false, false, false, false)
    if err == nil {
        t.Fatal("expected invalid agent name error")
    }
    if !strings.Contains(err.Error(), "invalid agent name") {
        t.Fatalf("error = %q, want invalid agent name", err)
    }
}
```

### Test Naming Convention

`Test<Command>_<Scenario>` — e.g., `TestRetire_HappyPathDeletesState`, `TestRetire_DirtyWorktree_Refuses`, `TestMessagesSend_HappyPath`.

### Error Assertions

Use `strings.Contains` on `err.Error()` to check error messages:

```go
if !strings.Contains(err.Error(), "SPRAWL_ROOT") {
    t.Errorf("error should mention SPRAWL_ROOT, got: %v", err)
}
```

### Running Tests

```bash
go test ./...              # All tests
go test ./cmd/             # Just cmd package
go test -v ./cmd/ -run TestSpawn  # Verbose, matching pattern
go test -race ./...        # With race detector
```

Docs: [Go testing](https://pkg.go.dev/testing), [go test flags](https://pkg.go.dev/cmd/go/internal/test)

---

## Error Handling Patterns

Reference: [Go blog: Error handling and Go](https://go.dev/blog/error-handling-and-go), [Go blog: Working with Errors in Go 1.13](https://go.dev/blog/go1.13-errors)

### Error Wrapping with `%w`

Always wrap errors with context using `fmt.Errorf` and `%w`:

```go
// internal/agentops/kill.go
if err := state.SaveAgent(sprawlRoot, agentState); err != nil {
    return fmt.Errorf("updating agent state: %w", err)
}

// internal/agentops/spawn.go
if err := os.MkdirAll(agentsDir, 0o755); err != nil {
    return nil, fmt.Errorf("creating agents directory: %w", err)
}
```

The `%w` verb wraps the error so callers can use `errors.Is()` and `errors.As()` to inspect the chain. See [fmt.Errorf docs](https://pkg.go.dev/fmt#Errorf).

### Context-Only Errors (No Wrapping)

When the original error isn't useful to callers, create a new descriptive error (real examples from `internal/agentops/spawn.go` and `cmd/report.go`):

```go
return fmt.Errorf("SPRAWL_ROOT environment variable is not set")
return fmt.Errorf("SPRAWL_AGENT_IDENTITY environment variable is not set; report must be called from within a sprawl agent")
```

### Validation Errors

Return descriptive errors with the invalid value and valid options:

```go
return fmt.Errorf("invalid agent type %q; valid types: %v", agentType, validTypes)
return fmt.Errorf("agent type %q is not yet supported; currently supported: engineer", agentType)
```

### Idempotent Operations — Warn, Don't Error

For operations that are safe to repeat, log a warning and return nil:

```go
if agentState.Status == "killed" {
    fmt.Fprintf(os.Stderr, "Warning: agent %q is already killed\n", agentName)
    return nil
}
```

### Best-Effort Cleanup — Discard Errors

For cleanup that shouldn't fail the operation (real example from `cmd/retire.go`):

```go
defer func() { _ = lock.Release() }()
```

### Error Handling Decision Tree

1. **Can the caller recover?** → Wrap with `%w` so they can inspect
2. **Is it a validation error?** → Include the bad value and valid options
3. **Is the original error useless?** → Create a new descriptive error (no `%w`)
4. **Is it cleanup?** → Discard with `_ =`
5. **Is retrying safe?** → Warn and return nil

---

## Subprocess stdio: TTY vs pipe

**Hazard:** When you launch a child process with `os/exec`, the value you assign to `cmd.Stdout` / `cmd.Stderr` determines whether the child sees a TTY or a pipe on that file descriptor. Getting this wrong silently changes the child's behavior.

### The `os/exec` rule

From the [`os/exec` docs](https://pkg.go.dev/os/exec#Cmd): if `Stdout` or `Stderr` is an `*os.File`, the corresponding file descriptor of the child process is set directly to that file. Otherwise, Go allocates an anonymous pipe, hands the write end to the child, and copies from the read end into your writer in a goroutine.

Short version:

- `cmd.Stdout = os.Stdout` → child's fd 1 **is** the terminal (TTY passthrough).
- `cmd.Stdout = someIoWriter` (bytes.Buffer, tee, markerWriter, io.MultiWriter, …) → child's fd 1 is an **anonymous pipe**. Not a TTY.

This is true even for `io.MultiWriter(os.Stdout, buf)` — a MultiWriter is not an `*os.File`, so the child gets a pipe.

### TTY-sensitive children

Many modern CLIs gate behavior on `isatty(fd)`:

- **Claude Code ≥2.1** — if stdout is not a TTY, silently switches into `--print` (non-interactive) mode. Requires a prompt on argv/stdin and exits on stdin EOF. Completely different UX.
- **TUIs** (bubble tea, ink, etc.) — usually refuse to start, or fall back to a dumb line-mode.
- **`git`, `less`, `ls`** — drop color, disable pagers, change output format.
- **`ssh`** — requires `-t` to force a PTY when stdin/stdout aren't terminals.

If you wrap stdout to scan or tee, the child sees a pipe and its behavior changes underneath you.

### Guidance

1. **Default to `os.Stdout` / `os.Stderr` directly** for any interactive or potentially-TTY-sensitive child. This is the only way the child keeps a real terminal on its stdio.
2. **If you must intercept output, wrap stderr, not stdout.** Most tools only TTY-check stdout. Error/log scanning on stderr is typically safe. (`RunWithResumeWatch` in `internal/claude/resumewatch.go` does exactly this — it wraps stderr with a marker scanner and leaves stdout untouched.)
3. **If you must capture or intercept stdout of a TTY-sensitive child, use a PTY.** [`github.com/creack/pty`](https://github.com/creack/pty) lets you allocate a pseudo-terminal; pass the PTY's slave end as `cmd.Stdout` (still an `*os.File`, so os/exec doesn't pipe it) and read from the master end. The child sees a TTY, you still get the bytes.
4. **Never pass `io.MultiWriter(os.Stdout, …)` to a TTY-sensitive child** thinking it "just tees to the terminal". It silently downgrades the child's fd 1 to a pipe.

### Concrete example — QUM-261

`RunWithResumeWatch` originally wrapped `cmd.Stdout` with a markerWriter to scan for a "no conversation" string. Go's `os/exec` promoted the writer to an anonymous pipe. Claude Code saw a non-TTY stdout, auto-flipped to `--print` mode, and `sprawl init` bricked: Claude exited immediately on EOF without ever showing the interactive onboarding screen.

Fix (commit `7c801f5`): move the marker scanner to stderr, leave `cmd.Stdout` as the inherited `os.Stdout`. The marker string is stderr-only anyway, so stdout scanning was wrong from the start — the TTY regression just made it loud.

Follow-up documentation: QUM-308 (this section) and the hazard comment in `internal/claude/resumewatch.go`.

---

## Go Module & Dependencies

- **Module path:** `github.com/dmotles/sprawl`
- **Go version:** 1.25.0
- **Dependencies:**
  - `github.com/spf13/cobra v1.10.2` — CLI framework
  - `github.com/spf13/pflag v1.0.9` — Flag parsing (transitive, used by cobra)
  - `github.com/inconshreveable/mousetrap v1.1.0` — Windows support (transitive)

### Useful Commands

```bash
go mod tidy          # Clean up go.mod/go.sum
go mod download      # Download dependencies
go build ./...       # Build all packages
go vet ./...         # Static analysis
```

---

## Quick Reference Links

- [Cobra GitHub](https://github.com/spf13/cobra)
- [Cobra User Guide](https://github.com/spf13/cobra/blob/main/site/content/user_guide.md)
- [cobra.Command docs](https://pkg.go.dev/github.com/spf13/cobra#Command)
- [pflag docs](https://pkg.go.dev/github.com/spf13/pflag)
- [Go testing package](https://pkg.go.dev/testing)
- [Go errors package](https://pkg.go.dev/errors)
- [fmt.Errorf](https://pkg.go.dev/fmt#Errorf)
- [Effective Go](https://go.dev/doc/effective_go)
- [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments)
