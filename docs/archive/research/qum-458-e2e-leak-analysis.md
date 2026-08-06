# QUM-458: e2e Sandbox Leak — Root-Cause Analysis & Fix Design

Author: ghost (research agent)
Date: 2026-05-04
Status: research deliverable; **no production code modified**

## TL;DR

Every `make test-*-e2e` script can leak its sandbox (tmux server + `sprawl enter` host + `claude` subprocess + `/tmp` dir + phantom `script` wrapper) when the bash test driver dies abnormally. The leak surface has **four** independent gaps, none of which alone is fatal but which compound on SIGKILL:

1. **Bash test drivers** trap `EXIT` only. SIGKILL bypasses it; INT/TERM/HUP technically reach EXIT but no extra trap is registered for defense in depth.
2. **`tmux -L <socket> new-session -d`** daemonizes the tmux server (reparented to PID 1). It outlives the spawning bash. There is no parent-death link.
3. **`sprawl enter` (Go host)** has *no* orphan watchdog. It only forwards SIGTERM/SIGHUP into the bubbletea program. Nothing detects `getppid()==1`, a vanished `SPRAWL_ROOT`, or an unreachable tmux server.
4. **The spawned `claude` subprocess** is started via plain `exec.CommandContext` — no `SysProcAttr.Pdeathsig`. If `sprawl enter` is `kill -9`'d, claude is reparented to init and runs indefinitely (this is the dollar-burning leak).

Cause #4 is what makes the recurrence so expensive: 7 × `claude --model opus[1m]` waiting on stdin for ~2h is real money even idle.

The fix is defense in depth across all four layers — no single layer can prevent every scenario, but together they make any normal kill (including SIGKILL of any single ancestor) self-cleaning. A `sprawl sandbox-gc` janitor catches the residue.

---

## 1. Inventory: which scripts spawn sandboxes?

```
$ git ls-files scripts | grep -E 'test-.*e2e\.sh|sprawl-test-env\.sh'
scripts/sprawl-test-env.sh
scripts/test-handoff-e2e.sh
scripts/test-mcp-identity-e2e.sh
scripts/test-notify-tui-e2e.sh
scripts/test-parallel-agent-viewport-e2e.sh
scripts/test-skip-wake-unified-e2e.sh
scripts/test-state-divergence-e2e.sh
scripts/test-tui-e2e.sh
```

Note: the prompt mentioned `scripts/test-init-e2e.sh` — **does not exist**. The
tmux-mode `sprawl init` parent entrypoint was removed in QUM-346 (M13 cutover);
see `scripts/sprawl-test-env.sh:7-9`. The parallel-agent and skip-wake scripts
were not in the original inventory but share the same trap-on-EXIT pattern.

Each script that launches the TUI in detached tmux:

