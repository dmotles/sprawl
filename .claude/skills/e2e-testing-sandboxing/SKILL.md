---
name: e2e-testing-sandboxing
description: Set up and use the sandbox testing system for end-to-end validation of sprawl changes without affecting production state. Read it before running any tmux command in sandbox or harness context — sandbox tmux lives on a dedicated socket and bare `tmux` reaches the wrong server. Also carries the hard `/tmp` hygiene rules (shared `/tmp`, never a broad `rm -rf` glob, never touch `/tmp/coder-script-data`) and the `claude` auth shim — `.env` plus `scripts/run-claude` and `$SPRAWL_CLAUDE` — which is the fix when `claude` fails with `Not logged in` from a Bash subshell.
user-invocable: true
argument-hint: "[setup|inspect|cleanup] or omit for full workflow"
---

# Sandbox Testing Workflow

Use this workflow to validate sprawl changes end-to-end in an isolated environment. No real Claude API keys are needed.

## **DO NOT**

- **Do NOT run `rm -rf "$SPRAWL_ROOT"` (or any destructive command against `$SPRAWL_ROOT`) manually.** The setup script installs an EXIT trap and a `sprawl_sandbox_destroy` function — use those. If `$SPRAWL_ROOT` is stale or points somewhere unexpected, a manual `rm -rf` can nuke your real repo.
- **Do NOT run `bash scripts/sprawl-test-env.sh` from inside a `.sprawl/worktrees/` path.** The script refuses, by design. `cd /tmp` first, then invoke it by absolute path.
- **Do NOT nest this workflow with `/tui-testing` in the same shell session.** Their env vars collide and the cleanup traps can stomp each other. Use separate shells.
- **Do NOT run bare `tmux kill-server`.** It kills the *developer's* tmux server and every other agent's session on the default socket. The ban is on the **unscoped** form: socket-scoped `tmux -L "$SPRAWL_TMUX_SOCKET" kill-server` is what `sprawl_sandbox_destroy` itself runs and is correct, because `-L` confines it to this sandbox's own daemon.
- **Do NOT use bare `tmux` at all in a sandbox or in a harness script — use `_stmux`.** Sandbox tmux state lives on a dedicated socket (see Setup), so a bare `tmux` command talks to the wrong server: it will not find your session, and anything destructive it does lands on someone else's.

Production `sprawl enter` sessions still share the **default** socket. That asymmetry is the whole reason the sandbox socket exists, and it is why a bare `tmux` in sandbox context is not merely wrong but dangerous. The dedicated sandbox socket was introduced by **QUM-325** — that is the provenance for every tmux rule above.

### `/tmp` hygiene — hard rules

Sandbox roots live under `/tmp`, but `/tmp` is **shared** with other agents and
with host tooling. These rules are not advisory:

- **Never `rm -rf` a broad `/tmp` glob** (`/tmp/*`, `/tmp/sprawl-*`, `$TMPDIR/*`,
  …). It destroys other agents' in-flight sandboxes and host state.
- **Only remove a sandbox root you created**, and only after asserting the path
  is under `/tmp/` and matches the prefix you expect — assert, then delete. See
  `_e2e_cleanup` in `scripts/lib/e2e-common.sh` and `_unit_reset_markers` in
  `scripts/test-e2e-matrix-unit.sh` for the pattern (a `case` guard on the
  literal path, and `find -delete` rather than a `rm` glob).
- **Never touch `/tmp/coder-script-data`.** It is host tooling state. In this
  workspace `/tmp/coder-script-data/bin/claude` is a **symlink** into the
  developer's home dir, where the real binary lives on the persistent volume —
  so deleting it breaks `claude` PATH resolution rather than the installation,
  and recovery is a single `ln -s`. The hazard is not the blast radius, it is
  the silence: nothing in the harness would *tell* you, and every
  `needs_claude` e2e row would quietly start skipping.

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
| `SPRAWL_TMUX_SOCKET` | Dedicated tmux socket for this sandbox (format: `sprawl-sandbox-test-XXXXXXXX`), so sandbox tmux operations cannot reach the developer's default server — or any other agent's. |

It also installs:

