# BEADS (bd) Worktree Integration Research

**Date:** 2026-04-07
**Researcher:** ghost
**bd version tested:** 0.63.3

## Problem Statement

Sprawl creates git worktrees for each agent at `.sprawl/worktrees/<agentName>/`. The `bd` CLI discovers its `.beads/` directory based on CWD. When agents run in worktrees, bd may not correctly locate the beads database because `.beads/` lives in the main repo root.

## Findings

### How bd Discovers .beads/

1. **CWD-based discovery**: bd looks for a `.beads/` directory starting from the current working directory. In a git worktree, `.beads/` is checked out from git (it contains tracked files like `config.yaml`, `README.md`, `hooks/`, `metadata.json`), but the actual database (`embeddeddolt/`) only exists in the main repo's `.beads/`.

2. **Embedded Dolt resolution**: Even without a redirect file, bd resolves the embedded dolt database path through the git commondir mechanism. Testing confirmed that `bd list`, `bd q`, and `bd where` all work from a plain git worktree (created with `git worktree add`, not `bd worktree create`). The `database_path` correctly resolves to the main repo's `.beads/embeddeddolt`.

3. **The redirect file**: `.beads/redirect` is a plain text file containing a relative path to the main `.beads/` directory. When present, bd transparently follows it. `bd where --json` shows both `redirected_from` and the resolved `path`.

**Key insight**: bd already works from worktrees without a redirect file for database operations. However, the redirect is recommended because it makes the resolution explicit and may be required for operations that work with the `.beads/` directory path itself (not just the database).

### bd worktree create

`bd worktree create <path> [--branch=<branch>]` does three things:

1. Creates a git worktree via `git worktree add`
2. Creates a `.beads/redirect` file in the worktree pointing back to the main `.beads/`
3. Adds the worktree path to `.gitignore` (if inside the repo root)

**Accepts arbitrary paths**: The `<path>` argument can be any relative or absolute path, including Sprawl-style paths like `.sprawl/worktrees/agent-name`. Tested successfully:

```
bd worktree create .sprawl/worktrees/agent-bd --branch test-bd-agent
```

This created the worktree at the expected path with a correct redirect (`../../../.beads`).

### Redirect Mechanism Details

- **Format**: A single line containing a relative path to the target `.beads/` directory
- **Example from `.sprawl/worktrees/agent/`**: `../../../.beads`
- **Transparent to all bd commands**: Once the redirect is in place, all bd commands work identically to running from the main repo
- **Manual creation works**: Writing the redirect file manually (without `bd worktree create`) works fine. bd resolves it the same way.

### Second-Generation Worktrees (Nesting)

**Tested**: Creating a worktree from within an existing worktree (simulating a manager agent spawning a sub-agent).

- `bd worktree create` from a worktree with a redirect **always points back to the original main `.beads/`** — it does not chain redirects through the parent worktree
- The sub-agent worktree gets its own redirect with the correct relative path (e.g., `../../../../../../.beads`)
- `bd where --json` confirms correct resolution from the nested worktree

This means the redirect mechanism handles arbitrary nesting depth correctly.

### BEADS_DIR Environment Variable

bd supports a `BEADS_DIR` environment variable that overrides all discovery logic:

```bash
BEADS_DIR=/path/to/main/.beads bd list  # works from anywhere
```

This is a viable fallback but less elegant than the redirect approach.

## Evaluation of Approaches

### Option A: Config-Driven Worktree Hooks

Add a config option to `.sprawl/config.yaml`:

```yaml
worktree:
  post-create: "bd worktree create {path} --branch {branch}"
  pre-remove: "bd worktree remove {path}"
```

**Pros:**
- Generic — works for any tool with worktree integration needs (not just beads)
- No beads-specific code in sprawl
- Users opt in explicitly
- `bd worktree create` handles all the redirect setup automatically

**Cons:**
- Requires replacing `git worktree add` with `bd worktree create` (or running both), since `bd worktree create` already calls `git worktree add` internally
- Hook template variables need careful design (`{path}`, `{branch}`, `{baseBranch}`)
- Doesn't handle the case where sprawl's worktree creation already happened — hooks would need to run *instead of* or *after* the built-in creation
- The `bd worktree create` approach creates the git worktree too, so we'd either need to let it handle the full creation or split into pre/post hooks

**Design consideration**: The cleanest approach would be a `post-create` hook that runs *after* sprawl creates the worktree. But `bd worktree create` both creates the worktree AND sets up the redirect. To use it as a hook, sprawl would either need to:
1. Skip its own `git worktree add` and let the hook handle everything (risky — hook failure means no worktree)
2. Run `git worktree add` first, then the hook just handles beads setup (but `bd worktree create` would fail since the worktree already exists)

**Resolution**: A `post-create` hook where the user manually creates the redirect would work. Or better: a hook that receives the worktree path and main repo root, letting the user script whatever they need.

### Option B: Automatic Redirect Management

When sprawl creates a worktree, automatically create `.beads/redirect` if `.beads/` exists in the main repo.

