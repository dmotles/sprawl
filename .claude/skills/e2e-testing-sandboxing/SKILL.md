---
name: e2e-testing-sandboxing
description: Set up and use the sandbox testing system for end-to-end validation of sprawl changes without affecting production state.
user-invocable: true
argument-hint: "[setup|inspect|cleanup] or omit for full workflow"
---

# Sandbox Testing Workflow

Use this workflow to validate sprawl changes end-to-end in an isolated environment. No real Claude API keys are needed.

## **DO NOT**

- **Do NOT run `rm -rf "$SPRAWL_ROOT"` (or any destructive command against `$SPRAWL_ROOT`) manually.** The setup script installs an EXIT trap and a `sprawl_sandbox_destroy` function — use those. If `$SPRAWL_ROOT` is stale or points somewhere unexpected, a manual `rm -rf` can nuke your real repo.
- **Do NOT run `bash scripts/sprawl-test-env.sh` from inside a `.sprawl/worktrees/` path.** The script refuses, by design. `cd /tmp` first, then invoke it by absolute path.
- **Do NOT nest this workflow with `/tui-testing` in the same shell session.** Their env vars collide and the cleanup traps can stomp each other. Use separate shells.

## Setup

```bash
cd /tmp                    # never run this from a worktree
make -C /path/to/sprawl build
eval "$(bash /path/to/sprawl/scripts/sprawl-test-env.sh)"
```

This exports the following environment variables into your shell:

| Variable | Purpose |
|---|---|
| `SPRAWL_BIN` | Path to the built binary. **Always use `$SPRAWL_BIN` instead of bare `sprawl`.** |
| `SPRAWL_ROOT` | Temporary test directory (acts as the project root). Always under `/tmp/`. |
| `SPRAWL_TEST_MODE=1` | Injects sandbox warnings into agent prompts. |
| `SPRAWL_NAMESPACE` | Isolated tmux namespace (format: `test-XXXXXXXX`). |

It also installs:

- `sprawl_sandbox_destroy` — the sanctioned manual teardown. Kills the tmux session and removes `$SPRAWL_ROOT`, but only after reasserting the path is under `/tmp/`.
- An `EXIT` trap on your shell that auto-cleans `$SPRAWL_ROOT` (same `/tmp/` guard) when the shell terminates. In the common case you don't need to clean up manually — just `exit`.

## Exercising Features

Run all commands using `$SPRAWL_BIN` and work within `$SPRAWL_ROOT`:

```bash
cd "$SPRAWL_ROOT"

# Example: spawn an agent in the sandbox
$SPRAWL_BIN spawn --family engineering --type engineer \
  --prompt "Hello from sandbox"

# Example: list agents
$SPRAWL_BIN status

# Example: send a message
$SPRAWL_BIN messages send weave "Test message" "Hello"
```

## Inspecting State

```bash
# tmux sessions for this sandbox
tmux list-sessions | grep "$SPRAWL_NAMESPACE"

# Agent state, messages, memory
ls "$SPRAWL_ROOT/.sprawl/"
ls "$SPRAWL_ROOT/.sprawl/agents/"

# Read specific state files
cat "$SPRAWL_ROOT/.sprawl/agents/<agent-name>.json"

# Read message files
ls "$SPRAWL_ROOT/.sprawl/messages/"
```

## Cleanup

Preferred: just exit the shell — the EXIT trap handles it.

Manual teardown from the same shell:

```bash
sprawl_sandbox_destroy
```

Do **not** hand-roll `rm -rf "$SPRAWL_ROOT"`. See the DO-NOT section above.

## Gotchas

Hard-won lessons from prior e2e testing sessions. Read these before writing sandbox tests.

### 1. Sandbox identity convention

Sandbox test processes simulating child agents must use an identity that clearly screams "sandbox" — e.g. `sandbox-child`, `sandbox-pretend`, `test-harness-child`. **Do NOT use generic names like** `pretend-child`.

Reason: the legacy tmux notifier's namespace resolution falls open to hardcoded defaults (`⚡:weave`) that collide with the outer developer tmux session (QUM-315). Any send-keys text bleeding out carries the identity, so making it self-labeled as sandbox work lets the human ignore it cleanly.

### 2. TUI pane-size pin (200×50)

Detached tmux sessions default to ~80-col width, which truncates the TUI badge/tree rendering (e.g. the `(N)` weave unread badge gets cut). When launching `sprawl enter` in a detached tmux session for e2e tests, pin window size:

```bash
tmux new-session -d -s <name> -x 200 -y 50 <cmd>
tmux resize-window -t <name>:0 -x 200 -y 50   # required even after -x/-y on some tmux versions
```

Discovered in QUM-312.

### 3. Trust-prompt advancement

On a fresh sandbox, `claude` prompts for trust on first invocation ("Trust this directory? Y/n"). Any e2e script that launches `sprawl init`/`sprawl enter` must advance past this prompt before `tmux send-keys` assertions will render meaningful output. Detect the trust prompt (e.g. grep `capture-pane` for "Trust") and send Enter before the main test scenario.

Discovered in QUM-310.

### 4. Respawn-window verification trick

To verify env-var propagation onto a tmux session without launching the full child agent, use `tmux respawn-window` to start a shell in the session and run `env | grep SPRAWL_` directly:

```bash
tmux respawn-window -t <session>:0 -k 'bash -c "env | grep SPRAWL_; sleep 5"'
tmux capture-pane -t <session>:0 -p
```

Useful for QUM-309-class env-propagation tests.

### 5. Manager merge target

Managers spawned by weave share weave's supervisor identity (QUM-384). This means `sprawl merge` from a manager may commit to main instead of the manager's integration branch. When using managers, verify that merged work lands on the manager's branch — not main. If in doubt, use `git -C <worktree> log --oneline -5` to check where commits landed before reporting done.

## Scripted Smoke Tests

For an example of automated sandbox assertions, see `scripts/smoke-test-memory.sh`. It sets up a sandbox, exercises the memory system, and asserts expected outcomes. Run it with:

```bash
bash scripts/smoke-test-memory.sh
```

## Tips

- If a command hangs or behaves unexpectedly, check that you're using `$SPRAWL_BIN` (not a globally installed `sprawl`).
- The sandbox is completely isolated — it won't affect your real `.sprawl/` directory or tmux sessions.
- You can run multiple sandboxes concurrently; each gets a unique namespace.

## Why this matters

On 2026-04-21 an agent ran `rm -rf "$SPRAWL_ROOT"` from inside its own worktree (`/home/coder/sprawl/.sprawl/worktrees/finn`) while `$SPRAWL_ROOT` still pointed into the real repo tree. The worktree — and then the real repo — were destroyed, and the root repo had to be re-cloned. The hardened script (cwd guard + `/tmp/` assertion + guarded cleanup trap + `sprawl_sandbox_destroy`) exists so this cannot happen again. Follow the DO-NOT list.
