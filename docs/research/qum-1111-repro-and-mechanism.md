# QUM-1111 — REPRODUCED live, with mechanism

**Author:** ghost (researcher)
**Date:** 2026-08-06
**Verdict:** **(a) REPRODUCED.**

**Where the artifacts live.** This branch (`dmotles/qum-1111-findings-doc`) carries **only** this
document and the live harness `scripts/research/qum1111-repro.sh`. The harness runs standalone
against an ordinary `make build` binary: both of its assertions read the tmux pane, none reads the
trace, so with the instrumentation absent it still produces a verdict (its trace grep is a
diagnostic that simply prints nothing).

Two artifacts referenced below are deliberately **not** on this branch and remain on
`dmotles/qum-1111-repro`, unmerged:

| artifact | why it is not here |
|---|---|
| `internal/qum1111trace/` + its call sites in `eventbus.go`, `unified.go`, `app.go`, `chatlist.go`, `pendingzone.go`, `tuiadapter.go` | temporary instrumentation; must never land |
| `internal/tui/qum1111_order_demo_test.go` | **asserts the current buggy behaviour** — see §4b |

---

## 0. One-paragraph answer

A prompt submitted mid-turn and flushed with `Ctrl+G` stays permanently dim because the TUI's
`UserMessageConsumedMsg` reducer can run **before** the `UserMessageSentMsg` reducer that creates
the bubble it is supposed to settle. `SendAllNow` publishes those two events in the correct order
on one goroutine and the `EventBus` stamps and enqueues them in that order — but the
`TUIAdapter` has **more than one concurrent `WaitForEvent` goroutine** reading the single
subscriber channel, so the order in which their translated messages reach the bubbletea update
loop is not the bus `Seq` order. When it inverts, `ZoneSettle(B)` is a no-op against a zone that
does not yet contain `B`, `ZoneAddUser(B)` then creates the entry, and nothing will ever settle
it: the CLI's later genuine `isReplay` echo for `B` is swallowed by QUM-1068's idempotency gate
(`markConsumed … transitioned=false`), so no second `EventUserMessageConsumed` is ever published.
The stuck entry is keyed to **B**, the replacement uuid.

**The defect is in the TUI adapter/pump, not in `internal/runtime/unified.go`.**

---

## 1. What was reproduced, and how it was judged

Harness: `scripts/research/qum1111-repro.sh` (research-only, not a matrix row).

It reproduces the *reported shape*, not the simple case:

* a long **tool-bound** turn (twelve separate `Bash` tool calls), not pure token streaming;
* an **earlier** mid-turn message (`SETTLEA…`) that **settles normally inside the same turn** —
  asserted, not assumed, so the turn is demonstrably **not** "acked-nothing" and QUM-1000's
  `settleNeverAcked` sweep is out of scope by design;
* then a second mid-turn message (`STUCKB…`) and **`Ctrl+G` before the turn ends**.

### Two independent assertions on the stuck state

**A1 — SGR.** The bubble's `›` glyph line carries faint `\x1b[2m` instead of bold `\x1b[1m`.

**A2 — structure, and it survives an SGR strip.** `buildRender` appends the pending zone *after*
the committed items, so a bubble still held in the zone renders **below the model's own answer to
it**, and a settled one renders above. The model is asked to answer with a unique token, and the
assertion compares **line order**. Both texts are present in the working case too — this is
deliberately not a containment assertion.

> **Recorded gap in the pending-dim contract.** For a **user bubble** the faint/bold SGR *is the
> only* styling delta. QUM-925 F3's `┊` vs `│` gutter — the load-bearing second differentiator
> that exists precisely because faint is SGR 2 and a terminal may ignore it — lives on
> `SystemNotificationItem` only (`renderUserPromptBlock`, `internal/tui/render_helpers.go:110`,
> changes *only* the style). So a user bubble has **no SGR-strip-surviving styling
> differentiator**. A2 above is a *positional* substitute I had to invent for this investigation;
> it is not something the product asserts. Worth a follow-up on its own.

