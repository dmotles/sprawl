# Design: Unify TUI-Mode Weave Init with Tmux-Mode

## Status: Draft (QUM-252)

## Context

The root weave agent has two launch paths today:

- **tmux mode** — `cmd/rootloop.go`, invoked via `sprawl _root-session` inside a bash restart loop. This path is the battle-tested one: it writes the system prompt to disk, tracks session IDs, auto-summarizes missed handoffs, runs the consolidation pipeline after every handoff, and updates persistent knowledge.
- **TUI mode** — `cmd/enter.go` + `internal/tui/bridge.go` + `internal/host/session.go`. This path launches Claude as a stream-json subprocess and injects the system prompt as a string in the SDK `initialize` control request. It has **none** of the memory/handoff/session-resume machinery.

Every time the user restarts `sprawl enter`, weave starts with a blank context: no session log persisted, no timeline continuity, no resumable conversation, no handoff consolidation. Meanwhile tmux weave is stateful and self-healing. The two implementations have drifted to the point where "weave" means different things depending on how you entered the system — that is a maintenance trap and a UX regression for anyone who prefers the TUI.

This doc proposes the primary path to converge the two, with the `sprawl-ops` MCP channel as the only mode-specific delta.

## 1. Side-by-Side Comparison

| Concern | tmux (`cmd/rootloop.go`) | TUI (`cmd/enter.go` / `defaultNewSession`) |
|---|---|---|
| Prompt construction | `agent.BuildRootPrompt(PromptConfig{Mode: "tmux"})` | `agent.BuildRootPrompt(PromptConfig{Mode: "tui"})` |
| Context blob | `memory.BuildContextBlob(sprawlRoot, "weave")` before launch | same — good, already shared |
| Prompt delivery | On disk: `state.WriteSystemPrompt` → `.sprawl/agents/weave/SYSTEM.md` → passed via `--system-prompt-file` | In-memory string, injected via SDK `initialize` control request (`host.SessionConfig.SystemPrompt`, session.go:48). Not on disk, not inspectable. |
| `LaunchOpts` fields used | `SystemPromptFile`, `Tools`, `AllowedTools`, `DisallowedTools`, `Name`, `Model: "opus[1m]"`, `SessionID` | `Print`, `InputFormat`, `OutputFormat`, `Verbose`, `PermissionMode`, `AllowedTools` (+ MCP tool names), `DisallowedTools`. **No `Model`, no `SessionID`, no `SystemPromptFile`.** |
| Session ID | `state.GenerateUUID()` → `memory.WriteLastSessionID` → `--session-id` flag | Not generated. Not written. Not passed. |
| `--resume` | Not used today (fresh session every loop iteration — handoff path is the "resume" mechanism) | Not used. |
| Missed-handoff detection | Yes: `readLastSessionID` → `hasSessionSummary` → `autoSummarize` fallback before launch | No. |
| Post-session consolidation | Yes: check `.sprawl/memory/handoff-signal` → `memory.Consolidate` → `memory.UpdatePersistentKnowledge` → clear signal → restart | No. Process exits and that's it. |
| Restart mechanics | Bash loop around `sprawl _root-session` with exit-code contract (0=restart, 1=retry, 42=shutdown) | One-shot Claude subprocess. TUI has `restartFunc` wired for crash recovery only. |
| `/handoff` skill | Writes summary via `sprawl handoff` → `memory.WriteSessionSummary` + `memory.WriteHandoffSignal` | Same command is available, but nothing consumes the signal. |
| MCP `sprawl-ops` | N/A — tmux weave uses the `sprawl` CLI directly | In-process via `sprawlmcp.New(sup)` → `host.MCPBridge` |
| Process structure | Bash → `sprawl _root-session` → Claude (interactive tty) | `sprawl enter` (Go/Bubble Tea) → Claude (stream-json subprocess) |
| User-facing impact | Full memory, handoff, resume, persistent knowledge | Blank slate every restart |

