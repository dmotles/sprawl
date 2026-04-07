# Config Load Bug: `sprawl merge` and `sprawl retire --merge`

## Summary

The "WARNING: no validate command configured" message appears because `cmd/retire.go` never loads the project config when building the merge configuration. The `sprawl merge` code path is actually correct.

## Root Cause

### `retire --merge` (confirmed bug)

In `cmd/retire.go` lines 157-165, when `--merge` is requested, the code builds a `merge.Config` struct directly **without ever calling `config.Load()`**:

```go
// cmd/retire.go:157-165
cfg := &merge.Config{
    SprawlRoot:     sprawlRoot,
    AgentName:      agentName,
    AgentBranch:    agentState.Branch,
    AgentWorktree:  agentState.Worktree,
    ParentBranch:   targetBranch,
    ParentWorktree: callerWorktree,
    AgentState:     agentState,
    // NOTE: ValidateCmd is never set! Defaults to ""
    // NOTE: NoValidate is never set! Defaults to false
}
```

Because `ValidateCmd` is empty and `NoValidate` is false, the merge logic in `internal/merge/merge.go` line 117 hits the warning branch:

```go
} else if !cfg.NoValidate && cfg.ValidateCmd == "" {
    fmt.Fprintf(deps.Stderr, "WARNING: no validate command configured...")
}
```

Compare with `cmd/merge.go` lines 194-207, which **correctly** loads config first:

```go
// cmd/merge.go:112 — loads config
sprawlCfg, err := deps.loadConfig(sprawlRoot)

// cmd/merge.go:194-207 — passes ValidateCmd
cfg := &merge.Config{
    ...
    ValidateCmd:     sprawlCfg.Validate,  // ← correctly set
    NoValidate:      noValidate,          // ← from --no-validate flag
    ...
}
```

### `sprawl merge` (code is correct)

The `sprawl merge` command at `cmd/merge.go` **does** load config correctly:

1. Line 112: `sprawlCfg, err := deps.loadConfig(sprawlRoot)`
2. Line 202: `ValidateCmd: sprawlCfg.Validate`

`config.Load()` resolves the path as `filepath.Join(sprawlRoot, ".sprawl", "config.yaml")`, which correctly finds the config file when `SPRAWL_ROOT` is set to the repo root.

The bug report states both commands are affected, but code analysis shows only `retire --merge` has the defect. It's possible the user primarily encountered the bug via `retire --merge` and attributed it to both commands. Alternatively, if `sprawl merge` was always tested after a `retire --merge` in the same session, the warning output may have been conflated.

## Additional Issues Found

### Missing `retireDeps.loadConfig`

The `retireDeps` struct in `cmd/retire.go` has no `loadConfig` field at all. The retire command doesn't have any mechanism to load project config. This is a structural gap — compare with `mergeDeps` which has `loadConfig func(sprawlRoot string) (*config.Config, error)`.

### No test coverage for ValidateCmd in retire --merge

The test `TestRetire_MergeFlag_MergesBeforeRetiring` (line 844) only verifies that `doMerge` was called — it never inspects the `merge.Config` to check `ValidateCmd`. The merge command has dedicated tests (`TestMerge_ConfigValidateCmd_PassedThrough`) that verify this, but retire's merge path has no equivalent.

## Recommended Fix

### 1. Add `loadConfig` to `retireDeps`

```go
type retireDeps struct {
    // ... existing fields ...
    loadConfig func(sprawlRoot string) (*config.Config, error)  // add this
}
```

Wire it up in `resolveRetireDeps()`:

```go
loadConfig: config.Load,
```

### 2. Load config and set ValidateCmd in retire --merge path

In `runRetire()`, before building the merge config (around line 155):

```go
if mergeFirst {
    // ... existing precondition checks ...

    // Load project config for validate command
    sprawlCfg, err := deps.loadConfig(sprawlRoot)
    if err != nil {
        return fmt.Errorf("loading config: %w", err)
    }

    cfg := &merge.Config{
        SprawlRoot:     sprawlRoot,
        AgentName:      agentName,
        AgentBranch:    agentState.Branch,
        AgentWorktree:  agentState.Worktree,
        ParentBranch:   targetBranch,
        ParentWorktree: callerWorktree,
        ValidateCmd:    sprawlCfg.Validate,  // ← add this
        AgentState:     agentState,
    }
    // ...
}
```

### 3. Add test coverage

Add a test that captures the `merge.Config` passed to `doMerge` from `retire --merge` and verifies `ValidateCmd` is populated when config exists.

## Open Questions

- **Is `--no-validate` needed on `retire --merge`?** Currently `retire --merge` doesn't expose a `--no-validate` flag. Should it? The merge command has one. Users who want to skip validation during retire --merge currently have no way to do so (other than not having a validate config).
- **Should `sprawl merge` be re-verified in production?** Code analysis says it's correct, but the bug report claims it's affected. A quick manual test would confirm.

## Files Involved

| File | Role |
|------|------|
| `cmd/retire.go:117-175` | Bug location — merge config built without loading project config |
| `cmd/merge.go:102-227` | Correct implementation for comparison |
| `internal/config/config.go:22-57` | `Load()` function — works correctly |
| `internal/merge/merge.go:107-119` | Warning logic that triggers the symptom |
| `cmd/retire_test.go:830-884` | Insufficient test — doesn't verify ValidateCmd |