**Pros:**
- Zero configuration — works out of the box for all beads users
- Simple implementation: detect `.beads/` in repo root, create redirect file in worktree
- Handles nesting correctly (always compute relative path back to the original main repo's `.beads/`)
- No prompt changes needed — agents use bd normally
- Tested: manually creating the redirect file works identically to `bd worktree create`

**Cons:**
- Sprawl needs to know about the `.beads/redirect` convention specifically
- If bd changes its redirect format in the future, sprawl would need updating
- Tight coupling between sprawl and beads internals

**Implementation sketch:**
```go
// In worktree.Create, after git worktree add:
mainBeads := filepath.Join(repoRoot, ".beads")
if _, err := os.Stat(mainBeads); err == nil {
    wtBeads := filepath.Join(worktreePath, ".beads")
    relPath, _ := filepath.Rel(wtBeads, mainBeads)
    os.WriteFile(filepath.Join(wtBeads, "redirect"), []byte(relPath+"\n"), 0644)
}
```

### Option C: Prompt-Based Guidance

Tell agents in their system prompt to run bd commands from `SPRAWL_ROOT` instead of their worktree CWD.

**Pros:**
- Simplest to implement — no code changes
- Works today

**Cons:**
- Breaks worktree isolation guarantees
- Agents would need to `cd` to main repo for bd commands, then back to worktree for code changes
- Error-prone — agents may forget or make mistakes
- Doesn't scale to second-generation worktrees (manager agents don't have access to SPRAWL_ROOT easily)
- **Actually unnecessary**: testing showed bd already resolves the database correctly from worktrees through git commondir. The redirect just makes it explicit.

**Verdict: Not recommended.** The problem this solves is actually less severe than expected since bd already partially works from worktrees.

### Option D: BEADS_DIR Environment Variable (Discovered During Research)

Set `BEADS_DIR` environment variable when launching agents:

```yaml
# In agent environment
BEADS_DIR: "{repoRoot}/.beads"
```

**Pros:**
- No file creation needed
- Works regardless of worktree path structure
- Official bd mechanism

**Cons:**
- Requires sprawl to know about BEADS_DIR specifically (same coupling as Option B)
- Less discoverable — agents can't inspect `.beads/redirect` to understand the setup
- Doesn't benefit from `bd worktree info` showing redirect information
- Must be set per-agent; easy to miss when spawning sub-agents

## Recommendation

**Primary: Option B (Automatic Redirect) with Option A (Config Hooks) as the extensibility mechanism.**

### Rationale

1. **Option B is the right default behavior.** If a repo has `.beads/`, sprawl should automatically create the redirect. This is simple, reliable, tested to work with nesting, and follows the beads convention exactly. The implementation is ~10 lines of Go.

2. **Option A provides extensibility for future tools.** Beads isn't the only tool that might need worktree setup. A generic `post-create` / `pre-remove` hook config gives users flexibility. However, this is more work and should be a separate task — don't let it block the beads fix.

3. **Option C should be avoided.** It breaks isolation and is unnecessary given the findings.

4. **Option D (BEADS_DIR) is a reasonable fallback** if the redirect approach causes issues, but it's less idiomatic for beads.

### Suggested Implementation Order

1. **Immediate fix**: Add automatic `.beads/redirect` creation to `internal/worktree/worktree.go` (Option B). This is the minimal change to unblock beads users.
2. **Follow-up**: Design and implement the config-driven hook system (Option A) as a general-purpose mechanism. At that point, users who prefer `bd worktree create` can configure it as a hook.
3. **Cleanup hook**: Also add automatic redirect cleanup when sprawl removes a worktree (if applicable).

## Open Questions

1. **Should sprawl also handle worktree removal via `bd worktree remove`?** It has safety checks (uncommitted changes, unpushed commits) that plain `git worktree remove` doesn't.
2. **Does the redirect file need to be `.gitignore`'d?** Since `.beads/` has its own `.gitignore`, and the redirect file is worktree-specific, it probably shouldn't be committed. The `bd worktree create` approach handles this, but manual redirect creation might not.
3. **What about concurrent database access?** Multiple agents writing to the same beads database simultaneously could cause conflicts. This is a beads-level concern, not a sprawl concern, but worth noting. The `--readonly` and `--sandbox` flags on bd suggest beads has thought about this.
4. **Config hook design**: If implementing Option A, should hooks replace or augment sprawl's worktree creation? The `bd worktree create` case suggests "replace" might be needed, which requires careful error handling.

## Reflections

**Surprising findings:**
- bd already partially works from git worktrees without any redirect, because it resolves the embedded dolt database through git's commondir. The redirect is "belt and suspenders" — it makes the resolution explicit and authoritative rather than relying on git internals.
- `bd worktree create` accepts arbitrary paths, making it fully compatible with Sprawl's `.sprawl/worktrees/<name>` layout without modification.
- The redirect always points to the **original** main .beads, even from nested worktrees — it doesn't chain through intermediate worktrees.

**What I'd investigate with more time:**
- Concurrent write safety when multiple agents access the same beads database
- Whether `bd worktree create` could be modified to work as a pure post-hook (set up redirect without creating the worktree)
- The interaction between beads' dolt auto-commit and sprawl's merge workflow
- Whether there's a `bd init --worktree` or similar that just sets up the redirect in an existing worktree
