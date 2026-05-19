# Forensic: ratz 5-hour hang (QUM-588 close-out) — 2026-05-19

**Author:** ghost (researcher)
**Subject:** ratz / session `72f01cf2-d10a-4e74-99e8-a6c3e605f505`
**Branch under investigation:** `dmotles/qum-588-merge-lock-and-validate-popup`
**Base commit:** `de1873c` (one commit past `68fddb0` main; difference is `finn: QUM-587`, unrelated).
**Hypothesis posed by weave:** the Claude SDK queued a permission prompt that sprawl could not answer.
**Verdict on hypothesis:** **FALSIFIED.** The hang is not a permission prompt. It is a host-side stream-reader wedge that prevented two final tool calls (`mcp__sprawl__report_status` and `mcp__sprawl__send_message`) from ever reaching the in-process sprawl MCP server. ratz had already finished the work and was sending its close-out report when the wedge took effect.

Root cause is upstream of permission: claude is running with `--permission-mode bypassPermissions`, no `can_use_tool` control_request was ever emitted, and sprawl's `can_use_tool` handler already auto-allows (`internal/backend/session.go:511-517`). The 5-hour silence is the wedge symptom, not the cause.

---

## §1 Timeline

All timestamps UTC. Ground truth: Claude's session JSONL `~/.claude/projects/-home-coder-sprawl--sprawl-worktrees-ratz/72f01cf2-d10a-4e74-99e8-a6c3e605f505.jsonl` (1.27 MB, 423 lines, mtime frozen at `2026-05-19T03:55:25.303Z`).

### Pre-hang execution (2026-05-18)

| Time (UTC) | Source | Event |
|---|---|---|
| 22:26:55 | sprawl `agents/ratz/state.json` | session created (`created_at`) |
| 22:26:56 | activity.ndjson L1 | `system_state_changed: running` (first entry) |
| 22:27:17 | mcp-calls.jsonl | `caller=ratz report_status state=working "QUM-588 picked up"` |
| 22:39:18 | mcp-calls.jsonl | `caller=ratz report_status state=working "Part 1 done @ fba6ade"` |
| 22:55–22:56 | activity.ndjson | rapid burst: `git diff`, `go vet`, `go test`, `golangci-lint`, `Read help.go`, `Edit help.go` |
| 22:56:47.017 | activity.ndjson | `assistant_text: "Let me address the Ctrl+V help entry and the NoValidate edge."` |
| 22:56:52.895 | activity.ndjson | `tool_use: Edit help.go` |
| 22:56:55.798 | **activity.ndjson L406 (LAST WRITE)** | `assistant_text: "Now check help_test.go for golden expectations:"` |

### Post-wedge activity (sprawl host blind, claude internal log still advancing)

After 22:56:55, sprawl's activity.ndjson stops growing. **Claude continued executing for another 5 minutes 30 seconds**, visible only in its own JSONL (and in subagent JSONLs):

| Time (UTC) | JSONL line | Event |
|---|---|---|
| 22:56:55 → 23:01:01 | (subagent files) | sub-agents `agent-a615b2933263597f2` (qa-validator), `agent-a5589076a82bb0cb4`, etc. run and complete. Final subagent assistant text: `"All artifacts present. **VERDICT: PASS**"`. |
| 23:01:24.081 | L411 user | tool_result for `toolu_01Qjw4gZPKBG` (Bash) |
| 23:01:28.135 | L412 assistant | `Bash` tool_use |
| 23:01:28.168 | L413 user | tool_result for `toolu_01UpY2orgjb7` (Bash) |
| 23:02:00.602 | L414 assistant | `msg_01QxpWEUKG4goB8FTwNHwGFF` → `mcp__linear__save_comment` (uuid `21970e60`) — closing comment on QUM-588 |
| 23:02:00.926 | L415 assistant | same message → `mcp__linear__save_issue {id:"QUM-588", state:"Done"}` (uuid `a3d5d6f2`) |
| 23:02:01.650 | L416 user | tool_result for save_comment |
| 23:02:02.888 | L417 user | tool_result for save_issue (returns the QUM-588 record) |
| **23:02:06.812** | **L419 assistant** | **`msg_01An6gYXRpvtnzqjZenEM7nz` → `mcp__sprawl__report_status` (`toolu_01TWDmbQCNeW5swWRubj43f2`)** with `input = {state:"complete", summary:"QUM-588 done @ 65e141a. Part1 lock + Part2 streaming/log/popup. make validate + merge-reuse + tui-notify e2e green. CLI live smoke ✓. Follow-ups QUM-591/592 filed. Linear Done."}` |
| **23:02:25.672** | **L420 assistant** | **same message → `mcp__sprawl__send_message` (`toolu_01W6S2GnjdMzkshzsnv4oXUj`)** with `input = {to:"weave", body:"QUM-588 complete — branch tip 65e141a … (full ~2.7KB close-out report)"}` |