### Negative control (the instrument was watched failing)

`self_test()` drives both evaluators with synthetic panes and demands each report the **broken**
verdict for a broken pane and the working verdict for a working one, before any sandbox is
created. It has been observed both passing and failing: an early sourcing bug produced

```
  SELFTEST FAIL: attr_of faint ->
  SELFTEST FAIL: attr_of bold ->
  SELFTEST FAIL: attr_of none ->
  FAIL: self-test: evaluators did not distinguish pending from settled — harness is not measuring anything
```

which is exactly the loud failure the control exists to produce. The run also carries an
assertion-count floor, so a run that asserts nothing exits non-zero rather than reading as a clean
non-repro.

**What the assertion would have to see to be measuring the actual defect** (rule 1 of the brief):
A1 alone is *not* sufficient — a bubble is legitimately faint while genuinely queued, so a red A1
during an in-flight turn proves nothing on its own. The defect is "faint **after** the model has
demonstrably answered it," which is why A2 (bubble below its own answer token) is the load-bearing
assertion and why the answer token is required to be on screen for A2 to be evaluated at all. Both
must be read together; a run where A2 reports `na` is inconclusive, not green, and the harness
says so.

### Rate

Intermittent, not deterministic — consistent with a live report of a timing-dependent shape.
Per-iteration outcomes are in §6. Do not quote a single rate as a property of the bug: the window
depends on scheduling, on how many prompts have been typed in the session (see §3), and plausibly
on instrumentation (see §5).

---

## 2. The three claims tower asked to be kept separate

For the replacement uuid **B**, in the red iteration:

| claim | answer | evidence |
|---|---|---|
| `EventUserMessageConsumed` **published**? | **YES** | `rt.markConsumed uuid=B transitioned=true present=true`, Seq 17 stamped |
| **delivered** to the `tui-viewport` subscriber? | **YES** | `bus.deliver sub=tui-viewport type=8 seq=17 uuid=B chlen=0/64` |
| **reducer ran**? | **YES** | `tui.reducer UserMessageConsumedMsg uuid=B` |

All three happened. **The interesting failure is not in the gaps between them — it is a fourth
thing: ORDER.** The reducer ran, and ran *too early*.

`trySendWithDeadline` is **not** implicated: there are **zero** `bus.DROP` lines across every run.
The subscriber channel never exceeded `0/64` on these events.

---

## 3. Mechanism

### 3.1 The trace, verbatim (red iteration, run 1)

`bus.deliver` is logged inside `EventBus.fanout`, i.e. after `Seq` stamping and **inside
`publishMu`**, once per subscriber. `adapter.recv` is logged in `TUIAdapter.WaitForEvent`
immediately after `ev, ok := <-a.events`.

```
21:00:52.589950 rt.cancelPendingUser uuid=A=4fd3b7ff-… cancelled=true err=<nil>
21:00:52.589977 bus.deliver sub=tui-viewport type=9  seq=15 uuid=A        ← Cancelled(A)
21:00:52.590028 rt.SendAllNow replacement uuid=B=00e57dd1-…
21:00:52.590038 bus.deliver sub=tui-viewport type=10 seq=16 uuid=B        ← Sent(B)      enqueued 1st
21:00:52.590040 rt.markConsumed uuid=B transitioned=true present=true
21:00:52.590049 bus.deliver sub=tui-viewport type=8  seq=17 uuid=B        ← Consumed(B)  enqueued 2nd
21:00:52.590051 adapter.recv type=9  seq=15 uuid=A -> tui.UserMessageCancelledMsg
21:00:52.590062 adapter.recv type=8  seq=17 uuid=B -> tui.UserMessageConsumedMsg   ← returned 1st
21:00:52.590066 adapter.recv type=10 seq=16 uuid=B -> tui.UserMessageSentMsg       ← returned 2nd
21:00:52.590573 tui.reducer UserMessageCancelledMsg uuid=A
21:00:52.590583 chatlist.ZoneDrop   uuid=A hit=true
21:00:52.592298 tui.reducer UserMessageConsumedMsg uuid=B
21:00:52.592311 chatlist.ZoneSettle uuid=B hit=FALSE remaining=[]         ← settles nothing
21:00:52.592794 tui.reducer UserMessageSentMsg uuid=B …                   ← creates the bubble
21:00:59.406646 rt.markConsumed uuid=B transitioned=false present=true    ← real isReplay echo, swallowed
```