## 2. Proposed Shared Init Flow

The flow decomposes into four phases. Each phase is either **shared** (identical logic for both modes) or **mode-specific** (differs by implementation detail but has the same name and role).

### Phase A — Pre-launch housekeeping (shared)

1. Resolve `sprawlRoot` and `rootName` ("weave").
2. Read `last-session-id`. If present:
   - If `HasSessionSummary` → run the consolidation pipeline, clear `last-session-id`.
   - Else → auto-summarize (`memory.AutoSummarize`), then consolidate, then clear.
3. Build context blob (`memory.BuildContextBlob`).
4. Build system prompt (`agent.BuildRootPrompt`) — passing `Mode` so the child-rules / merge-retire / communication blocks render correctly.
5. Persist system prompt via `state.WriteSystemPrompt`. **See §3 for filename.**
6. Generate fresh session ID, write to `last-session-id`.

### Phase B — Launch (mode-specific)

Shared inputs: `promptPath`, `sessionID`, `rootTools`, disallowed tools, model.

- **tmux:** `LaunchOpts{SystemPromptFile, Tools, AllowedTools, DisallowedTools, Model: "opus[1m]", Name, SessionID}` → interactive tty.
- **TUI:** `LaunchOpts{SystemPromptFile, AllowedTools, DisallowedTools, Model: "opus[1m]", SessionID, Print: true, InputFormat: "stream-json", OutputFormat: "stream-json", Verbose: true, PermissionMode: "bypassPermissions"}` → subprocess with stdio pipes into `host.Session`.

The only delta is the stream-json flag block. Everything else flows through the same `LaunchOpts`.

### Phase C — Run (mode-specific)

- **tmux:** `deps.runCommand(claudePath, args)` blocks until the interactive Claude process exits.
- **TUI:** `host.Session` negotiates `initialize`, pumps messages via `protocol.Reader`/`Writer`, `tui.Bridge` marshals into Bubble Tea events. Blocks until the user ends the session (via Ctrl-C in TUI, or the subprocess exits).

### Phase D — Post-launch housekeeping (shared)

1. Check for `.sprawl/memory/handoff-signal`.
2. If present → run the consolidation pipeline (`memory.Consolidate` → `memory.UpdatePersistentKnowledge`) → remove handoff-signal → clear `last-session-id`.
3. Decide whether to restart:
   - **tmux:** `return nil` from `runRootSession`, bash loop restarts.
   - **TUI:** call `restartFunc`, which builds a new Bridge/subprocess and swaps it into the Bubble Tea model. The TUI itself stays running.