Both final tool_use entries share `requestId: req_011CbAsGCmCQducvaymBJWN4` and message id `msg_01An6gYXRpvtnzqjZenEM7nz`. (Each tool_use is its own JSONL line, but they were emitted in a single Anthropic message.)

### The 5-hour silence

From `23:02:25.672` to `03:55:25.196` — **5 hours 53 minutes** — nothing was written to the JSONL, the activity log, mcp-calls, or anywhere else. The claude process (`pid 1427324`) is still alive in state `S (sleeping)`, 11 threads, fds intact (pipes 0/1/2 to parent supervisor).

### Interrupt + synthetic rejection (2026-05-19)

| Time (UTC) | Source | Event |
|---|---|---|
| 03:50:47.476 | mcp-calls | `caller=weave peek agent=ratz tail=15` |
| 03:51:28.368 | mcp-calls | `caller=weave peek agent=ratz tail=5` |
| 03:55:25.032 | mcp-calls | `caller=weave send_message {to:"ratz", interrupt:true, body:"Wake-up check…"}` — interrupt+nudge |
| 03:55:25.190 | mcp-calls | call ends, `duration_s=0.157, status=ok` |
| 03:55:25.196 | JSONL L421 user | synthetic tool_result for `toolu_01TWDmbQCNeW5swWRubj43f2`: `"The user doesn't want to proceed with this tool use…"` (uuid `af3026d2`) |
| 03:55:25.196 | JSONL L422 user | synthetic tool_result for `toolu_01W6S2GnjdMzkshzsnv4oXUj`: same rejection text (uuid `d4a68e44`) |
| 03:55:25.204 | JSONL L423 user | text record `"[Request interrupted by user for tool use]"` (uuid `428af10d`) |
| 03:55:41.873 | mcp-calls | `caller=weave peek agent=ratz` — confirms no further progress |
| 03:58:06.162 | mcp-calls | `caller=weave peek` again |
| (subsequent) | none | claude still alive, sprawl still wedged from its perspective |

The "user rejected" wording is **not** a permission denial — it is claude-code's standardized synthetic `tool_result` body for in-flight tool calls aborted by the host's interrupt control_request. The clue is the absence of any `can_use_tool` control_request in the JSONL (`grep -c can_use_tool` = 0; `grep -c permission_request` = 0; the four `permission` hits in the file are all the static `"permissionMode":"bypassPermissions"` field on init records).

### `mcp-calls.jsonl` audit — the smoking gun

`mcp-calls.jsonl` records every sprawl MCP tool dispatch (phase `start` and phase `end`). Filtered for `caller=ratz`:

- Last successful call from ratz: **22:39:18** (`report_status "Part 1 done @ fba6ade"`).
- **Zero entries from ratz between 22:39:18 and 03:55:25.** The two final tool_uses at 23:02:06 and 23:02:25 **never crossed the MCP boundary**.

This is the single most important forensic fact. Claude emitted them locally (they are in its own JSONL), but the in-process sprawl MCP server never received them. The hang is therefore upstream of the MCP server, in the host's stdin/stdout protocol layer or in the host's reader→subscriber pipeline.

---

## §2 Root cause analysis

### What ratz was actually trying to do

