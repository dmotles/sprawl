# Forensics: Why is "consolidating timeline" + "updating persistent knowledge" slow?

**Issue:** [QUM-278](https://linear.app/qumulo-dmotles/issue/QUM-278) — investigation of the two slow, opaque steps that run during handoff finalization (and any root-loop restart that triggers the consolidation pipeline).

**Branch:** `dmotles/qum-278-memory-perf`
**Scope:** research only — produces this doc, not code changes.

---

## TL;DR

Both steps block on **serial `claude -p` subprocess invocations** — each is a full, non-streaming LLM call that (a) pays a cold-start cost to spawn the Claude Code CLI, (b) sends a prompt that grows with the size of the user's accumulated session history, and (c) waits for the entire response before returning. There is no model override, no timeout, no parallelism, no "skip if inputs unchanged" short-circuit, and the spinner gives no insight into what's happening. On top of that, the two LLM calls run one after the other on the user's critical path during a handoff — so the user sits in front of a spinner waiting for two round trips to a big model before they can continue.

The root cause is architectural, not a bug. Fixing it cleanly needs both code changes (async / parallelize / cap prompt size / choose a cheaper model) and UX changes (progress, elapsed time, what-is-it-doing output).

---

## Current-state: code paths

### Entry point

`internal/rootinit/postrun.go :: runConsolidationPipeline`

```
startSpinner("consolidating timeline...")
  deps.Consolidate(ctx, sprawlRoot, NewCLIInvoker(), nil, nil)   // LLM call #1
stop spinner

deps.ListRecentSessions(sprawlRoot, 1)  // read latest session body (fs)
deps.ReadTimeline(sprawlRoot)           // read timeline.md (fs)

startSpinner("updating persistent knowledge...")
  deps.UpdatePersistentKnowledge(ctx, sprawlRoot, NewCLIInvoker(), nil, sessionSummary, timelineBullets) // LLM call #2
stop spinner
```

Called from:

* `rootinit.FinalizeHandoff` when `.sprawl/memory/handoff-signal` is present (tmux `cmd/rootloop.go` and TUI `cmd/enter.go` both route here, as does the TUI `restartFunc` after `/handoff`).
* `rootinit.prepare` ("consolidate-then-fresh" and missed-handoff recovery paths in `internal/rootinit/init.go` lines 89, 110, 115).

Both call sites are on the user's **critical path** — the next Claude session can't launch until these return. In tmux mode the agent is restarting; in TUI mode this runs inside the `restartFunc` before the next `runProgram` iteration.

### Step 1: Consolidate (`internal/memory/consolidate.go`)

1. `ListRecentSessions(sprawlRoot, 1<<30)` — reads **every** `.sprawl/memory/sessions/*.md` file, parses YAML frontmatter, sorts. Linear in session count; unbounded.
2. Partition: candidates = `sessions[:len-3]`. No-op if ≤3 total.
3. `ReadTimeline(sprawlRoot)` — reads the existing `timeline.md`.
4. `buildConsolidationPrompt` — concatenates the whole existing timeline plus **every candidate session body verbatim**. No truncation, no chunking, no budget.
5. `invoker.Invoke(ctx, prompt)` — shells out to `claude -p` (see below). **This is where the wall time lives.**
6. Parse response, merge, `CompressTimeline` (recency-weighted grouping into weekly / monthly buckets), `PruneTimeline` (enforce `MaxEntries=200`, `MaxSizeChars=50000`), write.

### Step 2: UpdatePersistentKnowledge (`internal/memory/persistent.go`)

1. `ReadPersistentKnowledge` — reads `persistent.md`.
2. `buildPersistentPrompt` — includes existing persistent knowledge, the latest session body (already read by postrun), and the freshly-written timeline bullets.
3. `invoker.Invoke(ctx, prompt)` — second `claude -p`. **Second chunk of wall time.**
4. Parse, cap to `MaxItems=20`, write.

### The invoker (`internal/memory/oneshot.go`)

```go
// CLIInvoker.Invoke:
args := []string{"-p"}
if cfg.model != "" {            // <-- memory callers never set a model
    args = append(args, "--model", cfg.model)
}
cmd := exec.CommandContext(ctx, binaryPath, args...)
cmd.Stdin = strings.NewReader(prompt)
// ... collect stdout into a buffer, wait for process exit
```

Notes:

* No `--model` flag is ever passed — `claude -p` uses its own default (typically Sonnet; could be anything the user has globally configured). The root agent itself uses `opus[1m]` (see `internal/rootinit/tools.go`), so if the user has globally set Opus as their default they are paying Opus latency/cost for a distillation task.
* No stream consumption — the code waits for the entire response, then `TrimSpace`es the stdout buffer.
* The context passed in has no deadline at the call site (`FinalizeHandoff` receives whatever `ctx` the caller passed, and none of the callers wrap a `context.WithTimeout` around these specific calls).
* Cold-start of `claude -p` is itself non-trivial — it's a full Node.js CLI that authenticates, loads configuration, etc., before the first byte of model output is requested.

---

## Timing breakdown — where does wall-clock time go?

Direct instrumentation was not run as part of this investigation (the worktree's `.sprawl/memory/sessions` is empty, so a realistic prompt can't be assembled locally without seeding data). Based on code inspection the time must decompose as:

| Phase | Per call | Notes |
|---|---|---|
| fs reads (all sessions + timeline) | ~ms–tens of ms | Bounded in practice by disk + parse cost. Linear in session count but still fast. |
| build prompt string | ~ms | `strings.Builder` concat. Free. |
| `exec.CommandContext` spawn + `claude -p` startup | ~1–3 s | Node.js CLI cold start, auth handshake. |
| LLM round-trip | **~10–60 s** | Dominant. Scales with prompt size and model. Non-streaming — we wait for the whole response. |
| parse + compress + prune + write | ~ms | Free. |

So of the two spinners combined, the user is paying **~2× (CLI cold-start + LLM round-trip)** — easily 20–120 seconds end-to-end in the common case, more as session history accumulates.

### Contributing factors that make it worse than it has to be

1. **Serial, not parallel.** Step 1 and Step 2 don't have a true data dependency in spirit — PK wants the post-consolidation timeline, but it is *not* catastrophic if PK reads the pre-consolidation timeline (PK's job is to distill persistent facts, and the timeline it sees as input has less than one handoff's worth of skew). As written they are strictly sequential: step 2 reads the file step 1 just wrote.
2. **Unbounded prompt growth.** `ListRecentSessions(sprawlRoot, 1<<30)` means "all sessions". Every candidate session body is concatenated verbatim. On a long-lived project the prompt grows monotonically and so does the LLM latency. There is no token/char budget on the consolidation input (the *output* timeline is capped by `PruneTimeline` at 50KB / 200 entries, but the input is not).
3. **Default model.** `claude -p` with no `--model` uses whatever default the environment picks. A distillation task (produce bullet points from a structured input) is a good candidate for the cheapest/fastest available model (Haiku / Sonnet), not Opus. The root agent deliberately uses `opus[1m]`; the consolidation agent has no reason to match that, but it inherits the user's global default.
4. **No short-circuit.** Consolidation runs every handoff even when there's only one new candidate session since the last consolidation. There is no "hash of inputs == hash of last inputs, skip" logic. `UpdatePersistentKnowledge` runs every handoff unconditionally (the early-exit in `Consolidate` for ≤3 sessions doesn't propagate to PK).
5. **No timeout / no cancellation hint.** If `claude -p` hangs, the spinner spins forever. Only a SIGINT on the parent will unblock it, and even then the ctx plumbing may leave the spinner orphaned briefly.
6. **Opaque to the user.** The spinner shows `⠋ consolidating timeline... (42s)`. That's it. No indication of what model is being called, how big the prompt is, or how many sessions are being considered. If the user's only signal is "this takes a minute every handoff", they reasonably conclude something is wrong.
7. **Runs on the critical path.** Both calls happen before the next Claude session can start. The user is staring at a terminal waiting. This is the real UX pain — a 30-second background housekeeping task would be fine; a 30-second foreground gate between "I typed /handoff" and "I can continue working" is not.
8. **Silent double-work on `PrepareFresh` paths.** `internal/rootinit/init.go` can call `runConsolidationPipeline` in three separate branches of `prepare`; combined with the post-run `FinalizeHandoff` call, some restart paths may run consolidation more than once per user-visible handoff.

