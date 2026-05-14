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
go test ./internal/agentops/...
go test ./internal/supervisor/...
go test ./internal/agent/...
go test ./internal/worktree/...
```

Run a specific test by name:

```bash
go test ./cmd/... -run TestRetire_HappyPathDeletesState
go test ./cmd/... -run TestMessagesSend_HappyPath
```

Use `-v` for verbose output:

```bash
go test -v ./...
```

## Dependency Injection Testing Pattern

This codebase uses a **struct-based dependency injection** pattern for testing CLI commands. Each command defines a `*Deps` struct that holds all external dependencies as fields — typically as **function values** (closures) for filesystem, environment, and git operations, with the occasional interface for richer collaborators (`backend.Adapter`, `worktree.Creator`, `merge.Deps`). The production code path wires in real implementations, while tests inject closures that record calls or return canned values.

The richest end-to-end example today is the offline `retire` command — `internal/agentops/retire.go` defines `RetireDeps`, `cmd/retire.go` wires the production deps, and `cmd/retire_test.go` builds them with closures. Use it as the reference.

### How it works

1. **Define a deps struct** for the command. The current convention is to put the struct (and the business logic) in `internal/agentops/` and re-export a type alias from `cmd/`. From `internal/agentops/retire.go`:

   ```go
   type RetireDeps struct {
       Getenv              func(string) string
       WorktreeRemove      func(repoRoot, worktreePath string, force bool) error
       GitStatus           func(worktreePath string) (string, error)
       RemoveAll           func(string) error
       GitBranchDelete     func(repoRoot, branchName string) error
       GitBranchIsMerged   func(repoRoot, branchName string) (bool, error)
       GitBranchSafeDelete func(repoRoot, branchName string) error
       DoMerge             func(ctx context.Context, cfg *merge.Config, deps *merge.Deps) (*merge.Result, error)
       NewMergeDeps        func() *merge.Deps
       LoadAgent           func(sprawlRoot, name string) (*state.AgentState, error)
       CurrentBranch       func(repoRoot string) (string, error)
       // ...
   }
   ```

   And in `cmd/retire.go`:

   ```go
   type retireDeps = agentops.RetireDeps
   ```

2. **The package-level run function** (`agentops.Retire`) accepts the deps struct instead of calling globals directly:

   ```go
   func Retire(deps *RetireDeps, agentName string, cascade, force, abandon, mergeFirst, yes, noValidate bool) error {
       // uses deps.Getenv, deps.WorktreeRemove, deps.LoadAgent, etc.
   }
   ```

3. **Tests build the deps with closures** in a per-test helper (e.g. `newTestRetireDeps` in `cmd/retire_test.go`):

   ```go
   func newTestRetireDeps(t *testing.T) (*retireDeps, string) {
       t.Helper()
       tmpDir := t.TempDir()
       deps := &retireDeps{
           Getenv: func(key string) string {
               if key == "SPRAWL_ROOT" {
                   return tmpDir
               }
               return ""
           },
           WorktreeRemove: func(repoRoot, worktreePath string, force bool) error {
               return os.RemoveAll(worktreePath)
           },
           GitStatus:           func(worktreePath string) (string, error) { return "", nil },
           RemoveAll:           os.RemoveAll,
           GitBranchDelete:     func(repoRoot, branchName string) error { return nil },
           GitBranchIsMerged:   func(repoRoot, branchName string) (bool, error) { return false, nil },
           GitBranchSafeDelete: func(repoRoot, branchName string) error { return nil },
           DoMerge:             func(_ context.Context, cfg *merge.Config, deps *merge.Deps) (*merge.Result, error) { return &merge.Result{}, nil },
           NewMergeDeps:        func() *merge.Deps { return &merge.Deps{} },
           LoadAgent:           state.LoadAgent,
           CurrentBranch:       func(repoRoot string) (string, error) { return "main", nil },
           // ...
       }
       return deps, tmpDir
   }
   ```

   Note that `state.LoadAgent` is wired through as a real function — tests use the real `state` package against `t.TempDir()` rather than mocking it.

4. **Individual tests override fields when they need to assert specific behavior** rather than maintaining mock structs:

   ```go
   func TestRetire_DirtyWorktree_Refuses(t *testing.T) {
       deps, tmpDir := newTestRetireDeps(t)
       deps.GitStatus = func(string) (string, error) { return "M file.go", nil }
       // ...
   }
   ```

### Function values vs interfaces

This codebase **strongly prefers function values** over single-method interfaces. Use a `func(...) (...)` field whenever the dependency is one operation (`os.Getenv`, `git status`, `state.LoadAgent`, a merge invocation). Reach for an interface only when:

- The collaborator has multiple related methods that callers compose together (e.g. `worktree.Creator`, `backend.Adapter`, `supervisor.Supervisor`).
- You need to fake a stateful object across several calls.

Counter-example to follow: `cmd/messages.go::messagesDeps` only needs `getenv` plus injectable `stdout`/`stderr` (`io.Writer`) — no interfaces at all. See `cmd/messages_test.go::newTestMessagesDeps`.

### Resolve / run separation

Each command file in `cmd/` has the same shape:

- `resolve<Command>Deps()` constructs the production deps (real `os.Getenv`, real git wrappers from `agentops`, real `state.LoadAgent`).
- `run<Command>(deps, ...)` is pure business logic and is the unit under test.
- The cobra `RunE` is a one-liner that calls `resolve...` and then `run...`.

`defaultRetireDeps` / `defaultMessagesDeps` package-level pointers exist so integration-style tests can swap in a pre-built deps struct without going through `resolve`.

### Test file conventions

- Each command file `cmd/foo.go` has a corresponding `cmd/foo_test.go`.
- Helper constructors follow the pattern `newTest<Command>Deps(t *testing.T)`.
- Tests use `t.TempDir()` for isolated filesystem state.
- The `state` and `messages` packages are used directly (not mocked) — tests create real state files and Maildir entries in temp dirs.
- Mock structs only appear when faking interfaces (`worktree.Creator`, `merge.Deps`); see `cmd/mocks_test.go` for the shared ones.

## Manual CLI Validation

Build the binary:

```bash
make build
```

This produces a `./sprawl` binary. The interactive entrypoint is `sprawl enter` — there is no `sprawl init` (it was removed in QUM-346; see `cmd/init_removed_test.go` for the regression guard). The CLI surface is intentionally small: the agent-facing operations (spawn, delegate, retire, kill, send_message, report_status, status, peek, merge, handoff, messages_*) are all MCP tools driven from inside a `sprawl enter` weave session. The standalone CLI exposes only:

```bash
# Open the TUI / weave session (loads the same-process supervisor)
./sprawl enter