Read the last line carefully. The CLI **does** replay-echo the `now`-write (QUM-1068 measured
this), so a second settle signal genuinely arrives 6.8 s later — and QUM-1068's idempotency gate,
which exists for good reasons, correctly refuses it because the entry already left `statePending`.
The gate is not the bug, but it is what makes the bug **permanent** rather than self-healing.

### 3.2 Publish order is fine. Delivery order is not.

* `Seq` stamping: correct (16 then 17), under `publishMu`.
* Enqueue into the single `tui-viewport` channel: correct order, same critical section; channels
  are FIFO.
* Adapter return: **inverted**.

> **A documented invariant in QUM-1111's own description is wrong as written.** It says
> `SendAllNow` publishes `EventUserMessageSent` before `ConfirmDeliveredWithoutReplay`
> "*deliberately … on the same goroutine so the bus seq-orders them*". The bus **does** seq-order
> them. The unstated assumption is that **bus `Seq` order survives to the reducer**, and it does
> not. Same-goroutine publish ordering buys ordering *into the channel* and nothing past it.
> Anything else in the tree that reasons "publish A before B so the TUI sees A before B" has the
> same hole. `internal/runtime/unified.go:1453-1462` carries that reasoning in a comment.

### 3.3 Why the adapter has more than one reader

`WaitForEvent` is **one-shot**: exactly one reducer must re-arm it per delivered event, so the
steady state is one live pump goroutine.

`TUIAdapter.SendMessage` (`internal/tuiruntime/tuiadapter.go:234`) returns
`tui.UserMessageSentMsg{…}` **directly from the cmd** — it is *not* pump-delivered.
`writeMessage` deliberately does not self-publish for `kindUser`
(`internal/runtime/unified.go:1024-1026`), precisely so the TUI and `SendAllNow` publish for their
own writes. But the `UserMessageSentMsg` reducer (`internal/tui/app.go:1204`) re-arms
`WaitForEvent()` **unconditionally**, because on the `SendAllNow` / `kindSystem` paths the same
message *is* pump-delivered and must re-arm.

So a cmd-delivered `Sent` consumes **no** pump event but adds **one** pump arm:
**+1 live `WaitForEvent` goroutine per typed prompt, permanently, for the life of the session.**

This is a **counting argument over the code**, not a timing observation — it does not depend on
instrumentation. Measured, and monotone with typed prompts (run 2):

```
21:14:07.636 adapter.wait.enter inflight=1     ← session start; correct steady state
21:14:12.952 tui.reducer UserMessageSentMsg  "Run this exact bash command TWELVE times…"
21:14:12.952 adapter.wait.enter inflight=2
21:14:16.765 tui.reducer UserMessageSentMsg  "SETTLEA001deb5b: …"
21:14:16.766 adapter.wait.enter inflight=3
21:14:20.971 tui.reducer UserMessageSentMsg  "STUCKB001deb5b: …"
21:14:20.971 adapter.wait.enter inflight=4
```

With N readers on one channel, Go hands each waiter a distinct value in FIFO order, but the
waiters then race to return their `tea.Msg` to bubbletea's queue. That race is the inversion.

**The inversion has two distinct spellings, and only the second one is universal.** Run 1 inverted
at the *channel receive* (`adapter.recv seq=17` logged before `seq=16`). Run 3 inverted at the
*cmd return*: the adapter received them in order and the goroutines then returned inverted —