| Script | Socket pattern | Spawns claude? | Trap |
|---|---|---|---|
| `test-handoff-e2e.sh` | `sprawl-handoff-e2e-$$` | yes | `EXIT` only (line 233) |
| `test-notify-tui-e2e.sh` | `sprawl-notify-e2e-$$` | yes | `EXIT` only (line 173) |
| `test-tui-e2e.sh` | `sprawl-tui-e2e-$$` | yes | `EXIT` only (line 153) |
| `test-parallel-agent-viewport-e2e.sh` | (per script) | yes (via spawn) | `EXIT` only (line 152) |
| `test-mcp-identity-e2e.sh` | (per script) | yes | `EXIT` only (line 109) |
| `test-state-divergence-e2e.sh` | n/a | no (CLI-only) | `EXIT` only (line 65) |
| `sprawl-test-env.sh` (eval'd) | `sprawl-sandbox-$NS` | indirectly | `EXIT` only (line 140) |

Trap signal sets confirmed via:
```
grep -n "trap " scripts/test-*-e2e.sh scripts/sprawl-test-env.sh
```
None register `INT TERM HUP` explicitly (bash EXIT covers them in practice but
not when the script crashes mid-cleanup or is `kill -9`'d).

---

## 2. Leak paths (with file:line evidence)

### Path A — SIGKILL of bash driver mid-run

```
bash test-handoff-e2e.sh           ← SIGKILL'd by mcp__sprawl__kill or OOM
├── tmux server (daemon, ppid=1)   ← survives, holds:
│   └── /bin/sh -c "SPRAWL_ROOT=… sprawl enter 2>…"
│       └── sprawl enter (Go host) ← survives, holds:
│           └── claude --model opus[1m] -p --stream-json …  ← survives
└── script -q -c "tmux attach …" /dev/null &   ← daemonized via &, ppid=1, survives
```

`scripts/test-handoff-e2e.sh:233` registers `trap cleanup EXIT`. Bash signal
semantics: SIGKILL **never** invokes the EXIT trap. The cleanup body
(`scripts/test-handoff-e2e.sh:220-232`) does
`_stmux kill-session` + `rm -rf $SPRAWL_ROOT`; if it never runs, every layer
above remains.

The detached `script` wrapper at `scripts/test-handoff-e2e.sh:295-296`:
```bash
script -q -c "tmux ${SPRAWL_TMUX_SOCKET:+-L $SPRAWL_TMUX_SOCKET} attach …" /dev/null \
    >/dev/null 2>&1 &
PHANTOM_PID=$!
```
is launched in the background — it's parented to bash, but bash death
re-parents it to init, and there's no `prctl(PR_SET_PDEATHSIG)` link. It keeps
its tmux client attached, which keeps the tmux server's session alive even if
something later tries `kill-session`.

### Path B — SIGKILL of `sprawl enter` (host) directly

```
sprawl enter            ← SIGKILL'd
└── claude (subprocess) ← reparented to init, survives
```

Evidence: `internal/agentloop/real_starter.go:24-39` builds the claude command
with no `SysProcAttr`:
```go
cmd := exec.CommandContext(ctx, claudePath, args...)
cmd.Dir = config.WorkDir
…
cmd.Stderr = os.Stderr
…
if err := cmd.Start(); err != nil { … }
```
No `cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}`. On
`exec.CommandContext` cancellation **or** `cancelFn()` (line 64), claude is
killed cleanly — but only if the Go runtime is alive to run the cancel logic.
SIGKILL of `sprawl enter` skips Go's deferred cleanup and leaves claude
orphaned. (No `Pdeathsig` anywhere in the tree:
`grep -r 'Pdeathsig\|SysProcAttr\|prctl' internal/ cmd/` returns nothing.)

### Path C — bash inside the tmux session dies, sprawl enter survives

The shell command line passed to `tmux new-session -d` is
`SPRAWL_ROOT='…' '$SPRAWL_BIN' enter 2>'$STDERR_LOG'`
(`scripts/test-handoff-e2e.sh:238-239`). When that exec finishes, tmux notes
the pane process exited and either closes the window (`remain-on-exit off`,
default) or keeps it (depending on tmux config). On most installs, the window
closes and the session closes when its last window dies — but `sprawl enter`
runs as the foreground `exec`, so the bash that wrapped it is replaced by
sprawl. SIGKILL of `sprawl enter` here closes the tmux pane → session → server
cleanly… *unless* the phantom `script` client in Path A is still attached,
which keeps the server up with no foreground process.

### Path D — `sprawl enter` survives indefinitely with no input

`cmd/enter.go:184-191` only handles SIGTERM/SIGHUP:
```go
signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGHUP)
```
There is no:
- `getppid()==1` watchdog,
- SPRAWL_ROOT-existence watchdog,
- idle timeout,
- heartbeat against the parent test driver.

So once orphaned, it sits there. The inner claude subprocess sits on stdin
forever (the stream-json transport is request/response — claude waits for the
next message from the TUI bridge, which is alive but receiving no user input).
**Claude itself has no built-in idle exit** when run with `-p
--input-format stream-json`; it stays alive as long as stdin is open. Stdin
stays open as long as the Go bridge stays open. The bridge stays open as long
as `sprawl enter` is alive. Loop.

### Path E — `sprawl-test-env.sh`-style eval'd traps

`scripts/sprawl-test-env.sh:127-140` installs an EXIT-only trap into the
caller's shell. If the agent's shell dies abnormally (e.g. its claude host is
killed by harness retire), the trap is bypassed and the sandbox tmux session +
SPRAWL_ROOT leak. The sandbox tmux socket
`sprawl-sandbox-$TEST_NS` (line 97) becomes one of the ~150 stale sockets
listed in QUM-458's symptom section.

---

## 3. Why the leak is structural, not a script bug

The original QUM-325 fix (dedicated tmux socket per sandbox) was correct in
isolating sandbox tmux state from the developer's default socket — but it
*increased* the leak surface, because every sandbox now spawns its **own
daemonized tmux server**. Before QUM-325, leaked sessions on the default
socket got swept away whenever the developer ran `tmux kill-server`. Now each
leaked sandbox has a dedicated daemon nobody touches.

Combined with cause #4 (no `Pdeathsig` on claude), and the fact that the
harness uses `mcp__sprawl__kill` (which sends SIGKILL via
`internal/sprawlmcp/server.go:107`'s `toolKill` → supervisor kill path) when
agents misbehave, every kill of an agent that is mid-`make test-handoff-e2e`
deterministically leaks one sandbox.

This is not a "tests are flaky" problem. It is "the test harness has no
parent-death contract."

---

## 4. Deterministic reproducers

All three repros run from the repo root with `claude` on PATH. They produce
the exact leak pattern QUM-458 documents.

### Reproducer R-A: SIGKILL the bash test driver mid-run

```bash
# Terminal 1
bash scripts/test-handoff-e2e.sh &
DRIVER_PID=$!
sleep 30   # let the TUI come up and claude spawn

# Terminal 2 (or same shell, after backgrounding)
kill -9 $DRIVER_PID

# Terminal 3 — observe the leak
sleep 5
ps -ef | grep -E 'sprawl-handoff-e2e|claude --model'
ls /tmp/tmux-$(id -u)/ | grep sprawl-handoff-e2e
ls -d /tmp/sprawl-handoff-e2e-* 2>/dev/null

# Expected: 1 tmux server, 1 sprawl enter, 1 claude opus, 1 script wrapper,
# 1 /tmp/sprawl-handoff-e2e-* dir, 1 stale tmux socket.
# These persist indefinitely without manual reaping.
```

### Reproducer R-B: SIGKILL of `sprawl enter` only

```bash
# Set up a sandbox with a live sprawl enter (no test driver involvement).
SPRAWL_ROOT=$(mktemp -d /tmp/sprawl-rb-XXXXXX)
git -C "$SPRAWL_ROOT" init -b main -q
git -C "$SPRAWL_ROOT" -c user.email=t@t -c user.name=t commit --allow-empty -m init -q
mkdir -p "$SPRAWL_ROOT/.sprawl"; echo weave > "$SPRAWL_ROOT/.sprawl/root-name"

SPRAWL_ROOT="$SPRAWL_ROOT" ./sprawl enter &
ENTER_PID=$!
sleep 15  # wait for claude to spawn

CLAUDE_PID=$(pgrep -P $ENTER_PID -f claude)
kill -9 $ENTER_PID

sleep 2
# Expected: $CLAUDE_PID is alive, ppid=1, still consuming Opus session capacity.
ps -p $CLAUDE_PID -o pid,ppid,cmd
```

This isolates cause #4 (missing `Pdeathsig`) from the bash/tmux layers.

### Reproducer R-C: bash trap registered but skipped

```bash
# Demonstrates that EXIT trap is skipped on SIGKILL, even though the
# author "set" a trap. Run this and observe the trap message:
bash -c 'trap "echo CLEANUP RAN" EXIT; sleep 30' &
PID=$!
kill -9 $PID
wait
# Expected: no "CLEANUP RAN" output. Same mechanism as the e2e scripts.
```

---

## 5. Fix design — defense in depth

Each layer plugs a different gap. None alone is sufficient; together they make
the leak deterministically self-cleaning under any kill scenario short of
"machine power loss" (which the sandbox-gc janitor handles on next boot).

### Layer 1 — Bash side: stronger traps + watchdog companion

**Change 1.1.** Expand the trap to all human-deliverable signals:

```bash
trap cleanup EXIT INT TERM HUP
```

This is cosmetic for the SIGKILL case but adds robustness against (a) cleanup
crashing midway and re-raising, (b) operator Ctrl+C at the wrong moment.

**Change 1.2.** Spawn a setsid'd watchdog that polls the driver pid and runs
cleanup if it dies. Survives SIGKILL of the driver because it is detached:

```bash
# At the top of the script, after computing SPRAWL_ROOT and SPRAWL_TMUX_SOCKET:
( setsid bash -c '
    DRIVER=$1; SOCKET=$2; ROOT=$3; SESSION=$4
    while kill -0 "$DRIVER" 2>/dev/null; do sleep 2; done
    # Driver died. Reap whatever we registered.
    tmux -L "$SOCKET" kill-server 2>/dev/null || true
    case "$ROOT" in /tmp/*) rm -rf -- "$ROOT" ;; esac
  ' _ "$$" "$SPRAWL_TMUX_SOCKET" "$SPRAWL_ROOT" "$SESSION"
) </dev/null >/dev/null 2>&1 &
disown
```

The watchdog must be `setsid`'d (no controlling terminal) and `disown`'d so
it's not attached to the driver's job table — otherwise SIGHUP propagation can
take it down with the driver. The `kill -0` poll is cheap (signal 0 = error
check only).

**Change 1.3.** Replace `tmux kill-session` with `tmux -L $SOCKET kill-server`
in cleanup. Since each sandbox owns its socket (QUM-325), killing the whole
server is fine and avoids the case where leftover phantom clients keep the
session alive past `kill-session`.

### Layer 2 — Go: `Pdeathsig` on every spawned subprocess (Linux)

**Change 2.1.** In `internal/agentloop/real_starter.go:24`, set
`Pdeathsig=SIGKILL` on the claude command:

```go
cmd := exec.CommandContext(ctx, claudePath, args...)
cmd.Dir = config.WorkDir
cmd.SysProcAttr = &syscall.SysProcAttr{
    Pdeathsig: syscall.SIGKILL,
    Setpgid:   true,                  // own pgid, so cancelFn can pgkill the whole tree
}
```

This is a Linux-only kernel feature (`prctl(PR_SET_PDEATHSIG, SIGKILL)`); on
darwin it must be guarded behind a build tag. The current dev/CI env is Linux
only, so the simplest path is `//go:build linux` for a `setSysProcAttr` helper
plus a no-op on darwin. The `Setpgid` addition lets `cancelFn` do
`syscall.Kill(-pid, SIGKILL)` to kill the whole pgroup (catches MCP servers
spawned by claude itself, which inherit the same pgroup).

**Change 2.2.** Mirror the same `SysProcAttr` on every other `exec.Command`
that launches a long-lived subprocess. Audit list (one liner):
`grep -rn 'exec\.Command\b' internal/ cmd/ | grep -v _test`. The notable
non-test callers are:
- `internal/agentloop/real_starter.go:24` — claude subprocess (this is the leak)
- `internal/agentops/helpers.go:31` — `bash -e` script runner (short-lived,
  but should still get Pdeathsig)
- `internal/merge/git.go` — git subprocesses (short-lived; lower priority)

### Layer 3 — Go: orphan watchdog inside `sprawl enter`

**Change 3.1.** In `cmd/enter.go` near the existing
`signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGHUP)` block (line 186), add
an orphan watchdog goroutine:

```go
// Defense in depth: in sandbox/test mode, exit if we get reparented to
// init (parent died without delivering SIGHUP — typical SIGKILL case).
// QUM-458.
if os.Getenv("SPRAWL_TEST_MODE") == "1" || strings.HasPrefix(sprawlRoot, "/tmp/") {
    go func() {
        // initial ppid; a transition to 1 means parent died.
        startPPID := os.Getppid()
        ticker := time.NewTicker(5 * time.Second)
        defer ticker.Stop()
        for range ticker.C {
            ppid := os.Getppid()
            if ppid == 1 && startPPID != 1 {
                fmt.Fprintf(os.Stderr, "[enter] orphaned (ppid=1), exiting\n")
                p.Quit()
                return
            }
            // Bonus: SPRAWL_ROOT vanished (test driver rm -rf'd it).
            if _, err := os.Stat(sprawlRoot); os.IsNotExist(err) {
                fmt.Fprintf(os.Stderr, "[enter] SPRAWL_ROOT %s vanished, exiting\n", sprawlRoot)
                p.Quit()
                return
            }
        }
    }()
}
```

**Why scope it to sandbox/test mode?** Production `sprawl enter` is meant to
survive its launching shell — users run it in tmux, ssh detach, etc. The
orphan check would falsely trip there. Gate by `SPRAWL_TEST_MODE=1` (already
set by `sprawl-test-env.sh:96`) plus a `/tmp/` prefix check.

**Why 5s polling?** `prctl(PR_SET_PDEATHSIG)` would be more elegant but it
fires on the *thread*, not the process; Go's M:N scheduling makes it unsafe
for arbitrary goroutines. Polling `getppid()` every 5s is the portable
equivalent and adds <0.01% CPU.

### Layer 4 — Sandbox janitor: `sprawl sandbox-gc`

**Change 4.1.** New CLI command `sprawl sandbox-gc [--dry-run] [--max-age=2h]`
that:
1. Lists `/tmp/tmux-*/sprawl-*` sockets, queries each via `tmux -L <socket> ls`. If error or no sessions, kill the server and unlink the socket.
2. Lists `/tmp/sprawl-*-e2e-*` and `/tmp/sprawl-test-*` directories. For each, check if any process has its `--system-prompt-file` argv pointing inside that dir. If none and dir mtime > max-age, `rm -rf`.
3. Lists `claude` processes whose `--system-prompt-file` argv is under `/tmp/sprawl-*` and whose process tree root is PID 1 (orphaned). Kill them.

Idempotent. Safe to run from cron or post-test hooks.

**Change 4.2.** Wire `make test-handoff-e2e` (and siblings) to invoke
`./sprawl sandbox-gc --max-age=10m` after the test, regardless of pass/fail.
This catches anything Layer 1 missed.

### Layer 5 — Documentation

**Change 5.1.** Update `.claude/skills/e2e-testing-sandboxing/SKILL.md` with a
"Hygiene contract" section: agents that run sandbox tests are responsible for
calling `sprawl_sandbox_destroy` synchronously before reporting done; the
parent (weave) periodically runs `sprawl sandbox-gc` to catch the rest.

---

## 6. Validation criteria for the fix

The fix is verifiable via a new repro test, e.g.
`scripts/test-leak-resistance-e2e.sh`:

1. `bash scripts/test-handoff-e2e.sh &` (background)
2. Wait 20 s for sandbox to come up.
3. `kill -9 $!` (driver).
4. `sleep 10`.
5. Assert: zero `claude` processes whose `--system-prompt-file` is under
   `/tmp/sprawl-handoff-e2e-*`.
6. Assert: zero tmux sockets matching `/tmp/tmux-$(id -u)/sprawl-handoff-e2e-*`.
7. Assert: zero `/tmp/sprawl-handoff-e2e-*` directories.
8. Repeat for `test-notify-tui-e2e.sh`, `test-tui-e2e.sh`,
   `test-mcp-identity-e2e.sh`, `test-parallel-agent-viewport-e2e.sh`.

Add this script to `make leak-test` and run it in CI nightly.

---

## 7. Open questions / not-yet-investigated

1. **Resume cookie + Pdeathsig interaction.** When `Pdeathsig=SIGKILL` fires
   on claude, can the pending session-id cookie in
   `.sprawl/memory/last-session-id` be re-used cleanly on next launch? Likely
   yes (resume just reads the saved transcript), but worth verifying.
2. **Setpgid vs the existing `cmd.Process.Kill()`.** Go's `Process.Kill` sends
   SIGKILL to the leader pid only. Adding `Setpgid` requires switching
   `cancelFn` to `syscall.Kill(-pgid, SIGKILL)` — otherwise grandchildren
   (e.g. claude's own MCP servers) leak on normal teardown.
3. **Darwin support.** `Pdeathsig` is Linux-only. The repo currently runs
   only on Linux (checked CI), but `internal/claude/resumewatch.go:128`
   mentions FD inheritance issues that hint at past darwin-vs-linux pain. The
   `setSysProcAttr` helper should `//go:build linux` cleanly so darwin doesn't
   regress.
4. **The Apr 17 `tmux ⚡` root-loop.** QUM-458's symptom #4 mentions a
   long-running orphan from the *legacy* `sprawl init` tmux-mode loop. Since
   QUM-346 removed that entrypoint, this should not recur for new sessions —
   but old scaffolds in the wild (e.g. forgotten dev VMs) still leak. The
   `sandbox-gc` janitor should also reap `/tmp/tmp.*/repo` style scaffolds.
5. **The phantom `script` wrapper.** Even after Layer 1, the
   `script -q -c 'tmux attach' &` wrapper is detached. Best fix is to register
   it explicitly in cleanup (already done via `PHANTOM_PID` trap path) but
   also add it to the watchdog's reap list so it dies on driver SIGKILL too.

---

## 8. Reflection (research process)

**Surprising:** The `Pdeathsig` gap is the single highest-leverage fix —
adding 3 lines of Go to `real_starter.go` would have prevented every
"orphan claude opus[1m]" line item in the bug report, since the test driver's
crash chain *almost* always passes through `sprawl enter`. The bash trap
hardening is necessary but secondary; the $/hour leak is in the Go layer.

**Open questions I'd chase next:**
- Whether MCP servers spawned *by claude* (claude's own subprocess tree) also
  leak independently of the host. `Setpgid` + `kill -pgid` should catch them,
  but I didn't validate empirically.
- Whether QUM-411's `CLAUDE_CODE_OAUTH_TOKEN` recovery shim could be repurposed
  to detect the test-harness execution context (ancestor walk already happens)
  and toggle the watchdog without needing `SPRAWL_TEST_MODE`.

**What I would build next given more time:**
1. A 30-line `sprawl sandbox-gc` skeleton (Cobra command in `cmd/`,
   filesystem-only, no claude required) so the janitor can be deployed before
   the bigger Go-side `Pdeathsig` change lands.
2. An empirical leak-resistance test driver that wraps each existing e2e
   script in a `setsid` + watchdog harness, confirming the proposed Layer 1
   pattern works end-to-end.

---

## 9. References

- `scripts/test-handoff-e2e.sh:90, 220-239, 295-296` — socket allocation, cleanup, phantom client
- `scripts/test-notify-tui-e2e.sh:77, 163-179` — same pattern
- `scripts/test-tui-e2e.sh:41, 137-153, 422-430` — same pattern + has the only orphan-count assertion
- `scripts/sprawl-test-env.sh:97, 102-141` — eval'd traps, sanctioned `sprawl_sandbox_destroy`
- `cmd/enter.go:184-192` — signal handling (only TERM/HUP)
- `internal/agentloop/real_starter.go:24-55` — claude subprocess launch (no Pdeathsig)
- `internal/sprawlmcp/server.go:107` — MCP `kill` tool path (delivers SIGKILL through supervisor)
- `internal/claude/resumewatch.go:128` — pre-existing comment on FD inheritance to orphan grandchildren (related concern)
- QUM-325 — dedicated tmux socket per sandbox (the change that made each leak self-contained)
- QUM-346 — removal of `sprawl init` tmux-mode parent (legacy leak source)
- QUM-411 — CLAUDE_CODE_OAUTH_TOKEN recovery shim (ancestor-pid walk pattern reusable here)
