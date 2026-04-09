---
name: e2e-testing-sandboxing
description: Set up and use the sandbox testing system for end-to-end validation of sprawl changes without affecting production state.
user-invocable: true
argument-hint: "[setup|inspect|cleanup] or omit for full workflow"
---

# Sandbox Testing Workflow

Use this workflow to validate sprawl changes end-to-end in an isolated environment. No real Claude API keys are needed.

## Setup

```bash
make build
eval "$(bash scripts/sprawl-test-env.sh)"
```

This exports the following environment variables into your shell:

| Variable | Purpose |
|---|---|
| `SPRAWL_BIN` | Path to the built binary. **Always use `$SPRAWL_BIN` instead of bare `sprawl`.** |
| `SPRAWL_ROOT` | Temporary test directory (acts as the project root). |
| `SPRAWL_TEST_MODE=1` | Injects sandbox warnings into agent prompts. |
| `SPRAWL_NAMESPACE` | Isolated tmux namespace (format: `test-XXXXXXXX`). |

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

When done, tear down the sandbox:

```bash
tmux kill-session -t "${SPRAWL_NAMESPACE}" && rm -rf "$SPRAWL_ROOT"
```

## Scripted Smoke Tests

For an example of automated sandbox assertions, see `scripts/smoke-test-memory.sh`. It sets up a sandbox, exercises the memory system, and asserts expected outcomes. Run it with:

```bash
bash scripts/smoke-test-memory.sh
```

## Tips

- If a command hangs or behaves unexpectedly, check that you're using `$SPRAWL_BIN` (not a globally installed `sprawl`).
- The sandbox is completely isolated — it won't affect your real `.sprawl/` directory or tmux sessions.
- You can run multiple sandboxes concurrently; each gets a unique namespace.