```
21:22:05.348942 adapter.recv id=34 type=10 seq=50 uuid=B inflight=6 -> tui.UserMessageSentMsg
21:22:05.348965 adapter.recv id=35 type=8  seq=51 uuid=B inflight=6 -> tui.UserMessageConsumedMsg
21:22:05.348972 adapter.wait.exit id=35 inflight=4    ← Consumed's goroutine returns FIRST
21:22:05.348975 adapter.wait.exit id=34 inflight=5    ← Sent's goroutine returns SECOND
21:22:05.359625 tui.reducer  UserMessageConsumedMsg uuid=B
21:22:05.359654 chatlist.ZoneSettle  uuid=B hit=false remaining=[]
21:22:05.360267 tui.reducer  UserMessageSentMsg     uuid=B
21:22:05.360287 chatlist.ZoneAddUser uuid=B zone=[B]   ← the surviving entry, keyed to B
```

Note the distinct ids **34** and **35**: two different goroutines, which is the whole point — a
single-goroutine pump cannot reorder anything.

Consequence for anyone re-deriving this: **do not measure the inversion rate by scanning
`adapter.recv` for descending `Seq`.** That detector sees only the receive-order spelling and
silently under-counts the return-order one. The authoritative signal is **reducer order**, or more
directly `ZoneSettle … hit=false` immediately followed by `ZoneAddUser` for the same uuid.

A second, smaller instance of the same class is already documented as deliberate:
`internal/tui/app.go:1747-1748` primes an extra `WaitForEvent` on session restart *in addition to*
the one `SessionInitializedMsg` will issue ("priming here is cheap"). Same +1 shape.

### 3.4 Why nothing noticed

The QUM-669 gap detector cannot see this. It fires only on a **forward** `Seq` jump; a backward or
duplicate `Seq` is deliberately a no-op (`internal/tuiruntime/tuiadapter.go:192-213`, the QUM-669
hardening). In the traced inversion the goroutine holding seq 16 won the `a.mu` section and
advanced `lastSeq` to 16 first, then the seq-17 goroutine saw a contiguous jump — so **no**
`EventDropDetectedMsg`, no resync, no banner. The inversion is completely silent.

The mirror-image interleaving is worth flagging for whoever owns QUM-669: if the seq-17 goroutine
reaches the `a.mu` section first while `lastSeq` is 15, the detector fires a **spurious**
`EventDropDetectedMsg` and kicks a resync for a drop that never happened. That is a plausible,
previously unexplained source of resyncs.

---

## 4. Which uuid the stuck entry is keyed to, and how that was determined

**B — the `SendAllNow` replacement uuid.** Determined from three independent trace facts in the
red iteration, not inferred:

1. `chatlist.ZoneDrop uuid=A hit=true` — A was removed from the zone. A is gone.
2. `chatlist.ZoneSettle uuid=B hit=false remaining=[]` — the settle for B found an **empty** zone.
3. `tui.reducer UserMessageSentMsg uuid=B` runs **after** (2) and calls `ZoneAddUser(B)`, which is
   the entry that survives.

This also refutes the closed hypothesis on its own terms: the entry is **not** keyed to the
cancelled uuid A, and the cancel **did** reach the reducer and **did** drop A. It is also not a
`kindSystem` notification — the surviving entry is a `pendingUser` created by `ZoneAddUser` with
the user's own prompt text.

This was the mechanism's **prediction before it was checked**, which is the point: had the stuck
entry turned out to be A, the ordering mechanism would be wrong regardless of how good the
ordering evidence looked.

---

## 4b. Deterministic confirmation that the ORDER is the cause

Everything above is timing evidence, and timing evidence cannot by itself separate "the inversion
causes the symptom" from "the inversion co-occurs with it". So the symptom was isolated from the
race entirely, at the reducer level, with no instrumentation involved:

`internal/tui/qum1111_order_demo_test.go` — **left on `dmotles/qum-1111-repro`, deliberately not
merged here; see the deletion hazard and the rebuild recipe at the end of this section** — delivers
the three messages in the order the live trace recorded — `Cancelled(a)`, `Consumed(b)`, `Sent(b)`
— and asserts the outcome:

