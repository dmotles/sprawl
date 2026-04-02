---
name: testing
description: Set up and use the sandbox testing system for end-to-end validation of dendra changes without affecting production state.
user-invocable: true
argument-hint: "[setup|inspect|cleanup] or omit for full workflow"
---

# Sandbox Testing Workflow

Use this workflow to validate dendra changes end-to-end in an isolated environment. No real Claude API keys are needed.

## Setup

```bash
make build
eval "$(bash scripts/dendra-test-env.sh)"
```

This exports the following environment variables into your shell:

| Variable | Purpose |
|---|---|
| `DENDRA_BIN` | Path to the built binary. **Always use `$DENDRA_BIN` instead of bare `dendra`.** |
| `DENDRA_ROOT` | Temporary test directory (acts as the project root). |
| `DENDRA_TEST_MODE=1` | Injects sandbox warnings into agent prompts. |
| `DENDRA_NAMESPACE` | Isolated tmux namespace (format: `test-XXXXXXXX`). |

## Exercising Features

Run all commands using `$DENDRA_BIN` and work within `$DENDRA_ROOT`:

```bash
cd "$DENDRA_ROOT"

# Example: spawn an agent in the sandbox
$DENDRA_BIN spawn --family engineering --type engineer \
  --prompt "Hello from sandbox"

# Example: list agents
$DENDRA_BIN status

# Example: send a message
$DENDRA_BIN messages send sensei "Test message" "Hello"
```

## Inspecting State

```bash
# tmux sessions for this sandbox
tmux list-sessions | grep "$DENDRA_NAMESPACE"

# Agent state, messages, memory
ls "$DENDRA_ROOT/.dendra/"
ls "$DENDRA_ROOT/.dendra/agents/"

# Read specific state files
cat "$DENDRA_ROOT/.dendra/agents/<agent-name>.json"

# Read message files
ls "$DENDRA_ROOT/.dendra/messages/"
```

## Cleanup

When done, tear down the sandbox:

```bash
tmux kill-session -t "${DENDRA_NAMESPACE}sensei" && rm -rf "$DENDRA_ROOT"
```

## Scripted Smoke Tests

For an example of automated sandbox assertions, see `scripts/smoke-test-memory.sh`. It sets up a sandbox, exercises the memory system, and asserts expected outcomes. Run it with:

```bash
bash scripts/smoke-test-memory.sh
```

## Tips

- If a command hangs or behaves unexpectedly, check that you're using `$DENDRA_BIN` (not a globally installed `dendra`).
- The sandbox is completely isolated — it won't affect your real `.dendra/` directory or tmux sessions.
- You can run multiple sandboxes concurrently; each gets a unique namespace.
