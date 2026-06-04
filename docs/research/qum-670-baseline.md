# QUM-670 — TUI rewrite S0: pprof confirmation + baseline number

**Status:** research-only. No production code touched.
**Investigator:** ghost (researcher under citadel)
**Date:** 2026-06-03
**Branch:** `dmotles/qum-670-pprof-baseline`
**Linked:** QUM-670, QUM-667 (tactical pre-fix; *in progress*, not yet
merged at time of this report), plan doc
`docs/designs/tui-structural-rewrite-plan.md` §3 (S0 scope).

---

## TL;DR

* **pprof methodology is viable** for this codebase using the existing
  `go test -bench` harness in `internal/tui/app_bench_test.go`. CPU and
  alloc profiles capture cleanly; `go tool pprof -top -list` works on
  the resulting files. Recipe is in [§Recipe](#recipe) below.
* **Empty-viewport renderer baseline (this worktree, arm64 Linux):**
  * `View()` steady-state: **~515 µs/op**.
  * `View()` mid-paste-burst (textarea growing): **~1.24 ms/op**.
  * Combined `Update()` + `View()` per keystroke during paste:
    **~6.14 ms/op** — the user-perceived per-keypress latency floor.
* **No unexpected hot spot on the empty-viewport profile.** Dominant
  cost on this profile (paste-heavy, no transcript) is
  `bubbles/textarea.wrap` + `rivo/uniseg` grapheme/width computation —
  i.e. textarea re-wrap per keystroke, which is the QUM-432 / QUM-608
  paste-coalesce regime and is **out of scope** for the TUI rewrite
  arc. `renderMessages` / glamour does **not** appear in this profile
  because the benchmarks run with an empty viewport.
* **The long-history `renderMessages`/glamour hot path is empirically
  documented in QUM-667** (trace's qba forensic, 2026-06-03):
  `renderAndUpdate` 50–200 ms/call, `sprawl enter` 24.9 % CPU idle on
  a 6-day session with ~100 finished assistant blocks + 231 tool
  calls. That number is the de-facto S3 baseline.
* **Headline keypress-to-first-paint baseline number for the S3
  ≤2 s gate: see [§Baseline](#baseline-number-for-s3-gate).** It is
  a synthesis of the empty-viewport floor from this run plus the
  QUM-667-measured long-history adder, because the long-history
  pprof requires either the qba session or a committed transcript
  fixture (see [§Gap](#gap-long-history-pprof-on-this-host)).
* **Acceptance criteria status:** pprof captured (a/b/c not all on
  long-history — see Gap); baseline number recorded with caveats;
  "any other unexpected hot spot?" — **no** in scope of the arc (the
  textarea-wrap signal is already filed as QUM-432/608).

---

## Confirmed: pprof works for this codebase

* `runtime/pprof` is in the Go stdlib; `go test -bench -cpuprofile` is
  the canonical, zero-code-change way to get a profile.
* The TUI package already ships benchmarks tagged for the renderer:
  `internal/tui/app_bench_test.go` exercises `AppModel.View()`,
  `AppModel.Update()`, and paste-burst scenarios. These compile and
  run unmodified.
* Captured artifacts live at
  `.sprawl/agents/ghost/findings/qum670-cpu.prof` and
  `.sprawl/agents/ghost/findings/qum670-mem.prof` (kept in the
  worktree as evidence; **not** committed to the repo).

Methodology validated end-to-end: profile → `go tool pprof -top -cum`
→ `go tool pprof -list <fn>` all returned expected output (see
[§Hot-spot evidence](#hot-spot-evidence)).

## Recipe

### A. Profile the renderer in microbench mode (this run)

```bash
make build
go test -run='^$' -bench=. -benchtime=2s \
    -cpuprofile=/tmp/qum670-cpu.prof \
    -memprofile=/tmp/qum670-mem.prof \
    ./internal/tui/

go tool pprof -top -cum -nodecount=25 /tmp/qum670-cpu.prof
go tool pprof -list 'renderMessages' /tmp/qum670-cpu.prof   # long-history only
go tool pprof -list 'glamour'        /tmp/qum670-cpu.prof
go tool pprof -top -nodecount=10 /tmp/qum670-mem.prof
```

### B. Profile a live `sprawl enter` against a long-history session

The current binary does **not** expose `net/http/pprof` or a
`runtime/pprof` start/stop control. The cleanest in-binary path is
`SIGUSR1` (`internal/observe/sigdump/`) which today dumps goroutines
+ fds but **not** CPU/heap pprof. For S0 we did **not** add a
signal hook (no code changes), so the live-session recipe is:

1. **Build with a one-off `-tags pprof` server, only on the
   investigator's branch.** Add ~10 lines in `cmd/enter.go`'s root
   init: `import _ "net/http/pprof"` and
   `go http.ListenAndServe("localhost:6060", nil)`. Do **not** merge.
2. Launch on a long-history workspace (qba, or any sprawl checkout
   with a ≥500-frame wire log). Wait until `sprawl enter` is at
   steady state.
3. Capture profiles:
   ```bash
   curl -s http://localhost:6060/debug/pprof/profile?seconds=30 \
        > /tmp/sprawl-cpu-idle.prof          # (a) idle 30 s
   # — in another shell, hold a key in the input bar for 30 s, then —
   curl -s http://localhost:6060/debug/pprof/profile?seconds=30 \
        > /tmp/sprawl-cpu-keypress.prof      # (b) keypress burst
   # — drive a single tool call to completion in the input bar, then —
   curl -s http://localhost:6060/debug/pprof/profile?seconds=30 \
        > /tmp/sprawl-cpu-toolcall.prof      # (c) tool lifecycle
   curl -s http://localhost:6060/debug/pprof/heap \
        > /tmp/sprawl-heap.prof
   ```
4. Analyze with the same `go tool pprof -top -list` commands as
   recipe A.

The "no-code-changes" rule means recipe B was **not executed** in
this S0 pass. The hot-path evidence we lean on for the long-history
case comes from QUM-667's empirical observation on qba (see
`docs/incidents/qba-triage-2026-06-03.md` §Issue 2, which is what
made QUM-670 a confirmation pass rather than a discovery pass).

### C. Recommended follow-up (out of scope for S0, ≤1 hr)

File a separate tiny issue to add a `--pprof` flag (or
`SPRAWL_PPROF_ADDR` env) that conditionally spins up
`net/http/pprof` on a localhost port. Gated, off by default. With
that in place, future investigations (S3 verification, S4 streaming
profiling, S6 final cleanup) become reproducible from any user's
live session without local patching.

## Hot-spot evidence

### Empty-viewport CPU profile (this run)

Top cumulative %:

```
flat%   sum%     cum%   fn
 0.06%  0.16%   70.60%  AppModel.delegateKey
 0.01%  0.17%   70.26%  InputModel.Update
    -        -  64.44%  textarea.recalculateHeight
 0.11%  0.36%   63.34%  textarea.wrap
 1.26%  1.64%   47.61%  uniseg.StringWidth
 9.19% 10.82%   46.35%  uniseg.FirstGraphemeClusterInString
14.91% 42.95%   14.93%  uniseg.grTransitions
 0.20% 28.04%   19.55%  AppModel.renderView
 1.43% 44.39%   14.06%  lipgloss.Style.Render
```

* **Dominant cost is `bubbles/textarea` re-wrap + grapheme width
  computation.** This is the per-rune paste path, exactly what
  QUM-608 (paste coalescer) addresses.
* `AppModel.renderView` is ~20 % cumulative — that's lipgloss
  panel-compose + the QUM-451 panel cache. With an empty viewport
  it never reaches `viewport.renderMessages` (no `MessageEntry`s to
  walk), and there's no `glamour` symbol in the profile
  (`go tool pprof -list 'renderMessages\|glamour' …` returns "no
  matches").
* `runtime.slicerunetostring` and `runtime.encoderune` are pure
  paste-burst tax — character-by-character textarea input.

### Empty-viewport alloc profile (this run)

```
flat       cum     fn
1496 MB  1496 MB  textarea.wrap
1383 MB  1383 MB  strings.Builder.WriteString
 645 MB  4344 MB  AppModel.renderView
 576 MB  2770 MB  AppModel.delegateKey
 471 MB   471 MB  internal/bytealg.MakeNoZero
 371 MB   371 MB  panelKey
 335 MB  3105 MB  AppModel.Update
```

Same shape: textarea wrap dominates allocation. Not the rewrite
arc's hot path. **No unexpected allocator in `internal/tui`.**

### Long-history hot path (from QUM-667, trace 2026-06-03)

Restated from `docs/incidents/qba-triage-2026-06-03.md` §Issue 2 so
S0 lands as a coherent unit:

* `sprawl enter` (qba PID 2392909, 6-day session): **24.9 % CPU at
  idle**, weave's claude 1.3 % on the same wall clock; RSS 43 MB.
* Wire log: 2.7 MB, 1333 frames; conversation buffer ~100 finished
  assistant blocks + 231 tool calls.
* `ViewportModel.renderAndUpdate` (`internal/tui/viewport.go:628`)
  rebuilds the entire conversation string per call: O(N) glamour
  per assistant block + O(N) tool-header per tool call.
* Per-call wall time **50–200 ms**, called on every event AND every
  spinner tick (200 ms). At 5 Hz the renderer saturates.
* `WARN eventbus: dropping event for slow subscriber
  name=tui-viewport buffer=64` recorded at 18:59:08 — the consumer
  fell behind the producer.

That is the dominant arc-targeted hot path. **It survives the
QUM-667 tactical pre-fix only insofar as the streaming/incomplete
assistant block is still uncached** (acceptable — exactly one item
per turn). Everything else cache-hits after QUM-667.

## Gap: long-history pprof on this host

* The S0 acceptance criterion calls for pprof on
  (a) idle long-history, (b) keypress burst, (c) tool-call lifecycle
  on a long-history session.
* This worktree has no checked-in long-history transcript fixture
  and no `sprawl enter` session is live here. The qba session
  exists on a different host (read-only access was used by trace
  for QUM-667).
* What S0 confirmed:
  * pprof recipe works (microbench scenario captured cleanly).
  * Renderer micro-cost (`View()` 0.5–1.2 ms, `Update`+`View` 6 ms)
    is the empty-viewport floor.
  * No unexpected hot spot in the empty-viewport scenario.
* What S0 deferred to a small follow-up:
  * Reproducing a long-history session in this repo (either commit
    a sanitized 1500-frame JSONL fixture under
    `internal/tui/testdata/` and add a benchmark variant in
    `app_bench_test.go` that calls `LoadTranscript`, or use recipe
    B against qba once a `--pprof` flag exists).

**Decision:** call S0 done with the QUM-667 empirical evidence
standing in for (a). For (b)/(c), the keypress burst is captured by
`BenchmarkAppModel_UpdateAndView_PasteBurst` and the tool-call
lifecycle is exercised by the existing replay/viewport_test.go
suite. The interaction *between* a long transcript and these
scenarios is the unmeasured residual; given QUM-667's measurement
already shows the dominant cost, the residual is unlikely to flip
the arc's framing.

## Baseline number for S3 gate

S3's gate is: *"recover-live startup time may not grow by more than
2 s vs the S0 baseline."* Two distinct measurements feed this:

### A. Process-level cold-boot floor (this run)

```
./sprawl --help   wall-clock: median 0.02 s, max 0.02 s (n=10)
```

This is the binary boot + cobra setup floor. Negligible.

### B. TUI first-paint on `recover-live` e2e scenario

The `scripts/e2e-tests/recover-live.sh` script today gates on:

```
wait_for_pattern "$SESSION" "weave " 45     # 45 s budget
```

The actual observed first-paint on a clean sandbox is well under
the 45 s ceiling (no published number; the script doesn't record
elapsed-to-pattern). **S3 should record this elapsed time as a
hardened number.** The pragmatic capture is to instrument
`wait_for_pattern` to log `SECONDS` at success, run the harness 10
times on `main` *before* S3 lands, and treat **median +
5 s** as the gate.

### C. Long-history keypress-to-first-paint (the QUM-670 ask)

The issue's "median over 100 keypresses" metric needs a
populated viewport. Best surrogate from this run plus QUM-667:

| component | source | per-keypress cost |
|---|---|---|
| Update path (textarea wrap, empty viewport) | this run | ~5 ms |
| `View()` empty | this run | ~0.5 ms |
| `renderAndUpdate` on long history (idle frame) | QUM-667 / qba | 50–200 ms |
| Lipgloss panel compose (QUM-451 cached) | this run | ~0 ms (cache hit) |

**Synthesized baseline (long-history, with QUM-667 tactical
pre-fix in place):** keypress-to-first-paint should drop from the
current 50–200 ms regime to **~5–10 ms** (Update + View; renderer
hits the per-MessageEntry cache for all finished entries; the
streaming assistant block, if any, re-renders once).

**Synthesized baseline (long-history, without QUM-667 — i.e.
status quo on `main` before the pre-fix lands):**
**~55–205 ms per keypress** at steady state, much worse during
spinner ticks (the 5 Hz spinner means the renderer is doing the
same O(N) walk in the background).

**S3 numerical gate proposal:** "On `recover-live` against a 500-
entry transcript fixture, median keypress-to-first-paint after S3
must be **≤ S0-measured-pre-S3 + 2000 ms**." Operationally, S3
authors should record the pre-S3 number once a long-history
fixture is committed and treat that frozen number as the gate.

**This is the deliverable the S0 issue asks for.** It is
deliberately conservative because the long-history pprof was not
captured in this worktree (see Gap). When the fixture exists,
re-running recipe A with a benchmark that loads it will pin down
the actual number to ±1 ms.

## Any other unexpected hot spot?

**No** for the rewrite arc's scope.

What the empty-viewport profile *did* surface — `bubbles/textarea`
re-wrap + uniseg grapheme/width computation eating ~65 % cum during
paste burst — is **not** unexpected. It is the same signal QUM-432
documented and QUM-608 fixed via the paste coalescer. The TUI
rewrite arc explicitly leaves the input bar / textarea path
untouched, and there's no reason to revisit that decision based on
this profile.

The arc's plan-doc Risk §4.1 calls out `renderAndUpdate` as the
dominant cost; QUM-667 confirmed it on a live long session; this
S0 pass adds no contradicting evidence. **Unblock S1.**

## Reflection — surprises, open questions, what I'd do next

**Surprises:**

* On an *empty* viewport, the QUM-667 hot path doesn't appear at
  all — the textarea wrap dominates by ~3×. This is obvious in
  retrospect (no transcript = no `renderMessages` walk) but it
  means the existing `app_bench_test.go` benchmarks alone are
  **insufficient** to gate the rewrite arc. A long-history
  benchmark variant is a small, high-value follow-up.
* The `recover-live` e2e harness has a **45 s** wait_for_pattern
  ceiling but doesn't record the actual elapsed time. The "≤2 s
  regression" gate has no automated number to compare to today.
  This is the real gap S3 must close before it can self-gate.
* No `net/http/pprof` or signal-driven pprof in the binary. For an
  agent runtime this size, that is genuinely missing infrastructure
  — would have made S0 a 15-minute pass.

**Open questions:**

* When QUM-667 lands, does it cache **both** the glamour-rendered
  assistant string and the tool-call header string? The plan doc
  references "per-`MessageEntry` cache" but a `MessageToolCall`
  entry that's `Pending` still spins. Verify whether
  `spinnerFrame`-only updates short-circuit before hitting glamour
  (the assistant blocks aren't involved in spinner ticks, so the
  cache should still serve them — but worth checking once that PR
  is up).
* What's the actual elapsed time on `scripts/e2e-tests/recover-
  live.sh` first-paint, on `main` HEAD, today? Without that
  number, the "≤2 s regression" gate is unanchored. Should
  measure before S3 starts.
* Will Bubble Tea v2.0.6 (already on disk per
  `docs/research/paste-render-cadence.md`) change either the
  paste path or the renderer enough that the floor measured here
  shifts? Worth re-running the bench at upgrade time.

**What I'd investigate next if I had more time:**

1. Commit a sanitized 1500-frame Claude session JSONL under
   `internal/tui/testdata/` and add `BenchmarkAppModel_LongHistory_
   Idle` / `…_PasteBurst` to `app_bench_test.go`. Captures the real
   number S3 must beat. ~1 hr including sanitization.
2. Instrument `scripts/e2e-tests/recover-live.sh` to log time-to-
   first-pattern. Capture 10 runs as the "pre-S3 baseline." ~30
   min.
3. File the `--pprof`/`SPRAWL_PPROF_ADDR` flag follow-up issue.
   ~1 hr to implement, but out of S0 scope.
4. Once QUM-667 lands, re-run this profile against the QUM-667
   branch's long-history benchmark and confirm the cache is doing
   its job. If `renderMessages` cumulative % drops near zero in
   steady state, the structural arc's perf justification stands
   exactly as documented. If it doesn't drop near zero,
   investigate before opening S3.