```
--- PASS: TestUserMessage_SendAllNowCoalesce_SingleBubble           (existing product test, CORRECT order)
--- PASS: TestQUM1111_ConsumedBeforeSent_LeavesBubblePermanentlyPending  (INVERTED order)
```

The existing product test at `app_pendingzone_test.go:282` delivers the **same three messages in
the correct order** and passes. The only difference between the two is delivery order, which is
exactly the claim. The demo asserts all three observable consequences: the zone still holds one
entry, that entry is keyed to **`b`** (not the cancelled `a`), and zero user bubbles reached the
committed transcript.

**Red-first control, watched.** Swapping the demo's two lines back to the correct order makes it
fail, with the message it is supposed to fail with:

```
--- FAIL: TestQUM1111_ConsumedBeforeSent_LeavesBubblePermanentlyPending
    qum1111_order_demo_test.go:50: zone len = 0, want 1 — expected the replacement bubble to be
    STRANDED in the pending zone (this test documents the defect, not the fix)
```

So the demo is measuring delivery order and nothing else.

The demo also closes the "is it permanent?" question mechanically: a *second*
`UserMessageConsumedMsg` for `b` **does** settle it. That is precisely the signal production never
receives, because `markConsumed`'s QUM-1068 idempotency gate refuses to publish it
(`transitioned=false` in the live trace). The gate is correct and is not the bug — it is what
converts a one-frame ordering glitch into a permanent one.

### Why the demo is not on this branch, and how to rebuild it as a regression test

The demo asserts the **defect**, not the fix — its own failure message says so
(`this test documents the defect, not the fix`). On an integration branch that inverts the meaning
of a green suite: the test passing would mean *the bug is still present*. And a test the fix must
**delete** is worse than no test, because whoever deletes it also deletes the only record of what
it was checking.

Whether it survives the fix depends on where the fix lands, which is why this was not mine to
decide:

* **Fix at the adapter/pump layer** (stop the surplus `WaitForEvent` goroutine, so bus `Seq` order
  survives to the reducer — §3.3): the demo **still passes unchanged**, because it delivers
  messages directly to the reducer and never goes through the adapter. It would then be asserting
  behaviour nobody can reach — vacuous, but green.
* **Fix at the reducer layer** (make `ZoneSettle` tolerate arriving before its `ZoneAddUser` —
  belt-and-braces, and defensible, since the pump fix cannot easily be proven exhaustive): the
  demo **fails and must be deleted**.

Either way it is the wrong artifact to merge. **Rebuild it as a regression test instead** — it is
an assertion-flip, not a rewrite. Keep the file's setup and its three `deliver` calls in the
inverted order verbatim, and invert what they demand:

| assertion | demo (defect) | regression test (fix) |
|---|---|---|
| `cl.zone.len()` | `1` — stranded | `0` — settled |
| `cl.zone.uuids()` | `["b"]` | empty |
| `countUserItems(cl)` | `0` | `1` — reached the committed transcript |

Drop the trailing "a second consume settles it" block: post-fix the entry is already settled, so
that assertion no longer distinguishes anything. Keep the pairing with the existing product test
`TestUserMessage_SendAllNowCoalesce_SingleBubble` (`app_pendingzone_test.go:282`) — same three
messages in the correct order — because the pair is what pins *order-independence* rather than
merely "this one ordering works". And watch it fail first: on the pre-fix tree the flipped
assertions must go red, which is the same red this doc records above with the sign reversed.

Note that a reducer-level test passing is **not** evidence the pump fix works; it exercises the
reducer with an ordering the fix is supposed to prevent. Coverage of the delivery path itself is
the live harness plus the row obligations, not this test.

---

## 5. Controls against "the instrumentation caused it"

The trace call sites sit on exactly the paths under test and do file I/O under a mutex, so they
could in principle widen the window.

1. **Structural.** The +1-arm-per-typed-prompt result in §3.3 is a counting argument over
   unmodified control flow. It holds with instrumentation removed.