**Design guideline:** Phases A and D live in a new shared package, `internal/rootinit/`. Phases B and C stay in the mode-specific call sites (they're structurally different — interactive tty vs. stream-json SDK dance). See §4.

## 3. File Layout & Concurrency

Files involved:

- `.sprawl/agents/weave/SYSTEM.md` — system prompt
- `.sprawl/memory/last-session-id` — most-recent session UUID
- `.sprawl/memory/handoff-signal` — empty marker file
- `.sprawl/memory/sessions/{sessionID}.md` — per-session summaries
- `.sprawl/memory/persistent.md`, `.sprawl/memory/timeline.md`

### The concurrent-mode question

The user correctly flagged that running `sprawl enter` and a tmux weave against the same sprawl checkout at the same time creates a race.

**Recommendation: single filename, enforce single-active-weave invariant with a process lock.**

Rationale:

1. Only one weave should be active at a time regardless of mode — it's the root orchestrator. Running two in parallel is already semantically incoherent (both try to spawn agents into the same pool, both consume the same inbox, both write timeline entries).
2. The `/handoff` skill emits a single signal file; if we split per-mode files we'd need to split the whole handoff protocol, which cascades into `cmd/handoff.go`, the consolidation pipeline, etc.
3. Separate files (`SYSTEM-tui.md`) would paper over the race on the prompt file but would not fix the races on `last-session-id`, `handoff-signal`, or the timeline. Fixing the race only on SYSTEM.md is a false sense of safety.

**Proposed mechanism:** a file-lock at `.sprawl/memory/weave.lock` (flock-style, exclusive, non-blocking). Both `sprawl _root-session` and `defaultNewSession` acquire it before Phase A; whichever gets it runs, the other prints a friendly error ("Another weave session is active — reattach with `tmux attach` / `sprawl enter`") and exits.

Implementation note: Go's `syscall.Flock` on Linux/Mac is fine; the lockfile is process-scoped, so it auto-releases if the process dies. Store the PID in the lock file for debugging.

**Rejected alternative: separate filenames per mode (`SYSTEM-tui.md`, `last-session-id-tui`).** Splits state in a way that makes memory non-sharable across modes, doubles the surface area of the consolidation pipeline, and still doesn't prevent two weaves from concurrently spawning children.

**Rejected alternative: CAS via `writeLastSessionID` checking the previous content.** Works for the single file but does not generalize to the whole session lifecycle; the lock is cleaner.

## 4. Refactor Plan

### New package: `internal/rootinit/`

Move the mode-agnostic logic out of `cmd/rootloop.go` into a shared package:

```
internal/rootinit/
    init.go           // Prepare(): runs Phase A, returns PreparedSession
    postrun.go        // FinalizeHandoff(): runs Phase D
    lock.go           // WeaveLock: acquire / release / status
    deps.go           // injectable deps struct (same pattern as rootLoopDeps)
```

Proposed API:

```go
type PreparedSession struct {
    PromptPath string
    SessionID  string
    Model      string   // "opus[1m]"
    RootTools  []string
    Disallowed []string
}

func Prepare(ctx context.Context, deps Deps, mode Mode) (*PreparedSession, error)
func FinalizeHandoff(ctx context.Context, deps Deps) error
```

### `cmd/rootloop.go` becomes thin

`runRootSession` shrinks to: `rootinit.Prepare` → build tmux-flavored `LaunchOpts` → run claude interactively → `rootinit.FinalizeHandoff`. Current `runConsolidationPipeline`, `hasSessionSummary`/auto-summarize block, handoff-signal read all move into `rootinit`.

### `cmd/enter.go` / `defaultNewSession` grows

`defaultNewSession` (and a new `finalizeEnterSession` called by the TUI's `restartFunc` and on shutdown) call `rootinit.Prepare` and `rootinit.FinalizeHandoff`. The Claude subprocess args change to:

- Include `--system-prompt-file <promptPath>` (drop `host.SessionConfig.SystemPrompt` — see below).
- Include `--session-id <uuid>` and `--model opus[1m]`.
- Keep the stream-json flags.

**Important:** `host.SessionConfig.SystemPrompt` is sent in the `initialize` control request (`internal/host/session.go:48`). If we also pass `--system-prompt-file`, we'd be sending the prompt twice. **Proposal:** set `host.SessionConfig.SystemPrompt` to empty string when a prompt-file is on the CLI, or omit the `system_prompt` key in the control request when empty. This needs a tiny edit to `session.go` to conditionally include the key. (Alternative: stop passing SystemPrompt via `SessionConfig` entirely, since the subprocess already has it via the flag. Prefer this — one source of truth.)

### Small surgical edits to existing code

- `internal/host/session.go` — make `system_prompt` in the `initialize` request optional; drop the key when `SessionConfig.SystemPrompt == ""`.
- `internal/claude/launch.go` — no changes needed; `SystemPromptFile`, `SessionID`, `Model`, `Resume` are already there.
- `internal/state/state.go` — no changes; `WriteSystemPrompt` stays as-is (single path).
- `internal/memory/...` — no changes; existing functions are the shared primitives.

## 5. Restart & Resume Strategy for TUI

### Two distinct "restart" needs

1. **Handoff restart** — weave executes `/handoff`, the session ends, consolidation runs, a fresh weave spins up with updated memory. Today tmux does this via the bash loop; TUI has no equivalent.
2. **Crash recovery** — Claude subprocess dies unexpectedly. TUI already has `restartFunc` wired for this (`cmd/enter.go:258-264`); it currently just builds a new Bridge with no init logic.

Both should use the same code path.

### Recommendation: TUI owns its restart loop; do **not** inherit the exit-code contract

The tmux bash-loop exit-code contract (`0=restart, 1=retry, 42=shutdown`) is specific to a single-process lifecycle. In the TUI, the outer process (the `sprawl enter` Go process running Bubble Tea) is **long-lived**, and Claude is a child subprocess that gets rebuilt. That's a different topology.

Proposed TUI restart lifecycle:

1. Claude subprocess exits for any reason.
2. TUI catches the transport EOF (`host.Session` already handles this).
3. TUI runs `rootinit.FinalizeHandoff` (checks handoff-signal, consolidates if present, clears state). This is the "Phase D" call.
4. TUI checks: did the user request shutdown (e.g., pressed `q`/Ctrl-C)? If yes, exit the Bubble Tea program.
5. Otherwise, call `restartFunc` → `rootinit.Prepare` → new Claude subprocess → new `host.Session` → new `tui.Bridge` → swap into the model.

No exit-code contract needed; the TUI decides locally whether to restart based on why the subprocess ended (handoff signal present vs. user quit vs. crash).

### Session resume

Two flavors worth distinguishing:

- **Between handoffs:** new `sessionID` is generated — this is consistent with tmux behavior and preserves the memory/handoff model. Do the same in TUI.
- **Across `sprawl enter` invocations:** a different question. Should re-opening the TUI resume the previous (unhandoffed) session?

**Recommendation:** For initial implementation, **do not** pass `--resume`. Match tmux semantics: every launch is a fresh session; memory is the continuity mechanism via the context blob, not SDK-level resume. Adding `--resume` later is a small increment (set `LaunchOpts.Resume = true` + `SessionID = prevID` when `last-session-id` exists and has no summary yet). This is appropriate for a follow-up issue.

Rationale for deferring: `--resume` semantics across stream-json mode are not stress-tested in this codebase. The missed-handoff detection + auto-summarize already handles the "TUI died mid-session" case — it just summarizes the JSONL transcript post-hoc instead of resuming live. That's sufficient to start.

## 6. Migration Plan

Phased so tmux mode cannot break.

### Phase 1 — Extract `internal/rootinit/` (no behavior change)

- Move `runConsolidationPipeline`, missed-handoff block, session-id generation, prompt-build-and-persist out of `cmd/rootloop.go` into `internal/rootinit`.
- Rewrite `runRootSession` as a thin wrapper. All tmux tests pass unchanged.
- **Check:** `make validate` green; run tmux weave manually; trigger a `/handoff`; confirm consolidation still runs.

### Phase 2 — Add file lock

- Ship `rootinit.WeaveLock`. Both tmux and TUI acquire it before Phase A.
- **Check:** can start tmux weave; can start `sprawl enter`; second one shows friendly error. No deadlock on normal exit or crash (flock releases on fd close).

### Phase 3 — Wire TUI Phase A

- `defaultNewSession` calls `rootinit.Prepare` → adds `--system-prompt-file`, `--session-id`, `--model opus[1m]` to the subprocess args.
- Drop `SystemPrompt` from `host.SessionConfig` (or make it a no-op when empty) and patch `session.go` to omit the key when empty.
- **Check:** TUI weave now has memory context loaded; SYSTEM.md on disk is inspectable; session-id is recorded in `last-session-id`; `make test` green; `/tui-testing` harness passes; tmux mode still works.

### Phase 4 — Wire TUI Phase D (handoff + restart)

- Extend the TUI's session-end path: on transport EOF, run `rootinit.FinalizeHandoff`, then decide restart vs. exit.
- Handoff from inside TUI now triggers consolidation + restart with fresh session ID and re-prepared prompt.
- **Check:** invoke `/handoff` inside TUI weave, confirm consolidation runs, confirm new session starts with updated memory context, confirm persistent.md is updated.

### Phase 5 — (Optional, follow-up issue) Session resume

- Add `--resume` support gated on `last-session-id` existing and having no summary.
- Defer until phases 1–4 are stable.

**Cross-phase check at every step:** run the tmux smoke test (`scripts/smoke-test-memory.sh`) and the TUI E2E harness. Phase 1 is pure refactor; phases 2–4 only add behavior to TUI, so tmux regressions are unlikely but the smoke test catches them.

## 7. Open Questions

1. **`--model opus[1m]` in TUI.** Tmux weave runs opus with 1M context; TUI today runs whatever Claude's default is. Moving to opus[1m] likely 2-5x's per-session cost. Confirm this is intended (probably yes — weave is the root orchestrator — but worth naming).
2. **File lock cross-platform behavior.** `syscall.Flock` is POSIX; Windows support in sprawl is unclear. If Windows is a goal, need `LockFileEx`. Current sprawl is Linux/Mac per the Makefile feel; confirm.
3. **What if the TUI is launched inside a tmux pane that already has a tmux weave?** The lock catches the conflict, but the UX of "you're already running weave elsewhere" could be confusing. Worth an explicit log message pointing at the existing PID / session.
4. **Does dropping `SystemPrompt` from the SDK `initialize` message break any MCP server discovery?** I believe `initialize`'s `sdkMcpServers` key is independent of `system_prompt`, but worth verifying against the Claude Code SDK docs before landing Phase 3.
5. **Timeline integration.** Tmux weave sessions appear in `.sprawl/memory/timeline.md` via consolidation. TUI weave sessions currently don't. After Phase 4 they will — confirm there's no deduping issue when the same `sessionID` is consolidated twice (e.g., due to a retry after crash).
6. **`handoff-signal` lifecycle edge case.** Today tmux clears the signal *after* consolidation. If consolidation crashes, next launch sees the signal and retries — good. But the TUI restart loop happens in-process; if the TUI crashes between handoff and consolidation, is the signal still there for the next `sprawl enter`? Yes (on-disk file), so we're consistent — but confirm by trace.
7. **Should `host.SessionConfig.SystemPrompt` be removed outright?** Only `defaultNewSession` sets it, and the unifier drops it. Removing the field cleans up the surface, but may break unrelated consumers (tests?). Scan before deleting.

## Rejected Alternatives

### Alt A: Duplicate memory logic inside TUI

Copy-paste `runConsolidationPipeline` and the missed-handoff block into `cmd/enter.go`. **Rejected** — the whole point of QUM-252 is to end the divergence. Two copies guarantee drift.

### Alt B: Make the TUI shell out to `sprawl _root-session`

Use `_root-session` as the child process regardless of mode; the TUI captures its stdio via PTY. **Rejected** — this keeps divergence in the *launch* layer (now we need a PTY adapter that parses terminal escape sequences), and destroys the TUI's direct protocol.Reader/Writer path and the in-process MCP server (which needs to speak SDK control messages, not bytes to a tty). The MCP integration is the whole reason TUI mode exists.

### Alt C: Split state per mode (SYSTEM-tui.md, last-session-id-tui, etc.)

**Rejected** — see §3. Doesn't actually solve concurrency because timeline/persistent knowledge are still shared, and it makes memory non-portable between modes (switching between tmux and TUI would lose continuity). The lock is simpler and more correct.

### Alt D: Adopt the bash-loop exit-code contract inside the TUI

Make `sprawl enter` run the Bubble Tea program inside a bash loop that inspects exit codes. **Rejected** — the TUI is the outer UI loop; it should stay alive across Claude restarts. Embedding an outer restart loop doubles process depth and makes the TUI re-initialize on every handoff (losing terminal state, scroll history, etc.).