Trivial sprawl tool calls:

1. `mcp__sprawl__report_status({state:"complete", summary:"QUM-588 done @ 65e141a …"})`
2. `mcp__sprawl__send_message({to:"weave", body:"QUM-588 complete — branch tip 65e141a …"})` — ~2.7 KB markdown close-out report (no attachments, plain UTF-8, contains backticks, em-dash, and a long Markdown table).

Both tools are explicitly enumerated in ratz's `--allowed-tools` list (verified via `pgrep -af claude` against pid 1427324, still running). Both tools are routine; ratz had called `report_status` minutes earlier in the same session without incident.

### Why the SDK did NOT classify them as permission-requiring

- The launch line includes `--permission-mode bypassPermissions`. (`internal/agentloop/session_spec.go:49`, `internal/claude/launch.go:55`, observed in live `pgrep` output.)
- In `bypassPermissions` mode, claude-code does **not** emit `can_use_tool` control_requests. The JSONL bears this out — there are zero `can_use_tool` requests in the entire 423-line file.
- Sprawl additionally implements a `can_use_tool` handler that auto-replies `behavior:"allow"` (`internal/backend/session.go:511-517`), so even if the SDK had asked, the response would have been instantaneous.

The "user rejected" tool_result text is a red herring. It is the synthetic body that claude-code writes into the JSONL when a host-side `interrupt` control_request aborts in-flight tool calls. The `is_error: true` flag is set on these synthetic results; they were written at the same millisecond (`03:55:25.196Z`) as the interrupt arrived, not at the time of the original tool_use.

### Where the hang actually lives

`mcp-calls.jsonl` proves the two tool_uses never reached the sprawl MCP server. Combined with the fact that **activity.ndjson stopped writing 5+ minutes before claude was still emitting tool_uses to its own JSONL**, the wedge sits in the host-side protocol consumer chain. Three candidates ranked by code inspection:

1. **`internal/backend/session.go:402` blocking subscriber send.** The persistent reader loop has

   ```go
   if tf != nil && tf.subscriber != nil {
       select {
       case tf.subscriber <- msg:
       case <-ctx.Done():
           return
       }
   }
   ```

   `tf.subscriber` is a buffered chan of size 100. If the turnloop drain falls behind (or the subscriber goroutine has terminated without unbinding the frame), the reader can block indefinitely sending into a full channel. While blocked it cannot service further `control_request` frames — including the `mcp_message` envelopes that carry `mcp__sprawl__*` tool calls. Claude code would then block on its own stdout pipe write once the OS pipe buffer fills, and the whole pipeline freezes.

2. **`Observer.OnMessage` blocking inside the reader.** `runReader` calls `s.config.Observer.OnMessage(msg)` synchronously (`internal/backend/session.go:363-365`). `ObserverWriter.OnMessage` invokes `ActivityRing.Append`, which performs a raw `w.Write(b)` to `activity.ndjson` with no timeout (`internal/agentloop/activity.go:69-77`). A blocked file write (rotated fd, full disk, paused fs) would freeze the reader the same way. The fact that the last activity.ndjson write timestamp (`22:56:55.798`) corresponds precisely to the moment the host stopped servicing the stream, while claude itself kept logging internally for 5.5 more minutes, is the canonical signature of an Observer-in-reader stall.

3. **EventBus subscriber drop, then turnloop-exit-without-unbind.** Less likely — EventBus already uses a 1 ms `trySendWithYield` and drops events for slow subscribers (`internal/runtime/eventbus.go:174-188`), so EventBus itself cannot wedge the publisher. But if the turnloop exits (via per-turn deadline / ctx-cancel) **without** closing or unsubscribing its read from `tf.subscriber`, the backend's `runReader` keeps sending into a buffered channel nobody is draining → revisit candidate (1).

Candidate (1) and (2) both produce the exact observed symptoms — a clean cliff in sprawl-visible activity, but claude still happy locally until its stdout pipe buffer fills. Which one is the actual culprit needs targeted reproduction (see §3); they are not mutually exclusive — fixing one without the other leaves a second wedge surface intact.