# Tail an agent's session log
./sprawl logs alice

# Squash-merge an agent's branch (also available as the `merge` MCP tool)
./sprawl merge alice

# Branch hygiene — delete merged branches not owned by any active agent
./sprawl cleanup branches

# Config + memory utilities
./sprawl config show
./sprawl memory show
```

For anything else — inspecting agent state, sending messages, reporting status, spawning, killing, retiring — drive it from inside `sprawl enter` via the MCP tools.

## Validating Agent Behavior

When testing the full system (not unit tests), inspect these artifacts:

### Agent state files

```bash
# State files live in .sprawl/agents/
ls .sprawl/agents/
cat .sprawl/agents/alice.json

# Each JSON file contains: name, type, family, parent, prompt, branch,
# worktree path, status, session id, cost fields, and last_report_*.
# The full schema is internal/state/state.go::AgentState.
```

### Messages

```bash
# Maildir layout under .sprawl/messages/<agent>/{new,cur,archive}/
ls .sprawl/messages/
ls .sprawl/messages/weave/new/

# Inbox via MCP (from inside a weave session)
# messages_peek({})            — unread count + previews
# messages_list({filter: "unread"})
```

### Git worktrees

```bash
# Worktrees live under .sprawl/worktrees/<agent-name>/
ls .sprawl/worktrees/
git worktree list

