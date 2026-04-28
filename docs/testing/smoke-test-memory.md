# Weave Memory System Smoke Test

Integration smoke test for the sprawl weave memory system. Exercises session persistence, handoff, context blob file contracts, timeline, and budget enforcement using the locally-built binary.

Does NOT require a real Claude API key.

## Prerequisites

- `tmux` installed and on `$PATH`
- Go toolchain (for `make build`)
- `xxd` available (typically from `vim-common`)

## Running the Smoke Test

```bash
# From the repo root (or any worktree)
bash scripts/smoke-test-memory.sh
```

The script will:

1. Build the `sprawl` binary
2. Create a temp directory with an isolated git repo
3. Generate a `test-*` namespaced environment
4. Seed the minimal `.sprawl/` state files (namespace, root-name) directly — QUM-346 removed the legacy `sprawl init` parent entrypoint
5. Execute test cases against the memory system
6. Clean up all resources on exit (via trap)

Exit code 0 means all tests passed. Non-zero means at least one test failed.

## What It Tests

| Test | Description |
|------|-------------|
| Init state files | Verifies `.sprawl/namespace` and `.sprawl/root-name` are created |
| Session persistence | Writes session files in the expected YAML frontmatter format, reads back |
| Handoff command | Runs `sprawl handoff` via stdin, verifies session file and signal file created |
| Handoff error cases | Verifies handoff fails without required env vars or with wrong identity |
| Multiple sessions | Creates 5 session files, verifies ordering and file format contract |
| Handoff with existing sessions | Verifies new handoff appends alongside existing session files |
| Timeline round-trip | Writes and reads back timeline entries in expected format |
| Budget enforcement | Verifies handoff handles large (50KB) content without error |
| Safety check | Verifies no script contains `tmux kill-server` |
| Cleanup flags | Tests `--cleanup` and `--cleanup-all` flag functionality |

## Setting Up a Standalone Test Environment

Use the wrapper script to create an isolated environment for manual testing:

```bash
# Print export statements
bash scripts/sprawl-test-env.sh

# Or source directly into your shell
eval "$(bash scripts/sprawl-test-env.sh)"

# The following env vars are now set:
#   SPRAWL_BIN       - path to built binary
#   SPRAWL_ROOT      - temp directory with git repo
#   SPRAWL_TEST_MODE - set to 1
#   SPRAWL_NAMESPACE - test-* namespace
#   TEST_NS          - same as SPRAWL_NAMESPACE
#   TEST_ROOT        - same as SPRAWL_ROOT
```

The test environment creates a tmux session running the weave loop. You can attach to it or kill it as needed.

## Cleanup

**Automatic cleanup**: The smoke test uses an EXIT trap that always runs, even on failure. It kills only tmux sessions matching the test namespace and removes the temp directory.

**Manual cleanup for a specific namespace**:
```bash
bash scripts/smoke-test-memory.sh --cleanup <namespace>
```

**Kill all test sessions**:
```bash
bash scripts/smoke-test-memory.sh --cleanup-all
```

This finds and kills all tmux sessions with names starting with `test-`. It NEVER uses `tmux kill-server`.

## Running From Any Worktree

Both scripts derive the repo root from their own location (`$(dirname "$0")/..`), so they work correctly when invoked from any worktree:

```bash
# From a worktree
bash /path/to/repo/scripts/smoke-test-memory.sh
```