### Why the original "permission hang" hypothesis is falsified

| Symptom predicted by permission-hang hypothesis | Actual evidence |
|---|---|
| `can_use_tool` control_request in JSONL | **absent** (0 occurrences) |
| Permission mode requiring approval | **bypassPermissions** (explicit, on the live process) |
| Tools not on allowed list | **both tools explicitly allowed** (`pgrep -af` shows `--allowed-tools mcp__sprawl__report_status` and `--allowed-tools mcp__sprawl__send_message`) |
| `mcp-calls.jsonl` entry stalled in `phase:start` with no matching `phase:end` | **no `phase:start` entry at all** — the call never reached the MCP server |
| Activity.ndjson continuing past the suspect tool_use, demonstrating sprawl was still receiving stream-json events at the time | **activity stopped 5+ min BEFORE the tool_use was emitted in claude's own log** |

Permission was never the gate. The gate is sprawl's stdout reader, which stopped consuming claude's output at 22:56:55±ε, well before claude even attempted the close-out tool calls.

### Why the interrupt unblocked it partially

`Session.Interrupt` writes a control_request to claude's **stdin**, on a separate pipe. The host writer side was never wedged — only the reader side. When weave sent `send_message(interrupt: true)` at `03:55:25.032`, sprawl's writer wrote the interrupt onto stdin, claude's stdin reader (a separate goroutine in claude-code) picked it up, claude aborted in-flight tool_uses by synthesizing rejection results into its JSONL, and… presumably attempted to write the resulting `result` frame back on stdout, which is still wedged on the host side. So even after interrupt, the host side has not observed any new events (activity.ndjson still pinned at 22:56:55).

---

## §3 Reproduction recipe

The cheapest unambiguous repro targets candidate (2) (`OnMessage` blocking inside `runReader`) because it's directly testable without an actual stuck consumer:

```bash
# 1. Build sprawl
make build

# 2. Spawn an engineer agent in a sandbox (e2e harness gives you the SPRAWL_ROOT).
eval "$(bash scripts/sprawl-test-env.sh)"

# 3. While the agent is mid-turn, replace its activity.ndjson backing file with a
#    blocking sink:
#       AGENT=ratzlike
#       PROMPT_PIPE=$(mktemp -u); mkfifo "$PROMPT_PIPE"
#       mv "$SPRAWL_ROOT/.sprawl/agents/$AGENT/activity.ndjson" /tmp/old.ndjson
#       ln -s "$PROMPT_PIPE" "$SPRAWL_ROOT/.sprawl/agents/$AGENT/activity.ndjson"
#    (Or open a flock(2) exclusive on the fd to stall writes.)
#
# 4. Drive the agent to emit more tool_uses (any prompt continuation).
#    Observe: claude.jsonl keeps growing, activity.ndjson is pinned, the next
#    `mcp__sprawl__*` tool_use is invisible to mcp-calls.jsonl.
#
# 5. Time-bound: within 60s of the mtime gap on activity.ndjson, declare wedge.
```

A simpler synthetic repro that does not require the full e2e harness is to write a unit test against `internal/backend/session_test.go`'s mock transport that:

1. constructs a session with an Observer whose `OnMessage` blocks on a never-firing semaphore,
2. feeds in a normal turn,
3. feeds in a `control_request {subtype:"mcp_message"}` frame,
4. asserts no `control_response` is written within 100 ms.

That isolates the reader's synchronous-Observer wedge surface.

A repro is **achievable** but not deterministic against the real symptom unless the slow consumer is precisely modeled — the production failure may be triggered by a transient fs hiccup, an evicted fd, or a slow EventBus subscriber going silent. A standalone `scripts/repro-permission-hang.sh` is therefore deferred until a fix path is chosen — the script's shape depends on which surface gets fixed.

---

## §4 Recommended fix paths (with tradeoffs)

### Option A: Auto-reject all `can_use_tool` requests

**Not applicable here.** Sprawl already has the inverse — auto-allow — wired in (`internal/backend/session.go:511`). The wedge is not on the permission surface; rejecting permission requests would make no observable difference because none are being emitted.