2. **Per-invocation ids.** Each `WaitForEvent` invocation is tagged with a unique id logged on both
   `wait.enter` and `recv`. A single-goroutine pump cannot reorder anything, so **different** ids
   on the two inverted `recv` lines are what distinguishes "concurrency caused this inversion"
   from "concurrency merely exists". See §6.
3. **Untraced control run.** `QUM1111_NO_TRACE=1` leaves the trace package inert (no file → no I/O,
   no mutex on the publish or pump paths). The A1/A2 pane assertions do not depend on the trace,
   so the run still produces a verdict. See §6.

**Honest scope of these controls:** they can establish that the race is real and reachable without
instrumentation. They cannot establish that the observed *rate* is the production rate. Treat the
rate as untrustworthy either way.

---

## 6. Run log

Raw artifacts per run in the `run*/` subdirectories: `trace.log`, `pane-ansi.txt`,
`pane-plain.txt`, `driver.log`. All runs: 4-core host, 200-column tmux pane, real authenticated
`claude`, sandbox `SPRAWL_ROOT` under `/tmp`.

| run | tracing | rows | iters | evaluable | reproduced | notes |
|---|---|---|---|---|---|---|
| `run1` | on | 50 | 4 | 2 | **1** (iter 1, A1+A2) | iters 3–4 timing misses; first capture of the `adapter.recv` seq inversion |
| `run2` | on (+`inflight`) | 50 | 3 | 1 | **1** (iter 1, A1+A2) | source of the `inflight=1→2→3→4` measurement |
| `run3` | on (+`inflight`, +ids) | 50 | 2 | 2 | **1** (iter 2, A1+A2) | iter 1 green; ids 34/35 prove two distinct goroutines; return-order spelling |
| `run4-control-inconclusive` | **off** | 50 | 3 | **0** | — | **INCONCLUSIVE, not a non-repro** — see below |
| `run5-control-130rows` | **off** | 130 | 3 | 3 | 0 | `11 passed, 0 failed` |

### `run4` was no measurement at all, and is reported as such

All three iterations reported `attr=none` — the sentinel bubble had scrolled out of the TUI
viewport at 50 rows, so neither evaluator ran. The driver's original wording for that case was
"not reproduced in this run", which is the exact non-asserting-fallback shape CLAUDE.md warns
about: a clean-looking verdict backed by zero observations. The harness now counts evaluable
iterations and prints **INCONCLUSIVE** with a non-zero exit when the count is zero. `run5` is the
retry with a taller pane.

### What the control does and does not establish

`run5` is a real measurement — 3/3 evaluable, none reproduced — and it is nonetheless **weak
evidence**, for three reasons, all of which are properties of the experiment and not of the
result:

1. **Two variables changed at once.** Tracing went off *and* the pane went 50 → 130 rows. This is a
   timing-sensitive defect and terminal geometry changes what the TUI renders and when. A
   non-repro cannot be attributed to the tracing change alone.
2. **Three samples cannot exclude a rate this low.** Across the traced runs the defect appeared in
   roughly 3 of 7 evaluable iterations; three clean iterations is an unremarkable draw from that
   distribution.
3. **Absence under changed conditions is not evidence of instrumentation-as-cause.** It does not
   retroactively weaken the reds already observed at 50 rows.

**Conclusion, stated at the strength it actually has:** the mechanism is confirmed by (1) the
counting argument over the source, (2) the id-tagged trace showing two distinct goroutines, and
(3) the deterministic reducer-level demo — none of which depend on the timing of the live runs.
**Instrumentation-as-cause is NOT fully excluded**, so the inversion *rate* is untrustworthy. Its
*possibility* does not depend on instrumentation.

Two control attempts were run and then stopped deliberately. A control retried until it agrees
with the hypothesis has stopped being a control.

---

## 6b. Reflections

**Surprising.** Every individual leg of this path is correct, which is exactly why two careful
source audits missed it. The event is published, it is delivered, the reducer runs, and the
`ZoneSettle` / `ZoneAddUser` / `ZoneDrop` primitives all do the right thing with the right uuid.
Nothing is dropped, nothing is mis-attributed, no guard is wrong. The only defective thing is the
*sequence*, and sequence is not visible in any single function — you cannot find it by reading one
more file more carefully. That is a lesson about the audit method, not about anyone's care.

