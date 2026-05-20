# QUM-606: `mcp__sprawl__recover` leaves a zombie — root cause + fix plan

Date: 2026-05-20
Author: ghost (Researcher)
Tracking: [QUM-606](https://linear.app/qumulo-dmotles/issue/QUM-606)
Related: QUM-601 (introduced `recover`), QUM-605 (pending-queue not drained),
QUM-602 (fault banner does not re-fire).

## TL;DR

`AgentRuntime.Recover` passes the **MCP-tool request `ctx`** all the way down
into `realStarter.Start` → `exec.CommandContext(ctx, claude, …)`. When
`toolRecover` returns success, the MCP SDK cancels that `ctx`, the kernel
delivers SIGKILL to the freshly-spawned claude subprocess, and the agent
is dead before any caller can observe it.

The supervisor never notices because the `UnifiedRuntime.Done()` channel —
which is what `AgentRuntime.watchHandleExit` listens on — only closes when
the turn loop exits, and the turn loop runs on an independent
`context.Background()`-derived ctx (see `internal/runtime/unified.go:261`).
A faulted backend session does not exit the turn loop. So
`AgentRuntime.snapshot.Lifecycle` stays `Started`, no
`RuntimeEventStopped` fires, the TUI fault banner does not return, and
both async and interrupt sends silently write into a stdin pipe whose
reader (the SIGKILL'd claude) is gone.

This is **Hypothesis #1 from the issue** ("handle dies before/right after
Start"), with a concrete and verified upstream cause: subprocess lifetime
is tied to the MCP request lifetime.

## Smoking-gun trace

1. `toolRecover(ctx, …)` — `internal/sprawlmcp/server.go:456`. `ctx` is the
   MCP SDK's per-request ctx; it is cancelled after the tool returns.
2. `s.sup.Recover(ctx, name)` — `internal/sprawlmcp/server.go:463`.
3. `Real.Recover` — `internal/supervisor/real.go:714`. Forwards verbatim:
   `runtime.Recover(ctx)` (line 730).
4. `AgentRuntime.Recover` — `internal/supervisor/runtime.go:499`. Builds
   `RuntimeStartSpec`, then `newHandle, err := starter.Start(ctx, spec)`
   (line 556). `ctx` is still the MCP request ctx.
5. `inProcessUnifiedStarter.Start` — `internal/supervisor/runtime_launcher.go:96`.
   Calls `s.startBackendSession(ctx, prep)` (line 104).
6. `startBackendSession` — `internal/supervisor/runtime_launcher.go:237`.
   Calls `unifiedAdapterStartFn(ctx, prep.sessionSpec)` (line 238) which
   resolves to `backendclaude.NewAdapter(...).Start(ctx, spec)`
   (`internal/supervisor/runtime_launcher.go:36-38`).
7. `Adapter.Start` — `internal/backend/claude/adapter.go:61`. Forwards
   `ctx` into `a.starter.Start(ctx, execSpec)` (line 111).
8. `realStarter.Start` — `internal/backend/claude/adapter.go:145`. The
   smoking gun:

   ```go
   cmd := exec.CommandContext(ctx, spec.Path, spec.Args...)
   ```

   `os/exec.CommandContext` arms a kill goroutine that calls
   `cmd.Process.Kill()` (SIGKILL) the instant `ctx` is done. The
   subprocess's lifetime is now bound to the MCP request.

9. `toolRecover` returns `"Recovered backend session for finn"` →
   MCP framework cancels `ctx` → SIGKILL → no claude in `ps -ef`.

### Why the supervisor doesn't notice

`AgentRuntime.Recover` (`internal/supervisor/runtime.go:578-580`) re-arms
the exit watcher:

```go
if doneAware, ok := newHandle.(runtimeHandleDone); ok && doneAware.Done() != nil {
    r.watchHandleExit(newHandle, doneAware.Done())
}
```

`newHandle.Done()` is `UnifiedRuntime.Done()` — backed by `rt.done`, which
`closeDoneOnce.Do(close(rt.done))` only fires after `loopWG.Wait()`
returns (`internal/runtime/unified.go:286`). The turn loop only exits
when:

- its `runCtx` (`context.WithCancel(context.Background())` —
  `internal/runtime/unified.go:261`) is cancelled — only `rt.Stop` /
  `rt.StopWithOptions` does this; or
- something else cancels the loop ctx — nothing else does.

A faulted backend session (terminalErr set by `runReader` on EOF /
SubscriberWedge / HangTimeout) **does not exit the turn loop**.
`TurnLoop.Run` (`internal/runtime/turnloop.go:95-138`) just blocks on
`Queue.Signal()` between turns; when a turn arrives, `Session.StartTurn`
returns `terminalErr` synchronously, the loop publishes
`EventTurnFailed`, sleeps on the signal again, and never exits.

Therefore: subprocess dies → `runReader` exits → `terminalErr` set →
turn loop keeps spinning → `rt.done` stays open → `watchHandleExit`
goroutine never wakes → `Lifecycle` stays `Started`. From sprawl's
perspective the agent is alive.

### Secondary issues observed during the audit

These are not the primary zombie root cause, but they keep the failure
mode invisible and would each independently break the post-fix Recover
behavior. Worth folding into the fix:

1. **`OnResumeFailure` not propagated through Recover.**
   `AgentRuntime.Recover` builds its `RuntimeStartSpec` at
   `internal/supervisor/runtime.go:509-518` without
   `OnResumeFailure`. `RecoverAgents` (the post-restart path) sets it
   (`internal/supervisor/real.go:786-798`). If the resumed
   `--resume <id>` rejects the cookie, the stderr marker scanner is
   never installed (`internal/backend/claude/adapter.go:93-100`
   gates wrapping on `spec.OnResumeFailure != nil`), so the failure
   is undetected at the recover-call layer. This is hypothesis #3
   in the issue.
2. **Turn loop survives terminal session faults.** Independent of the
   ctx-lifetime bug: when `IsTerminallyFaulted()` becomes true, the
   turn loop has no way to exit cleanly. The loop should observe the
   sticky `terminalErr` (or get a fresh `EventBackendFaulted` →
   exit-arm) and unwind so `rt.done` closes. Today the supervisor
   relies on subprocess pipe-EOF to drive Done(), which only happens
   because Stop tears down the pipe — not because the session
   noticed the fault. This is hypothesis #2 in the issue (the race),
   restated structurally: `Done()` cannot fire from a session
   fault, only from `Stop`.
3. **No post-Start health check on the new handle.** Recover assumes
   `starter.Start(...) == nil` ⇒ alive session. Given (1) and the
   "session may immediately re-wedge on `--resume` because the same
   conversation context replays" worry called out in the issue, a
   short observable-health probe (e.g. wait up to 5s for a
   `session_state_changed: running` frame or for `IsTerminallyFaulted()`
   to flip) would convert the silent-success path into an explicit
   failure.

## Reproduction recipe

The bug is reproducible without inducing a real SubscriberWedge — the
ctx-cancellation kill triggers regardless of fault class. The fault
just gets us *into* `Recover`; the zombie is what `Recover` produces.

### Recipe A — direct unit-style repro (no claude binary)

1. `make build`.
2. Write a focused Go test in
   `internal/backend/claude/adapter_test.go` along the lines of:

   ```go
   func TestAdapter_Start_SubprocessSurvivesCallerCtxCancel(t *testing.T) {
       // Skip when no claude on PATH (use the existing skip helper).
       ctx, cancel := context.WithCancel(context.Background())
       a := claude.NewAdapter(claude.Config{})
       sess, err := a.Start(ctx, backend.SessionSpec{
           WorkDir: t.TempDir(),
           SessionID: uuid.NewString(),
           PromptFile: writeTempPrompt(t),
           Resume:   false,
       })
       if err != nil { t.Fatal(err) }
       // Capture PID via a starter-injection seam (TODO: expose
       // Process.Pid through ManagedTransport.Pid() — currently hidden).
       cancel() // simulate MCP request ctx cancel
       // Allow os/exec kill goroutine to fire.
       deadline := time.Now().Add(2 * time.Second)
       for time.Now().Before(deadline) {
           if _, perr := os.FindProcess(pid).Signal(syscall.Signal(0)); perr != nil { break }
           time.Sleep(20 * time.Millisecond)
       }
       // Assertion: process dead.
   }
   ```

   This is the minimum needed to lock down the regression at the
   adapter layer. It only requires a real `claude` binary on PATH
   (or a stub that respects `--print` mode) and exposing a Pid()
   on `ManagedTransport`/transport.

### Recipe B — live end-to-end with a real fault

Per the issue: use `/e2e-testing-sandboxing` to set up a sandbox,
then:

1. Build: `make build`.
2. `eval "$(bash scripts/sprawl-test-env.sh)"`.
3. `sprawl enter` in tmux detached; drive root weave to
   `spawn` an engineer-type child, identity `finn-repro`.
4. Induce a wedge in `finn-repro`. Two practical paths:
   - **F1 SubscriberWedge** — set
     `subscriberSendDeadline = 500 * time.Millisecond` via a test
     build tag, then send finn-repro a turn whose tool produces a
     burst the TUI cannot drain in 500ms (e.g. a tool emitting
     ≥1k consecutive `tool_use` frames). Verify
     `IsTerminallyFaulted()` flips via a peek.
   - **D1 HangTimeout** — set `HangTimeout = 5 * time.Second`
     via the same seam and let the child sit idle in
     `StartTurn` against a stalled stub. Faster; doesn't require
     a frame-burst tool.
5. From weave, call `mcp__sprawl__recover` on `finn-repro`. Assert
   the tool returns success.
6. Within the next 2s, `pgrep -af 'claude.*--resume.*<sess-id>'` and
   assert **at least one** match. Today this assertion fails.
7. Drive a turn post-recover (`messages_send` to finn-repro).
   Assert a new frame lands in finn-repro's activity.ndjson
   within 30s. Today this assertion also fails.

## Fix plan

The fix is small in surface area but cuts across two layers. Two
required changes, three recommended.

### Required (closes the primary zombie)

**R1. Detach subprocess lifetime from the MCP-request ctx in
`AgentRuntime.Recover`.**

`internal/supervisor/runtime.go:556`. Replace

```go
newHandle, err := starter.Start(ctx, spec)
```

with a Background-derived ctx so the subprocess outlives the recover
request. Mirror the precedent at `internal/supervisor/real.go:492`
(`runtime.Start(context.Background())` in `Spawn`). Concretely:

```go
// Subprocess lifetime must outlive the MCP request ctx — otherwise
// exec.CommandContext SIGKILLs the new claude as soon as toolRecover
// returns. See QUM-606. The new handle has its own teardown path
// (Stop / StopAbandon / watchHandleExit), so the ctx-cancel safety
// net is unnecessary here.
newHandle, err := starter.Start(context.Background(), spec)
```

Same fix should be audited for `StartResume` (line 357) which is on
the post-restart path — currently it already runs under the
`RecoverAgents` flow whose ctx is the long-lived enter ctx, but if
that ever changes we want both call sites consistent. Make
`startWithSpec` (line 297) also use `context.Background()` internally
and accept the original ctx only as a logical cancellation hint
(not propagated to `exec.CommandContext`).

**R2. Close `UnifiedRuntime.Done()` on terminal session fault.**

`internal/runtime/unified.go` + `internal/runtime/turnloop.go`. Two
sub-options; recommend (a) for minimal surface:

(a) In `TurnLoop.Run`, after `Session.StartTurn` returns and after
    each turn ends, probe `terminalErrSession.IsTerminallyFaulted()`
    (via a small interface on the session handle). If true, publish
    `EventStopped` and return so `loopWG.Wait()` unblocks and
    `closeDoneOnce` fires. The supervisor's `watchHandleExit`
    transitions lifecycle to `Stopped` and emits
    `RuntimeEventStopped`, which the TUI's fault subscriber already
    consumes.

(b) Have `session.setTerminalErr` cancel `runCtx` of the turn loop
    via a runtime-installed callback. More plumbing; same effect.

This change is independent of R1 but necessary: without it, any
future code path that produces a terminal session fault (not just
the ctx-cancel of R1) leaves the same zombie.

### Recommended (close the latent failure modes)

**R3. Propagate `OnResumeFailure` through `Recover`.** Add the same
callback `RecoverAgents` uses (`internal/supervisor/real.go:786-798`,
flips Status to `StatusResumeFailed`) into the `RuntimeStartSpec`
built at `internal/supervisor/runtime.go:509-518`. Without it, the
`--resume <bad-id>` failure mode is silent.

**R4. Post-Start health probe in `Recover`.** After `starter.Start`
returns success, wait up to ~5s for either (i) a frame on the
handle's EventBus (any frame demonstrates the subprocess is alive
and serving) or (ii) `IsTerminallyFaulted()` to flip. On timeout or
fault, tear the new handle down via `StopAbandon` and return an
error so `toolRecover` reports failure to the caller. This is the
"return non-success error AND emit `RuntimeEventStopped`" requirement
in the issue's acceptance criteria.

**R5. Expose subprocess PID on `ManagedTransport`.** Lets tests
assert the SIGKILL path directly without scraping `ps`. Add
`Pid() int` to the interface, return `cmd.Process.Pid` in
`realStarter`. Cheap; tightens the harness in §"Live-smoke harness
sketch" below.

### Sketch test matrix after fix

| Test | Layer | Asserts |
|------|-------|---------|
| `TestAdapter_Start_SubprocessSurvivesCallerCtxCancel` (new) | adapter | After `cancel()` of the ctx passed to `Start`, the cmd survives because Recover passes Background. (Negative — requires R1.) |
| `TestRuntime_Recover_DoesNotKillNewHandleOnCallerCtxCancel` (new) | runtime, supervisor | Inject a stubbed starter that records the ctx; assert Recover passes a non-cancellable ctx to it. |
| `TestUnifiedRuntime_TerminalFault_ClosesDone` (new) | runtime | Drive a session fault on a started runtime; assert `Done()` closes within bounded time. (Requires R2.) |
| `TestAgentRuntime_Recover_FlipsLifecycleStoppedOnFault` (new) | supervisor | Given a starter that returns a pre-faulted handle, Recover returns error and emits `RuntimeEventStopped`. (Requires R4.) |
| `make test-recover-live-e2e` (new) | end-to-end | Sketch below. |

## Live-smoke harness sketch (`make test-recover-live-e2e`)

Follows the precedent of `test-handoff-e2e.sh` and
`test-ask-user-question-e2e.sh` — real `claude` binary, isolated
`/tmp` sandbox, tmux detached pane, assertions via `state.json` +
`activity.ndjson` + `pgrep`.

```bash
#!/usr/bin/env bash
# scripts/test-recover-live-e2e.sh — guards QUM-606.
set -euo pipefail
: "${SPRAWL_E2E_SKIP_NO_CLAUDE:=0}"
command -v claude >/dev/null 2>&1 || {
  [[ "$SPRAWL_E2E_SKIP_NO_CLAUDE" = "1" ]] && exit 0
  echo "claude not on PATH"; exit 1
}

# Phase 0: sandbox.
eval "$(bash scripts/sprawl-test-env.sh)"

# Phase 1: launch enter under tmux; drive weave to spawn a child finn.
# (Reuse the spawn-helper invocation pattern from test-ask-user-question-e2e.sh.)
_stmux send-keys -t "$SPRAWL_NAMESPACE" "<prompt to spawn finn engineer>" Enter
wait_for_state finn active 60

CHILD_PID="$(pgrep -af "claude.*finn" | head -1 | awk '{print $1}')"
[[ -n "$CHILD_PID" ]] || { echo "no finn claude PID"; exit 1; }

# Phase 2: induce a SubscriberWedge fault on finn via the F1 test seam.
# (Requires a build-tag-gated env var that lowers subscriberSendDeadline
# and an `mcp__sprawl_test__wedge` tool gated behind the same tag, OR
# alternatively the D1 HangTimeout pathway with the watchdog interval
# lowered to a few seconds.)
SPRAWL_TEST_F1_WEDGE=1 _stmux send-keys -t "$SPRAWL_NAMESPACE" \
  "<prompt that triggers finn to emit a frame-burst>" Enter
wait_for_fault_banner finn 60   # asserts the TUI shows the wedge

# Phase 3: drive weave to call recover on finn, assert success.
_stmux send-keys -t "$SPRAWL_NAMESPACE" \
  "Call mcp__sprawl__recover with agent_name=\"finn\"" Enter
wait_for_text_in_pane "Recovered backend session for finn" 30

# Phase 4 — PRIMARY ASSERTION: a new claude --resume subprocess is alive
# 2 seconds after the recover return.
sleep 2
NEW_PID="$(pgrep -af 'claude.*--resume.*' | grep -v "$CHILD_PID" | head -1 | awk '{print $1}')"
[[ -n "$NEW_PID" ]] || { echo "FAIL: no live claude --resume subprocess"; exit 1; }
[[ "$NEW_PID" != "$CHILD_PID" ]] || { echo "FAIL: same PID as pre-recover"; exit 1; }

# Phase 5 — drive a post-recover turn and assert frames arrive within 30s.
PROBE="RECOVER-PROBE-$(uuidgen | head -c8)"
_stmux send-keys -t "$SPRAWL_NAMESPACE" \
  "Call mcp__sprawl__send_message to=finn body=\"echo $PROBE in your reply\"" Enter
wait_for_grep_in_file ".sprawl/agents/finn/activity.ndjson" "$PROBE" 60

echo "PASS: QUM-606 live-recover smoke"
```

Wiring requirements for this script to work:

1. A test-only fault-injection seam (build-tag gated) — either:
   - `mcp__sprawl_test__induce_wedge` MCP tool that calls
     `setTerminalErr(ErrSubscriberWedged)` on a named agent's session
     directly; or
   - A `SPRAWL_TEST_HANG_TIMEOUT=2s` env var that lowers
     `defaultHangTimeout` and a `SPRAWL_TEST_SUBSCRIBER_DEADLINE`
     for F1.

   The first is more surgical and less timing-dependent.
2. Helper functions `wait_for_state`, `wait_for_fault_banner`,
   `wait_for_text_in_pane`, `wait_for_grep_in_file` factored from
   `test-ask-user-question-e2e.sh` / `test-handoff-e2e.sh` into a
   shared `scripts/lib/e2e-helpers.sh` (a follow-up worth doing
   regardless).
3. Makefile target:

   ```make
   test-recover-live-e2e: build
   	bash scripts/test-recover-live-e2e.sh; rc=$$?; ./sprawl sandbox-gc --max-age=10m || true; exit $$rc
   ```

4. CLAUDE.md mandatory-test bullet: any change to
   `internal/supervisor/runtime.go` (Recover path),
   `internal/supervisor/real.go` (Recover wrapper),
   `internal/backend/claude/adapter.go` (subprocess lifetime),
   `internal/runtime/unified.go` (Done() closure), or
   `internal/runtime/turnloop.go` (loop exit on fault) must run
   `make test-recover-live-e2e`.

## Acceptance criteria mapping (from QUM-606)

| Issue criterion | Covered by |
|-----------------|------------|
| After success, `ps` shows exactly one live claude per recovered agent | R1; smoke phases 4 + harness assertion |
| If recover fails, return non-success error AND emit `RuntimeEventStopped` | R2 + R4; new supervisor test `Recover_FlipsLifecycleStoppedOnFault` |
| Add a smoke test: fault → recover → assert subprocess PID alive | `test-recover-live-e2e.sh` (phases 2–4) |
| Live-recover smoke (real claude): drive a turn post-recover, assert frames | `test-recover-live-e2e.sh` phase 5 |

## Reflection

**What surprised me.** Two things:

1. The bug is not in the recover *swap dance* (which I expected from
   the issue framing). The dance is fine — atomic swap, watcher
   detached before StopAbandon, watcher rearmed after the swap. The
   bug is one line earlier: the wrong ctx flowed down. The fix is
   trivial; the failure mode is severe.
2. `UnifiedRuntime.Done()` deliberately does **not** observe backend
   faults. The comment at `internal/runtime/unified.go:260` says
   "the turn loop must outlive the Start caller's ctx" — which is
   the right intent for Spawn (don't tear the loop down because
   Spawn's ctx cancelled), but the consequence is that
   `watchHandleExit` is structurally blind to backend-session
   death. QUM-602 ("fault banner does not re-fire after silent
   re-fault") is probably the same underlying gap surfaced from a
   different angle.

**Open questions.**

- Does this affect the *post-restart* `RecoverAgents` path too? I
  believe not — that runs under sprawl-enter's long-lived ctx, not
  an MCP request ctx — but it's worth a targeted check.
- Should `Real.Recover` actively block until the new handle proves
  healthy (R4), or is "return immediately, surface fault via the
  fault subscriber" the better contract? I argue for R4 because the
  recover MCP tool today returns a string status to a single caller
  who has no other way to learn the recovery actually took.
- The "could the resumed `--resume <id>` immediately re-wedge?"
  concern in the issue is real: the conversation transcript replays
  on resume. If a wedge-producing frame is in transcript history,
  the new session might fault on the same frame. R4's health probe
  catches this; a full fix may require the recover path to be able
  to elect a *fresh* session (clean slate) on the second consecutive
  wedge.

**Would-investigate-next.**

- Whether QUM-602's "fault banner does not re-fire" is the same
  bug as the "no `RuntimeEventStopped` emitted" half of QUM-606 —
  high-confidence guess: yes, single fix, two symptoms.
- Audit every other use of `exec.CommandContext` in the codebase
  for the same request-ctx-leak pattern. Sandbox-side helper
  subprocesses are most at risk.
- Whether `--resume <id>`'s transcript replay re-triggers the
  original wedge in practice — needs the live harness to be wired
  before we can measure it.