| | |
|---|---|
| Effort | none |
| Files | n/a |
| Edge cases | n/a |
| Wrong if | the wedge surface really were `can_use_tool` |
| **Verdict** | **No-op.** |

### Option B: Auto-approve a sensible default set

Same as Option A — already done at a coarser granularity (all permission requests → allow). Not applicable.

### Option C: Surface permission via `ask_user_question`

Not applicable; nothing is being surfaced from claude.

### Option D: Hard timeout on permission requests

Not applicable; no permission request was issued. **However**, the *underlying* hardening — a per-frame timeout in `runReader` — IS the relevant defense. See §6.

### Option E: Eliminate the permission surface entirely

Already done via `bypassPermissions`. Not applicable.

### **Option F (NEW — the actual fix): Bound the host's reader-to-consumer chain**

The forensic evidence points at a host-side stdin/stdout pipeline stall, not a permission stall. Concrete fixes:

#### F1 — Non-blocking subscriber send in `runReader` (`internal/backend/session.go:401-407`)

Replace the unbounded send-on-subscriber with the same bounded-deadline pattern EventBus uses:

```go
if tf != nil && tf.subscriber != nil {
    timer := time.NewTimer(subscriberSendDeadline)  // e.g. 5 s
    select {
    case tf.subscriber <- msg:
        timer.Stop()
    case <-timer.C:
        s.recordSubscriberDrop(tf)              // metric + one-shot warn
        s.setFatalErr(errSubscriberWedged)      // unblock StartTurn waiters
        return                                  // tear down session — caller can restart
    case <-ctx.Done():
        timer.Stop()
        return
    }
}
```

| | |
|---|---|
| Effort | small — ~30 LOC + 2 tests in `internal/backend/session_test.go` |
| Files | `internal/backend/session.go` |
| Edge cases | (a) bursty turn with momentarily full chan — pick deadline ≥ slowest-realistic consumer (5 s feels safe for the chan capacity of 100). (b) draining order on tear-down — `drainInflight` already exists. (c) interrupt-during-wedge — ensure setFatalErr unwinds StartTurn waiters. |
| Wrong if | the wedge isn't in the subscriber send (e.g., if it's the Observer write). Then F1 closes one surface but the second remains. |

#### F2 — Observer call out of the reader hot path (`internal/backend/session.go:363-365`)

Move `Observer.OnMessage` to a goroutine-fed channel or wrap it with a context-bounded `select` so a blocked Observer can't stall the reader. The minimum-risk version:

```go
if s.config.Observer != nil {
    select {
    case s.observerCh <- msg:
    default:
        // observer is slow; drop one and bump a counter rather than block reader
        s.observerDropped.Add(1)
    }
}
```

A dedicated goroutine then drains `observerCh` and calls `OnMessage` serially.

| | |
|---|---|
| Effort | small — ~40 LOC, plus startup/shutdown plumbing for the goroutine and a test that proves a blocking OnMessage no longer stalls the reader. |
| Files | `internal/backend/session.go`, `internal/agentloop/session_spec.go` (verify lifecycle) |
| Edge cases | Order preservation w.r.t. control_request handling — Observer always observed messages in arrival order; preserve that with a single drain goroutine. Drop-on-overflow surfaces in DroppedCounts-equivalent counter. |
| Wrong if | the wedge isn't in OnMessage. Then we've added one drain goroutine for nothing — cheap insurance regardless. |

#### F3 — Bounded write deadline on `activity.ndjson` (`internal/agentloop/activity.go:76`)

Wrap the writer in a `*os.File` with a write deadline (or detect EAGAIN via O_NONBLOCK), or simply switch to an O_APPEND fd plus a select against a timer. A bare `w.Write(b)` to a regular file *can* block if the underlying fd has been replaced with a fifo or socket (e.g., during fs rotation), and `runReader` is hostage to that latency.