---

## Root-cause hypothesis (with evidence)

**The dominant wall-clock cost is two back-to-back synchronous `claude -p` LLM invocations, run on the user's critical path, with no caching, no parallelism, no prompt budget, no model selection, and no visibility.**

Evidence:

* `internal/memory/oneshot.go` lines 52–90 — plain blocking `cmd.Run()` on a Node CLI subprocess that wraps an LLM call.
* `internal/rootinit/postrun.go` lines 45–78 — the two invocations are explicitly sequenced with `sp.stop()` between them; no goroutine, no `errgroup`.
* `internal/memory/consolidate.go` line 30 — `ListRecentSessions(sprawlRoot, 1<<30)` and lines 92–136 dump all candidate bodies into the prompt.
* `internal/rootinit/tools.go` line 22 — root agent uses `opus[1m]`; no equivalent constant or override is passed by the memory callers, so the consolidator inherits the user's default model.
* `internal/rootinit/spinner.go` — spinner renders only `label (elapsed)`; nothing richer.

This matches the symptom reported in `docs/todo/punchlist.md` items #6 and #7: "takes forever... slow and opaque... need visibility into what this step actually does and why it takes the time it does."

---

## Proposed fix directions

Ordered by expected impact-per-effort. Each is sized as a plausible follow-up issue (final numbering to be assigned when the Linear issues are filed).

