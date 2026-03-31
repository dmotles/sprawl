# Go CLI Best Practices for Dendrarchy

Reference skill for AI agents working in this Go CLI codebase. Covers project structure, cobra patterns, testing, and error handling as used in this repo.

---

## Project Structure

This repo follows the standard Go CLI layout:

```
main.go          # Entry point — calls cmd.Execute()
cmd/             # All cobra commands (one file per command + tests)
  root.go        # Root command definition + Execute()
  spawn.go       # Sub-command implementation
  spawn_test.go  # Tests for that command
internal/        # Internal packages (not importable by external code)
  agent/         # Agent launching, prompt building
  state/         # Agent state persistence
  tmux/          # Tmux session management
  worktree/      # Git worktree creation
```

**Key conventions:**
- One command per file in `cmd/`
- Each command file has a matching `_test.go`
- Business logic lives in `internal/` packages
- The `cmd` package wires dependencies together

---

## Cobra Command Patterns

Library: [github.com/spf13/cobra](https://github.com/spf13/cobra) — see [Cobra User Guide](https://github.com/spf13/cobra/blob/main/site/content/user_guide.md)

### Root Command (`cmd/root.go`)

```go
var rootCmd = &cobra.Command{
    Use:   "dendra",
    Short: "Tree-governance for AI agents",
    Long:  "Dendrarchy — a self-organizing AI agent orchestration system.",
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
var spawnCmd = &cobra.Command{
    Use:   "spawn",
    Short: "Spawn a new agent",
    Long:  "Spawn a new agent with the given family, type, and task prompt.",
    RunE: func(cmd *cobra.Command, args []string) error {
        deps, err := resolveSpawnDeps()
        if err != nil {
            return err
        }
        return runSpawn(deps, spawnFamily, spawnType, spawnPrompt)
    },
}
```

### Registering Commands with `init()`

```go
func init() {
    spawnCmd.Flags().StringVar(&spawnFamily, "family", "", "agent family")
    spawnCmd.Flags().BoolVar(&killForce, "force", false, "SIGKILL immediately")
    spawnCmd.MarkFlagRequired("family")
    rootCmd.AddCommand(spawnCmd)
}
```

### Positional Args Validation

Use cobra's built-in validators for positional args:

```go
var killCmd = &cobra.Command{
    Use:  "kill <agent-name>",
    Args: cobra.ExactArgs(1),   // Enforces exactly 1 positional arg
    RunE: func(cmd *cobra.Command, args []string) error {
        return runKill(deps, args[0], killForce)
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

This codebase stores flag values in package-level vars, bound in `init()`:

```go
var (
    spawnFamily string
    spawnType   string
    spawnPrompt string
)

func init() {
    spawnCmd.Flags().StringVar(&spawnFamily, "family", "", "agent family")
    spawnCmd.MarkFlagRequired("family")
}
```

Flag types: `StringVar`, `BoolVar`, `IntVar`, `StringSliceVar`, etc.
See [pflag docs](https://pkg.go.dev/github.com/spf13/pflag) for the full list.

---

## Dependency Injection for Testability

**This is the most important pattern in this codebase.** Every command separates:

1. **A deps struct** — holds all external dependencies (interfaces, func values)
2. **A resolve function** — creates real deps (with binary lookups, etc.)
3. **A run function** — pure business logic that takes deps as first arg

```go
// 1. Deps struct with interfaces and funcs
type spawnDeps struct {
    tmuxRunner      tmux.Runner        // interface
    claudeLauncher  agent.Launcher     // interface
    worktreeCreator worktree.Creator   // interface
    getenv          func(string) string // func for os.Getenv
    currentBranch   func(string) (string, error)
}

// 2. Resolve — creates real implementations
func resolveSpawnDeps() (*spawnDeps, error) {
    tmuxPath, err := tmux.FindTmux()
    if err != nil {
        return nil, fmt.Errorf("tmux is required but not found")
    }
    return &spawnDeps{
        tmuxRunner: &tmux.RealRunner{TmuxPath: tmuxPath},
        getenv:     os.Getenv,
        // ...
    }, nil
}

// 3. Run — testable business logic
func runSpawn(deps *spawnDeps, family, agentType, prompt string) error {
    // All external calls go through deps
}
```

The cobra `RunE` just wires it together:

```go
RunE: func(cmd *cobra.Command, args []string) error {
    deps, err := resolveSpawnDeps()
    if err != nil {
        return err
    }
    return runSpawn(deps, spawnFamily, spawnType, spawnPrompt)
},
```

---

## Testing Patterns

This codebase uses **stdlib `testing` only** (no testify). Docs: [Go testing package](https://pkg.go.dev/testing)

### Test Helper for Deps Setup

Use `t.Helper()` in shared setup functions:

```go
func newTestSpawnDeps(t *testing.T) (*spawnDeps, *spawnMockRunner, *mockWorktreeCreator, string) {
    t.Helper()
    tmpDir := t.TempDir()  // Auto-cleaned up

    runner := &spawnMockRunner{}
    deps := &spawnDeps{
        tmuxRunner: runner,
        getenv: func(key string) string {
            switch key {
            case "DENDRA_AGENT_IDENTITY":
                return "root"
            case "DENDRA_ROOT":
                return tmpDir
            }
            return ""
        },
        currentBranch: func(repoRoot string) (string, error) {
            return "main", nil
        },
    }
    return deps, runner, creator, tmpDir
}
```

### Mock Structs (Implement Interfaces)

Create mock structs that implement the same interfaces used in deps:

```go
type spawnMockRunner struct {
    hasSession                  bool
    newSessionWithWindowErr     error
    newSessionWithWindowCalled  bool
    newSessionWithWindowSession string
    // ... fields to record calls and return canned values
}

func (m *spawnMockRunner) HasSession(name string) bool {
    return m.hasSession
}
```

### Individual Test Functions (Not Table-Driven)

This codebase uses **one function per test case**, not table-driven tests. Each test is self-contained:

```go
func TestSpawn_HappyPath(t *testing.T) {
    deps, runner, creator, tmpDir := newTestSpawnDeps(t)
    err := runSpawn(deps, "engineering", "engineer", "implement login page")
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    // Assert on mock state...
}

func TestSpawn_InvalidType(t *testing.T) {
    deps, _, _, _ := newTestSpawnDeps(t)
    err := runSpawn(deps, "engineering", "foo", "task")
    if err == nil {
        t.Fatal("expected error for invalid type")
    }
    if !strings.Contains(err.Error(), "invalid agent type") {
        t.Errorf("error should mention 'invalid agent type', got: %v", err)
    }
}
```

### Test Naming Convention

`Test<Command>_<Scenario>` — e.g., `TestSpawn_HappyPath`, `TestKill_AlreadyKilled`, `TestSpawn_WorktreeCreationFails`

### Error Assertions

Use `strings.Contains` on `err.Error()` to check error messages:

```go
if !strings.Contains(err.Error(), "DENDRA_ROOT") {
    t.Errorf("error should mention DENDRA_ROOT, got: %v", err)
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
if err := state.SaveAgent(dendraRoot, agentState); err != nil {
    return fmt.Errorf("saving agent state: %w", err)
}

if err := deps.tmuxRunner.NewWindow(session, name, env, cmd); err != nil {
    return fmt.Errorf("creating tmux window for %s: %w", name, err)
}
```

The `%w` verb wraps the error so callers can use `errors.Is()` and `errors.As()` to inspect the chain. See [fmt.Errorf docs](https://pkg.go.dev/fmt#Errorf).

### Context-Only Errors (No Wrapping)

When the original error isn't useful to callers, create a new descriptive error:

```go
return fmt.Errorf("tmux is required but not found")
return fmt.Errorf("DENDRA_ROOT environment variable is not set")
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

For cleanup that shouldn't fail the operation:

```go
_ = deps.tmuxRunner.KillWindow(agentState.TmuxSession, agentState.TmuxWindow)
```

### Error Handling Decision Tree

1. **Can the caller recover?** → Wrap with `%w` so they can inspect
2. **Is it a validation error?** → Include the bad value and valid options
3. **Is the original error useless?** → Create a new descriptive error (no `%w`)
4. **Is it cleanup?** → Discard with `_ =`
5. **Is retrying safe?** → Warn and return nil

---

## Go Module & Dependencies

- **Module path:** `github.com/dmotles/dendrarchy`
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