| | |
|---|---|
| Effort | small |
| Files | `internal/agentloop/activity.go` |
| Edge cases | We do not actually want to drop activity entries silently — surface drops to the ring buffer's in-memory record so peek can still expose them. |
| Wrong if | activity.ndjson is never the offender. Combine with F2 anyway. |

**Recommended hybrid: F1 + F2.** F1 closes the structural unbounded-send wedge; F2 closes the synchronous-callback wedge that the Observer pattern enables. Together they make the reader independent of all downstream consumers — the only thing that can stop it is ctx cancel or transport EOF. F3 is a cheap third belt for the specific activity-log surface.

---

## §5 Recommendation

**Ship F1 + F2 together** as the QUM-595-class hardening. Both are localized to `internal/backend/session.go` (with a tiny touch to `internal/agentloop/session_spec.go` for Observer goroutine lifecycle), both are unit-testable without spinning up a real claude binary, and both close orthogonal wedge surfaces that produce the same observed symptom.

### Tests that prove the fix works

1. **Unit (new):** in `internal/backend/session_test.go`, drive a turn where the subscriber chan is never read; assert reader does not block past `subscriberSendDeadline + slack`, that subsequent `control_request` frames are still serviced, and that a fatal error unwinds `StartTurn` waiters.
2. **Unit (new):** Observer whose `OnMessage` blocks on `<-never`; feed normal frames and a `control_request`; assert the control_request is dispatched within ms regardless of the Observer block, and that overflow drops surface in a counter.
3. **E2E (extend `test-handoff-e2e.sh` or `test-merge-reuse-e2e.sh`):** in a sandbox, mid-turn `mv activity.ndjson activity.ndjson.bak && mkfifo activity.ndjson`; let it sit 60 s; assert `mcp-calls.jsonl` still receives the next sprawl tool call (i.e., reader did not wedge).
4. **Smoke (new `scripts/repro-stream-reader-wedge.sh`):** the §3 fifo trick wrapped as a one-shot repro/regression test (not for CI, but documented).

Additionally, the existing **wedge detector** weave used to discover this hang (silent ≥ 5 hours) is the operator surface of last resort. It should remain in place and should be tightened (see §6).

---

## §6 Defensive measures (independent of fix choice)

### D1 — Reader-loop heartbeat / hang detector
Track `last_event_received_at` on the session. A monitor goroutine compares it against `time.Now()` every minute; if no event has arrived in N minutes (proposal: 10 min for engineer agents, configurable; honor existing per-turn deadline as the inner bound), emit `EventTurnFailed` wrapping a sentinel `errHangTimeout` and publish a `report_status failure(hang_timeout)` on behalf of the agent so weave's wedge detection fires automatically. This is **independent of QUM-594 (idle indicator)** — QUM-594 is a UI surface; this is a control-plane health check.

Implementation site: `internal/backend/session.go` (add a `lastFrameAt atomic.Int64`, plus a watchdog goroutine started by `Initialize`).

### D2 — `mcp-calls.jsonl` "received but undispatched" log line
Today `mcp-calls.jsonl` only logs the `phase:start` from the **handler** side. There is no log at the **reader** side. Add one log line per inbound `control_request` (subtype + request_id) at the moment `handleInlineControlRequest` is entered. A future forensic could then distinguish "claude never sent it" from "host got it but handler hung" — today both look identical (no `mcp-calls.jsonl` row at all).

### D3 — Surface `EventBus.DroppedCounts()` in `peek`
Drops today are surfaced only in slog warns. Folding the drop counts into `peek`'s output would have made this incident visible 5+ minutes after the wedge instead of 5+ hours.

### D4 — Per-frame subscriber-send timeout warning
Independent of fix F1: if `runReader` blocks on subscriber send past, say, 250 ms, emit a warn log with the frame type and outstanding turn. Even before F1's hard timeout lands, the log alone is enough for a sysadmin to find the wedge.

---

## §7 Reflections (per researcher protocol)