### P0 — Get it off the critical path

**Do the consolidation pipeline in the background after the new session launches**, not before. The new session doesn't need `timeline.md` and `persistent.md` to be freshly updated the instant it boots — those files are read at context-blob-build time, and the previous run's versions are "good enough" for the first prompt. Options:

* Fire-and-forget goroutine kicked off by `FinalizeHandoff` that writes a lockfile (`.sprawl/memory/.consolidating`) so the next `FinalizeHandoff` knows to wait if it finds a stale one.
* Separate `sprawl consolidate` subcommand invoked as a detached child process; `FinalizeHandoff` just spawns it and returns.
* Queue the work to run during idle time (next handoff, daily cron, `sprawl doctor`).

Tradeoff: the very next session prompt will be a handoff "behind" on timeline/PK updates. Almost certainly fine — these files are summaries, not authoritative state.

**This is the single biggest win.** Everything else is a multiplier.

### P1 — Make it faster when it does run

1. **Parallelize the two LLM calls.** PK does not meaningfully depend on the just-written timeline; run both `claude -p` invocations concurrently under an `errgroup`. ~40–50% wall-clock reduction when it must run on the critical path.
2. **Pass `WithModel("claude-haiku-...")` (or sonnet)** from both `Consolidate` and `UpdatePersistentKnowledge` call sites. Distillation does not need Opus. Plausibly 2–5× faster per call, and cheaper. Make the model configurable via `.sprawl/config.yaml` for users who disagree.
3. **Cap the consolidation prompt.** Only feed sessions newer than the most recent timeline entry, plus maybe one session of overlap. Today it re-feeds the entire history every time. Budget the prompt to e.g. `MaxSizeChars * 2` using `TruncateWithNote` (which already exists in `budget.go`).
4. **Stream the response** via `claude -p --output-format stream-json` (or equivalent) so the spinner can show partial progress and the process can be killed early if the output is already complete.
5. **Add a timeout** (`context.WithTimeout`, e.g. 120s) around each `Invoke` and fall through to "warning: consolidation skipped (timed out)" on hit. Failure mode today is "hangs the spinner forever".

### P2 — Make it skippable when it shouldn't run

1. **Short-circuit on unchanged inputs.** Hash the concatenation of (timeline mtime + candidate-session IDs) and compare to a `.sprawl/memory/.consolidate-hash` file. Skip both LLM calls if unchanged.
2. **Skip PK entirely when `Consolidate` returned no-op** (≤3 sessions, or inputs unchanged).
3. **Deduplicate in `prepare`.** Audit the three `runConsolidationPipeline` call sites in `init.go` vs the one in `postrun.go` — ensure at most one runs per user-visible handoff.

### P3 — Make it honest about what it's doing

1. **Richer spinner messages.** Include model, session count, prompt size:
   `⠋ consolidating timeline: 17 sessions, 34KB prompt, model=sonnet (12s)`