**Also surprising, and it nearly bit me:** my first inversion detector scanned `adapter.recv` for
descending `Seq` and would have reported a rate roughly half the truth, because it can only see
the receive-order spelling of the inversion and is structurally blind to the return-order spelling
(run 3's red iteration received in order and *returned* inverted). I only noticed because the
zone trace disagreed with the detector. A detector that agrees with the hypothesis is not
evidence; the disagreement was.

**And:** the pump-arm leak is unbounded and grows with ordinary use — `inflight` reached 7 within a
~90-second session. I expected the extra arm; I did not expect it to be per-prompt and permanent.

**Open questions I did not answer.**
1. What *else* does the multiplied pump break? Out-of-order delivery is not specific to these two
   events. Assistant chunk ordering, tool-call/result pairing, and the terminal trio all flow
   through the same channel. I found no evidence of harm there and I did not look hard.
2. Does the QUM-669 gap detector fire **spuriously** in production because of this? §3.4 argues
   the mirror interleaving must produce a false `EventDropDetectedMsg`. I never observed one — the
   interleaving I caught went the other way each time — so this remains a prediction, not a
   finding.
3. `ChildStreamAdapter` (`internal/tui/child_stream.go:97`) is the same one-shot shape over a
   second subscription. I did not audit its re-arm sites at all.
4. Production rate. Everything here is a 4-core sandbox with a synthetic tool-bound turn.

**What I would do next with more time.** Audit *every* reducer that calls `WaitForEvent()` against
whether its msg is genuinely pump-delivered — that is a small, closed, mechanically checkable list,
and it is the honest scope of the defect rather than the one instance I chased. Then instrument
`inflight` over a long real session to see whether it grows without bound or self-limits.

---

## 7. Explicitly NOT done

* **No fix is proposed or implemented.** Researcher scope. The instrumentation (on
  `dmotles/qum-1111-repro`, not here) is a separate package (`internal/qum1111trace`) plus one-line call sites, every one tagged
  `QUM-1111 RESEARCH INSTRUMENTATION (temporary; do not merge)`, so it is trivially separable and
  will not collide with QUM-1112 work in `unified.go` / `app.go`.
* `internal/merge/` and `internal/agentops/` were not touched.
* Nothing was pushed to origin.

## 8. Rebuilding the instrumentation from scratch

The instrumentation is not on this branch. If someone needs it again and does not want to check out
`dmotles/qum-1111-repro`, it is five call sites plus a ~50-line package:

| where | what to log |
|---|---|
| `EventBus.fanout`, both arms of `trySendWithDeadline` | subscriber name, `ev.Type`, `ev.Seq`, `ev.UUID`, `len(ch)/cap(ch)`, and whether it was a **drop** |
| `TUIAdapter.WaitForEvent`, entry + deferred exit | a unique per-invocation id and an `atomic.Int64` in-flight count |
| `TUIAdapter.WaitForEvent`, after `TranslateRuntimeEvent` | id, `ev.Type`, `ev.Seq`, `ev.UUID`, `%T` of the translated msg (log it **even when nil** — the nil case is what proves the loop kept reading rather than parking) |
| `AppModel.Update`, the `UserMessageSentMsg` / `…ConsumedMsg` / `…CancelledMsg` cases | uuid, and for `Sent` the text and `Passthrough` |
| `ChatList.ZoneAddUser` / `ZoneSettle` / `ZoneDrop` | uuid, hit/miss, and the zone's uuid list **after** the mutation |
| `UnifiedRuntime.markConsumed` / `cancelPendingUser` / `SendAllNow` | uuid, `transitioned`, `cancelled`, and the replacement uuid |

Gate all of it on an env var so it is inert by default. The decisive pairing is
**`bus.deliver` (enqueue order) against `adapter.recv` (return order) for the same two `Seq`
values** — nothing else in the system distinguishes a publish-order bug from a delivery-order bug.