**Surprises:**
- The "permission rejected" wording in the JSONL is **claude-code's standard interrupt-abort body**, not a permission-denial. Easy to mis-read; this is worth documenting in `docs/research/claude-stream-json-protocol.md` so future forensics don't lose hours chasing the same red herring.
- `--permission-mode bypassPermissions` truly does suppress `can_use_tool` end-to-end — there are zero such requests in a 1.27 MB session log spanning 31 turn cycles.
- The blocking subscriber send at `internal/backend/session.go:402` is genuinely surprising: sprawl already has a non-blocking pattern (EventBus.trySendWithYield) that it just doesn't apply at this layer.
- `OnMessage` being invoked synchronously inside the reader loop is, in retrospect, an anti-pattern: any backend Observer becomes a stop-the-world dependency.

**Open questions:**
- Which of F1 vs F2 actually caused the production wedge? The forensic evidence is consistent with either. A live fix should land both and instrument so the next occurrence is unambiguous.
- Did the turnloop exit cleanly at 22:56:55? If yes, why did `tf.subscriber` remain bound (suggesting a session.go bug)? If no, why? The activity.ndjson timestamp gap is suggestive but not conclusive without a heap dump.
- Why is claude's process still alive ~6 hours later in `S` state? Presumably blocked on stdout write. Confirmation would require attaching `strace -p 1427324` (read-only, no SIGTRAP), which I deliberately did not do per the "no signals" constraint.
- Is the autonomous-turn frame allocation (`internal/backend/session.go:390-397`) involved? The very last successful frame in activity.ndjson is an `assistant_text` mid-turn, not a `result` boundary — so the original sprawl-initiated turn was still active. That means `tf.subscriber != nil` and candidate (1) is the most-likely surface.

**Investigations I'd do next given more time:**
- Attach `strace -ff -p 1427324` (without writing signals) to see exactly which syscall claude is blocked in — confirms the "stdout write blocked on full pipe" theory in one command.
- `cat /proc/1427324/wchan` and `/proc/1427324/stack` for the same purpose.
- Diff sprawl's `EventBus.DroppedCounts()` snapshot for ratz's session against zero — if dropped > 0 starting somewhere near 22:56:55, candidate (3) (turnloop-exit-without-unbind) gains weight.
- Audit `internal/backend/session.go` for every place a turnFrame can be created without a paired teardown.
- Read QUM-549 (`docs/research/qum-549-send-interrupt-during-mcp-tool-wait.md`) and QUM-570 references — they touched the same code and may have left a regression seam.

---

## §8 Evidence index

- **JSONL:** `/home/coder/.claude/projects/-home-coder-sprawl--sprawl-worktrees-ratz/72f01cf2-d10a-4e74-99e8-a6c3e605f505.jsonl` — 423 lines, mtime `2026-05-19T03:55:25.303Z`.
- **Activity:** `/home/coder/sprawl/.sprawl/agents/ratz/activity.ndjson` — 406 lines, last entry `2026-05-18T22:56:55.798Z`.
- **State:** `/home/coder/sprawl/.sprawl/agents/ratz.json` — `last_report_at: 2026-05-18T22:39:18Z`, `status: active`.
- **Sub-agent jsonls:** under `…/72f01cf2-…/subagents/`. Most recent: `agent-a615b2933263597f2.jsonl` (qa-validator) mtime `22:59`. Verdict on QUM-588: PASS.
- **mcp-calls:** `/home/coder/sprawl/.sprawl/logs/mcp-calls.jsonl` — searchable by `caller=ratz`.
- **Live process:** `pid 1427324`, parent `1416444`, state `S (sleeping)`, 11 threads, fds intact. **Do not kill.**
- **Branch tip:** `65e141a` on `dmotles/qum-588-merge-lock-and-validate-popup`. Working tree clean. Three commits off `68fddb0`.
- **Linear:** QUM-588 already marked `Done` by ratz at `23:02:00` (before the wedge fully isolated it). Linear-side state therefore reflects reality; only the sprawl-side report_status/send_message close-out is missing.

---

*End of forensic. Recommended next action for weave: file QUM follow-up (covered separately), then `kill --force ratz` is safe — its commits and Linear update are durable; only the unsent `send_message` body would be lost, which is recovered in §1 above for archival.*