2. **Log consolidation inputs/outputs** to `.sprawl/memory/.consolidate.log` so a curious user can cat it after the fact.
3. **Add `sprawl memory status`** CLI subcommand showing "last consolidation: X ago, Y entries, Z bytes; last PK update: ..." — even better if it can display "next scheduled".
4. **Surface failures** — today errors are logged as warnings and discarded. A `sprawl doctor` check (or a banner on next `sprawl enter`) can tell the user "heads up, last consolidation failed 3 sessions ago".

---

## Proposed decomposition into follow-up issues

| Issue | Title | Rough scope |
|---|---|---|
| A | Move consolidation pipeline off the handoff critical path | Design + implementation of async / detached execution. Biggest UX impact. |
| B | Parallelize Consolidate + UpdatePersistentKnowledge LLM calls | Small code change in `runConsolidationPipeline`: `errgroup` the two invocations; decide data-dependency story. |
| C | Select a cheaper model for memory distillation | Thread `WithModel` through both call sites; plumb a config knob (`memory_model` in `.sprawl/config.yaml`). |
| D | Cap/budget the consolidation prompt | Stop re-feeding entire session history; feed only sessions newer than last timeline update plus small overlap. Use existing `TruncateWithNote`. |
| E | Add timeout + cancellation to memory LLM invocations | `context.WithTimeout` in postrun; document the configurable default. |
| F | Richer progress output for memory operations | Spinner includes model + session count + prompt size; structured log file; `sprawl memory status`. |
| G | Input-hash short-circuit for consolidation | Skip LLM calls when inputs are unchanged since last run. |
| H | Audit + dedupe consolidation call sites | Walk the three `runConsolidationPipeline` calls in `init.go` + the one in `postrun.go` and guarantee at-most-once per handoff. |

A, B, C, D, E should be filed together as a coherent "memory perf" workstream; F as UX; G, H as cleanups.

---

## Reflection (what surprised me, open questions, what I'd do next)

**Surprises**

* I expected this to be "a slow fs scan" or "a sync git operation". It's neither — it's simply two blocking LLM calls, back-to-back, on the user's critical path. The design reads like it was written to be correct first and fast never, which is a perfectly reasonable v0 posture; it's just that the "never" part is now showing up as user pain.
* The root agent picks `opus[1m]` very deliberately (`tools.go` has a constant, a comment, tests). The consolidator by contrast passes no model at all and inherits the user's default. That asymmetry feels unintentional.
* `ListRecentSessions(sprawlRoot, 1<<30)` gave me a chuckle — "give me all of them" spelled via a 1-gig limit rather than a sentinel. Works, but the prompt it produces also grows without bound.
* The same pipeline runs from four different call sites, not one. Worth auditing.

**Open questions**

* **How long does this actually take in practice?** I did not run a real `sprawl enter` + `/handoff` cycle with a seeded session history, so the "10–60s per call" estimate is inferred from first principles (CLI cold-start + non-streaming LLM round-trip) rather than measured. A two-minute measurement exercise on a populated `.sprawl/memory/` would either confirm or falsify this and tell us which mitigations to prioritize. Recommending this as the very first step of the follow-up workstream.
* **What does `claude -p` use as its default model?** If it's already Sonnet the model-override change is a small win; if it's Opus (e.g. on a user with an Opus-default global config) it's a huge win. Easy to check with `claude -p --help` and `claude config`.
* **Is PK actually using fresh-from-disk timeline?** The postrun code reads timeline *after* Consolidate writes it, so the answer is "yes, post-consolidate". Worth confirming by test that the parallel-both-calls change doesn't regress anything semantic — probably not, but worth being honest about.
* **Does `claude -p` support streaming stdout?** If so, streaming both (a) lets the spinner show real progress and (b) could let us abort early on parse-failure. I didn't verify CLI flag availability.

**If I had more time**

1. Seed `.sprawl/memory/sessions/` with 10–20 fake session summaries and run a timed `sprawl enter` + `/handoff` loop to get ground-truth numbers. That converts this doc's hypotheses into measurements.
2. Write a tiny benchmark that calls `Consolidate` and `UpdatePersistentKnowledge` directly with a test invoker that records prompt sizes — to track prompt-growth regressions over time.
3. Draft a design doc for the "off the critical path" option (P0) — there are genuine tradeoffs between fire-and-forget goroutine vs. detached subprocess vs. deferred-to-next-idle, and I'd want to pick one intentionally rather than whichever I typed first.

---

*Produced 2026-04-21 by trace (researcher agent) on behalf of weave.*