# Check for uncommitted changes in an agent's worktree
git -C .sprawl/worktrees/alice status
```

### End-to-end harnesses

The `make validate` pipeline does NOT cover the live supervisor / TUI integration. Use these dedicated harnesses (each spins up an isolated `/tmp` sandbox via `scripts/sprawl-test-env.sh`):

```bash
make test-handoff-e2e          # supervisor + MCP handoff round-trip (QUM-329)
make test-notify-tui-e2e       # TUI inbox-notifier delivery (QUM-311/312)
make test-tui-e2e              # general TUI rendering smoke
```

Each target requires a real `claude` binary on `PATH`; set `SPRAWL_E2E_SKIP_NO_CLAUDE=1` to skip in environments without one. They are **mandatory** before merging changes that touch the file lists called out in `CLAUDE.md` ("TUI-notifier changes are mandatory-tested" / "Handoff-path changes are mandatory-tested").

For ad-hoc exploration, use the `/e2e-testing-sandboxing` skill to set up a sandbox manually.

## Testing Pyramid

### Unit tests (fast, isolated, closures)

The bulk of testing happens here. External dependencies (filesystem mutations, git commands, environment, signals, time) are injected as function-value fields on the deps struct. These tests verify:

- Happy-path logic for each command
- Error handling (missing env vars, exhausted name pool, git failures, worktree failures)
- State transitions (active → killed, active → retiring → deleted)
- Edge cases (already-killed agents, agents with children, dirty worktrees, deprecated CLI paths)

Run with: `go test ./...`.

### Integration-style tests (use real `state` / `messages` / `merge` packages)

Command tests use the real `state` and `messages` packages to read/write JSON files and Maildir entries in `t.TempDir()`. This validates serialization and file operations end-to-end without mocking the filesystem.

`internal/supervisor/*_test.go` exercises the same-process runtime registry against fake backends (see `internal/backend` and `internal/runtime` test helpers) — that's where the bulk of supervisor logic is covered without spinning up real Claude processes.

### Manual / scripted e2e (real claude, real git, sandbox /tmp)

Full-system behavior — TUI rendering, MCP tool routing, claude-process lifecycle, inter-agent message delivery, handoff/restart — is validated by the `make test-*-e2e` targets and ad-hoc sandbox sessions. These cannot be meaningfully unit-tested.

## Common Pitfalls

### Don't shell out to real `git` / `tmux` / `claude` in unit tests

The closure-injection pattern exists specifically so tests don't depend on `git`, `tmux`, or `claude` being installed. If a unit test calls real binaries it will be slow, flaky, and CI-hostile. Always inject closures via the deps struct.

### Function values vs interfaces — pick the smaller hammer

Use function fields when the dependency is one operation (`getenv`, `signalFunc`, `gitStatus`). Use interfaces when the collaborator is stateful or has multiple methods (`worktree.Creator`, `backend.Adapter`). Don't define a single-method interface just to "be testable" — a `func(...)` field is simpler.

### Use `t.Helper()` in test setup functions

All `newTest*Deps` helpers call `t.Helper()` so failure messages point to the actual test function, not the helper.

### Use `t.TempDir()` for state isolation

Never write state files to a shared directory. Each test gets its own temp dir via `t.TempDir()`, which is automatically cleaned up.

### The `state` and `messages` packages are intentionally NOT mocked

Tests use `state.SaveAgent`/`state.LoadAgent` and `messages.*` directly against temp directories. This gives confidence that JSON serialization and Maildir handling work without adding indirection.

### Override fields per-test rather than building parallel mock structs

Idiomatic style: call `newTest<Command>Deps(t)`, then mutate the field you care about (e.g. `deps.GitStatus = func(string) (string, error) { return "M foo", nil }`). See `TestRetire_DirtyWorktree_Refuses` in `cmd/retire_test.go`.

### `cmd/init_removed_test.go` guards a deletion

If you find yourself wanting to add a `sprawl init` or `_root-session` command back, read QUM-346 first — that test will fail and is intentional. The interactive entrypoint is `sprawl enter`.