- `_stmux` — the tmux wrapper you should use for **every** tmux call in sandbox context. It is `tmux ${SPRAWL_TMUX_SOCKET:+-L "$SPRAWL_TMUX_SOCKET"} "$@"` — note the fallback: with the variable unset it degrades to bare `tmux` **silently**. An unset socket is not safe, it is undetected, so check the variable is exported before trusting the wrapper. The e2e drivers get the same wrapper from `scripts/lib/e2e-common.sh`.
- `sprawl_sandbox_destroy` — the sanctioned manual teardown. Kills this sandbox's tmux server on its own socket and removes `$SPRAWL_ROOT`, but only after reasserting the path is under `/tmp/`.
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
# tmux sessions for this sandbox — _stmux, never bare tmux. Two reasons this
# is not `tmux list-sessions | grep "$SPRAWL_NAMESPACE"`: bare tmux queries the
# DEFAULT server, not $SPRAWL_TMUX_SOCKET; and the namespace names the socket,
# not the session, so the grep would filter out the sessions you want.
_stmux list-sessions

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

To clear only the tmux state and keep `$SPRAWL_ROOT` for inspection, stay
socket-scoped. `kill-server` is safe *here* because `-L` confines it to this
sandbox's own daemon — it is the same call `sprawl_sandbox_destroy` makes:

```bash
_stmux kill-server
```

To kill one session and leave the rest, look the name up first — **do not guess it
from `$SPRAWL_NAMESPACE`.** The namespace names the *socket*, not the session; each
script mints its own session name, so `-t "$SPRAWL_NAMESPACE"` errors with
`session not found`:

```bash
_stmux list-sessions                 # find the real name
_stmux kill-session -t <that-name>
```

Do **not** hand-roll `rm -rf "$SPRAWL_ROOT"`, and do **not** reach for
`tmux kill-server` to clean up. See the DO-NOT section above.

## Gotchas

Hard-won lessons from prior e2e testing sessions. Read these before writing sandbox tests.

### 1. Sandbox identity convention

Sandbox test processes simulating child agents must use an identity that clearly screams "sandbox" — e.g. `sandbox-child`, `sandbox-pretend`, `test-harness-child`. **Do NOT use generic names like** `pretend-child`.

Reason: the legacy tmux notifier's namespace resolution falls open to hardcoded defaults (`⚡:weave`) that collide with the outer developer tmux session (QUM-315). Any send-keys text bleeding out carries the identity, so making it self-labeled as sandbox work lets the human ignore it cleanly.

### 2. TUI pane-size pin (200×50)

Detached tmux sessions default to ~80-col width, which truncates the TUI badge/tree rendering (e.g. the `(N)` weave unread badge gets cut). When launching `sprawl enter` in a detached tmux session for e2e tests, pin window size:

```bash
_stmux new-session -d -s <name> -x 200 -y 50 <cmd>
_stmux resize-window -t <name>:0 -x 200 -y 50   # required even after -x/-y on some tmux versions
```

Discovered in QUM-312.

### 3. Trust-prompt advancement

On a fresh sandbox, `claude` prompts for trust on first invocation ("Trust this directory? Y/n"). Any e2e script that launches `sprawl enter` must advance past this prompt before `tmux send-keys` assertions will render meaningful output. Detect the trust prompt (e.g. grep `capture-pane` for "Trust") and send Enter before the main test scenario.

Discovered in QUM-310.

### 4. Respawn-window verification trick

To verify env-var propagation onto a tmux session without launching the full child agent, use `respawn-window` to start a shell in the session and run `env | grep SPRAWL_` directly:

```bash
_stmux respawn-window -t <session>:0 -k 'bash -c "env | grep SPRAWL_; sleep 5"'
_stmux capture-pane -t <session>:0 -p
```

Useful for QUM-309-class env-propagation tests.

### 5. Manager merge target

Managers spawned by weave share weave's supervisor identity (QUM-384). This means `sprawl merge` from a manager may commit to main instead of the manager's integration branch. When using managers, verify that merged work lands on the manager's branch — not main. If in doubt, use `git -C <worktree> log --oneline -5` to check where commits landed before reporting done.

## Scripted Smoke Tests

For an example of automated sandbox assertions, see `scripts/smoke-test-memory.sh`. It sets up a sandbox, exercises the memory system, and asserts expected outcomes. Run it with:

```bash
bash scripts/smoke-test-memory.sh
```

## Running `claude` from agent bash subshells (QUM-518)

When an agent invokes `claude -p ...` from a Bash tool subshell, Claude Code
sanitizes the subprocess env and strips `CLAUDE_CODE_OAUTH_TOKEN`. The inner
`claude` then fails with `Not logged in`. The fix is a thin shell shim that
re-hydrates auth env vars before exec'ing the real binary.

**Setup (one-time, host side):**

1. Create `.env` at the repo root containing your auth token(s):

   ```
   CLAUDE_CODE_OAUTH_TOKEN=...
   ANTHROPIC_API_KEY=...     # optional
   ```

   Then `chmod 0600 .env`. **`.env` is gitignored — never commit it.**

2. Launch sprawl with the shim as `$SPRAWL_CLAUDE`:

   ```bash
   SPRAWL_CLAUDE=$(pwd)/scripts/run-claude sprawl enter
   ```

`scripts/run-claude` sources `$SPRAWL_ROOT/.env` (falling back to the script's
parent dir if `$SPRAWL_ROOT` is unset) and then `exec`s `claude`. The
`worktree.setup` hook in `.sprawl/config.yaml` copies `.env` into each new
agent worktree (preserving `0600` mode via `cp -p`) so the shim works from
inside worktrees too.

`internal/agent/claude.go` honors `$SPRAWL_CLAUDE`: if set, it is used
verbatim as the `claude` binary path; otherwise it falls back to a `PATH`
lookup.

## Tips

- If a command hangs or behaves unexpectedly, check that you're using `$SPRAWL_BIN` (not a globally installed `sprawl`).
- The sandbox is completely isolated — it won't affect your real `.sprawl/` directory or tmux sessions.
- You can run multiple sandboxes concurrently; each gets a unique namespace.

## Hygiene contract (QUM-458)

The sandbox harness has a defense-in-depth lifecycle to prevent leaks (orphan
`claude` processes, stale tmux sockets, leftover `/tmp/sprawl-*` dirs):

1. **Agent responsibility.** Any agent running sandbox tests must call
   `sprawl_sandbox_destroy` synchronously (and successfully) before reporting
   done via the `report_status` MCP tool (state: "complete"). This is the
   primary cleanup path.
2. **Bash watchdog (Layer 1).** Each e2e driver script (`scripts/test-*-e2e.sh`,
   `scripts/sprawl-test-env.sh`) installs a `setsid`'d watchdog via
   `scripts/lib/sandbox-traps.sh`. If the driver dies abnormally (including
   `kill -9`, which bypasses `trap ... EXIT`), the watchdog kills the
   sandbox tmux server and removes `SPRAWL_ROOT`.
3. **Pdeathsig (Layer 2).** `sprawl enter` spawns `claude` with
   `Pdeathsig=SIGKILL` and its own pgroup, so claude dies if the sprawl host
   is `kill -9`'d.
4. **Orphan watchdog (Layer 3).** `sprawl enter` polls `getppid()` and
   `SPRAWL_ROOT`; if it gets reparented to init or its sandbox root vanishes,
   it self-exits.
5. **Janitor (Layer 4).** `sprawl sandbox-gc [--dry-run] [--max-age=DUR]`
   reaps stale tmux sockets, old `/tmp/sprawl-*` dirs, and orphan claude
   processes. Run it from cron, post-test hooks, or manually when in doubt.
   The `make test-handoff-e2e`, `test-notify-tui-e2e`, `test-tui-e2e`,
   and `test-parallel-agent-viewport-e2e` targets automatically invoke
   `./sprawl sandbox-gc --max-age=10m` after the script regardless of
   pass/fail.

Parent agents (e.g. weave) should periodically run
`./sprawl sandbox-gc --max-age=2h` to catch anything that slipped past
layers 1-4.

The root cause, stated here so this section does not depend on a document
surviving: isolating sandbox tmux onto a dedicated socket was correct, but it
*increased* the leak surface, because every sandbox now spawns its own daemonized
tmux server. Before the socket split, leaked sessions on the default socket got
swept whenever the developer ran `tmux kill-server`; now each leaked sandbox has a
daemon nobody touches. Combined with a missing parent-death contract on `claude`,
every `kill -9` of an agent mid-e2e leaked a sandbox deterministically. That is
what the layers above exist for — and it is why the answer is `sprawl sandbox-gc`,
never a bare `kill-server`.

Longer analysis in `docs/research/qum-458-e2e-leak-analysis.md`. A docs
restructure in flight relocates that file under an archive directory; if the path
above stops resolving, search the tree for the leak analysis by name rather than
assuming it was deleted.

## Why this matters

On 2026-04-21 an agent ran `rm -rf "$SPRAWL_ROOT"` from inside its own worktree (`/home/coder/sprawl/.sprawl/worktrees/finn`) while `$SPRAWL_ROOT` still pointed into the real repo tree. The worktree — and then the real repo — were destroyed, and the root repo had to be re-cloned. The hardened script (cwd guard + `/tmp/` assertion + guarded cleanup trap + `sprawl_sandbox_destroy`) exists so this cannot happen again. Follow the DO-NOT list.
