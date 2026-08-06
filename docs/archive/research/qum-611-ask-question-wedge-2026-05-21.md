# QUM-611: `ask_user_question` wedge — root cause and fix

**Researcher:** ghost
**Date:** 2026-05-21
**HEAD verified:** `6d108eb` (on `main` line)
**Branch:** `dmotles/qum-611-ask-question-wedge-research`

---

## 1. TL;DR

**The wedge is a *modal-dismiss-vs-question-cancel* asymmetry combined with a missing user-facing "cancel" affordance, not a timeout bug.**

Concretely:

1. The MCP `ask_user_question` tool has **no timeout** — it blocks on the
   bridge-supplied ctx until either (a) the question is resolved/cancelled,
   (b) Session.Interrupt cancels the bridge ctx, or (c) the backend session
   is torn down (drainInflight fires on reader exit). None of those happen
   automatically.
2. The TUI `DismissQuestionMsg` handler (`internal/tui/app.go:1630-1633`)
   **only hides the modal** — it never calls `Supervisor.CancelQuestion`.
   By design (QUM-538) drafts are preserved and Ctrl-Q reopens. But this
   means an Escape inside the modal does **not** unwedge the agent.
3. While the question is pending, the TUI's `turnState` is `TurnStreaming`
   (the underlying assistant turn never finalizes — it's parked on the
   tool_use awaiting a tool_result). The single-slot pending-submit gate at
   `internal/tui/app.go:675` queues every typed message: by-design queue
   behavior, but it looks like a freeze.
4. The "Let me know when you want to answer these questions" text is part
   of the *same* assistant message that emitted the tool_use — claude is
   permitted to interleave text content blocks with tool_use content blocks.
   It is not evidence of a second turn; it explains why the modal *and*
   the assistant text are visible together with the tool still in-flight.
5. Recover does **not** call `cancelByAgent` for the recovered agent's
   pending questions. Today it is rescued by drainInflight on the abandoned
   session's reader exit (cancels every bridgeCtx), but that's accidental —
   if a future refactor changes the teardown order, Recover would leak the
   pending question. (Minor — separate item.)

**Recommended fixes (in priority order):**

1. **Wire `DismissQuestionMsg` to call `Supervisor.CancelQuestion(activeRequestID, "user dismissed")`** so a single Escape ALSO unblocks the MCP call. Tradeoff: drafts are lost. Mitigation: split bindings — `Esc` = cancel-and-unwedge; `Ctrl-Q`-from-modal = hide-but-keep-pending. The new "cancel" path produces `OutcomeSessionEnded` (or a new `OutcomeUserDismissed`) so claude observes a clean tool_result and finalizes the turn.
2. **Add a status-bar hint** while a question is pending: "press Ctrl-Q to reopen, Ctrl-D / Esc-Esc to cancel". The user must know there's an out.
3. **Defensive: have `AgentRuntime.Recover` call `r.questions.cancelByAgent(agentName, "agent recovering")` before `StopAbandon`.** Cheap, idempotent, removes the implicit dependency on drainInflight ordering.
4. **Update the stale comment in `internal/backend/session.go:889-892`** — it says ask_user_question is "non-ctx-respecting"; it now IS ctx-respecting (`internal/supervisor/question.go:178-189`). Stale comments mislead future readers.

---

## 2. The lifecycle as it exists today

ASCII end-to-end. Every transition cites file:line.

```
┌──────────── claude subprocess ────────────┐
│ assistant emits content blocks:           │
│   - text "Let me know when you want…"     │
│   - tool_use mcp__sprawl__ask_user_question
│ → stdout JSON frame                       │
└──────────────────┬────────────────────────┘
                   │ JSONL on stdout
                   ▼
┌──────── session.go runReader ────────────────────────────────────────┐
│ frame matched as control_request (sdkMcpServers MCP routing)         │
│ session.handleInlineControlRequest → dispatchMCPAsync                │
│   - new bridgeCtx, cancel := context.WithCancel(parentCtx)           │
│   - s.inflight[requestID] = cancel        (session.go:817-823)       │
│   - go func() { mcpResp, err := bridge.HandleIncoming(bridgeCtx,…) } │
└──────────────────┬───────────────────────────────────────────────────┘
                   ▼ in goroutine
┌────────── host/mcp_bridge.go HandleIncoming ─────────────────────────┐
│ → sprawlmcp.Server.HandleMessage(ctx) (mcp_bridge.go:65)             │
│ → handleToolsCall                                                    │
│ → emit MCPCallStartedMsg(tool=ask_user_question)  (server.go:137)    │
│ → dispatchTool                                                       │
│ → toolAskUserQuestion(ctx, args)                  (server.go:219)    │
└──────────────────┬───────────────────────────────────────────────────┘
                   ▼
┌────── sprawlmcp/server.go:toolAskUserQuestion (575-625) ──────────────┐
│ askUserQuestionEligibility(ctx, caller)            (server.go:632)    │
│   - empty caller (root weave) → allowed                               │
│   - Type=="manager"||"root" → allowed; else canonical restricted err  │
│ build supervisor.QuestionRequest{RequestID:UUID, From:caller, …}      │
│ resp, err := s.sup.AskUserQuestion(ctx, req)       (server.go:616)    │
│   ┊ blocks here until response or ctx cancel                          │
└──────────────────┬───────────────────────────────────────────────────┘
                   ▼
┌────── supervisor/question_real.go AskUserQuestion (11-16) ────────────┐
│ → r.questions.ask(ctx, req)                                           │
└──────────────────┬───────────────────────────────────────────────────┘
                   ▼
┌────── supervisor/question.go questionQueue.ask (132-191) ─────────────┐
│ if closed → return OutcomeSessionEnded immediately   (q.go:138-145)   │
│ if len(consumers)==0 → return OutcomeTUIUnavailable  (q.go:146-152)   │
│ allocate entry{respCh:make(chan QR,1)}                                │
│ q.entries = append(…, entry)                         (q.go:162)       │
│ snapshot consumers under lock; signalChanged(); fan out OnEnqueue     │
│ select {                                             (q.go:175-190)   │
│   case resp := <-entry.respCh:                                        │
│       return resp, nil                                                │
│   case <-ctx.Done():                                                  │
│       q.cancelInternal(req.RequestID, OutcomeSessionEnded, ctx.Err()) │
│       return QuestionResponse{…OutcomeSessionEnded…}, nil             │
│ }                                                                     │
└──────┬──────────────────────────────────────────────────────────────┘
       │ OnEnqueue fan-out
       ▼
┌────── tui/question.go QuestionConsumer.OnEnqueue (515-520) ───────────┐
│ c.send(QuestionsAvailableMsg{Head: pq})                               │
└──────────────────┬───────────────────────────────────────────────────┘
                   ▼ tea.Msg
┌────── tui/app.go QuestionsAvailableMsg handler (1591-1621) ───────────┐
│ statusBar.SetPendingQuestions(depth, agentFromHead(head))             │
│ if !questionModel.HasPending() && head != nil:                        │
│     questionModel = Install(head)                                     │
│     if !anyOtherModalUp(&m):                                          │
│         questionModel = Show(); m.showQuestion = true                 │
└──────────────────┬───────────────────────────────────────────────────┘
                   ▼ user sees modal
            ╔══════════════════════════╗
            ║   USER ACTION OPTIONS    ║
            ╠══════════════════════════╣
       ┌────╫ Answer (Enter / D-all)   ║────────────┐
       │    ║ Decline ('d')            ║            │
       │    ║ Escape  (Hide modal)     ║─────┐      │
       │    ║ Ctrl-Q  (Hide modal)     ║─────┤      │
       │    ╚══════════════════════════╝     │      │
       │                                     │      │
       ▼                                     ▼      ▼
ANSWER PATH                          HIDE PATH    (no path: kill/retire from elsewhere)
└─ questionModel.submit()            └─ DismissQuestionMsg
   emits QuestionAnsweredMsg            └─ app.go:1630-1633: showQuestion=false,
   (question.go:316-319)                   questionModel.Hide(). NO supervisor call.
   └─ AppModel: ResolveQuestion          ❌ MCP call STILL BLOCKED.
      (app.go:1635-1655)                 ❌ Drafts preserved; Ctrl-Q reopens.
      → questionQueue.resolve
         (q.go:196-211)
         entry.respCh <- resp
   ↓
   tool returns resp text  ←──────────────────────────────────────────┐
                                                                       │
ALTERNATIVE EXITS (no user action):                                    │
                                                                       │
• Supervisor shutdown                                                  │
  closeAll(OutcomeSessionEnded, "supervisor shutdown")  (q.go:324-348) │
                                                                       │
• Agent Retire/Kill on the *caller* agent                              │
  Real.Retire / Real.Kill → cancelByAgent(from, "agent retired/killed")│
  (real.go:571, real.go:671 → q.go:251-283)                            │
                                                                       │
• Session.Interrupt (Esc OUTSIDE modal) on the *caller* agent          │
  session.Interrupt cancels every s.inflight bridgeCtx                 │
  (session.go:895-899)                                                 │
  → ctx.Done() branch in q.go:178 fires → cancelInternal               │
  → respCh <- {OutcomeSessionEnded}                                    │
                                                                       │
• Session teardown (Recover → StopAbandon → reader exit)               │
  defer drainInflight in runReader cancels every inflight (q.go via    │
  ctx.Done())                                                          │
                                                                       │
                                                                       │
                   ◄───────────────────────────────────────────────────┘
                   │ respCh delivered → ask returns
                   ▼
┌────── back in toolAskUserQuestion (server.go:620-624) ────────────────┐
│ data, err := json.MarshalIndent(resp, "", "  ")                       │
│ return string(data), nil                                              │
└──────────────────┬───────────────────────────────────────────────────┘
                   ▼ tool result string
┌────── back in handleToolsCall (server.go:182-184) ────────────────────┐
│ s.callLog.End(callID, "ok", "")                                       │
│ emit MCPCallEndedMsg(status=ok)                       (server.go:144) │
│ return toolSuccessResult(id, text)                                    │
└──────────────────┬───────────────────────────────────────────────────┘
                   ▼ JSON-RPC response
┌────── back in dispatchMCPAsync (session.go:836-850) ──────────────────┐
│ resp.Response.Response = {"mcp_response": mcpResp}                    │
│ s.transport.Send(parentCtx, resp)                                     │
│ delete(s.inflight, requestID); cancel()                               │
└──────────────────┬───────────────────────────────────────────────────┘
                   ▼ stdin to claude
┌──────────── claude subprocess ──────────────────────────────────────┐
│ receives tool_result; continues assistant turn                      │
│ … emits final assistant text + result frame                         │
└──────────────────┬──────────────────────────────────────────────────┘
                   ▼ stdout final
┌────── TurnLoop → eventbus → TUIAdapter → tui.SessionResultMsg ──────┐
│ AppModel SessionResultMsg handler (app.go:796-824):                 │
│   - finalizeTurn()                                                  │
│   - setTurnState(TurnIdle)              (app.go via finalizeTurn)   │
│   - auto-fire any queued submit         (app.go:1913-1920)          │
└─────────────────────────────────────────────────────────────────────┘
```

**Critical observation:** the path back from "Hide modal" (Esc / Ctrl-Q
from inside the modal) is `DismissQuestionMsg` and it has **no edge** that
calls `Supervisor.CancelQuestion`. The MCP call remains parked. That is the
single most important wire missing from the diagram above.

---

## 3. The five hypothesis-test paths

### H1. MCP call timed out overnight

**Verdict: REFUTED.** `toolAskUserQuestion` has no timeout
(`internal/sprawlmcp/server.go:583-625`). The ctx it receives is
`bridgeCtx`, a `context.WithCancel(parentCtx)` from
`dispatchMCPAsync` (`internal/backend/session.go:817`). `parentCtx` is
tied to the session's `readerCtx` (created in `runReader`); it lives as
long as the reader runs. There's no `WithTimeout` anywhere on this path.
The question therefore blocks indefinitely.

(Note: a deferred `cancel()` fires only when the *handler* returns —
which it won't, because the handler is itself parked on `<-respCh` /
`<-ctx.Done()`. So the timeout is not even a self-fulfilling structural
property; it's just absent.)

### H2. Recover injected a synthetic tool_result

**Verdict: REFUTED, but with a latent hole.** `AgentRuntime.Recover`
(`internal/supervisor/runtime.go:500-629`) does **not** call
`r.questions.cancelByAgent` and does **not** synthesize a tool_result.
However, the abandoned subprocess teardown path *does* indirectly
release the question:

- `runtime.Recover` detaches the watcher and calls `handle.StopAbandon(ctx)`
  (`runtime.go:584`).
- `StopAbandon` closes the backend session.
- `session.Close` (`session.go:929-…`) cancels `readerCtx`. The reader
  exits, and its `defer s.drainInflight()` cancels every `s.inflight`
  cancelFunc (`session.go:856-873`).
- The inflight ask_user_question's bridgeCtx is cancelled.
- The `<-ctx.Done()` branch at `question.go:178` fires.
- `cancelInternal` delivers `{OutcomeSessionEnded, …}` on `respCh`.
- `toolAskUserQuestion` returns its marshaled response.
- `transport.Send` is called against `parentCtx` (the now-cancelled
  readerCtx) at `session.go:847` — fails silently into `setFatalErr`,
  which doesn't matter because the subprocess is dead anyway.

So Recover unblocks the question **as a side-effect** of teardown
ordering. This is fragile: if a future refactor moves teardown around
(e.g., starts the new claude before draining the old one), the question
could leak. Recommend: have Recover proactively call
`r.questions.cancelByAgent(agentName, "agent recovering")` BEFORE
`StopAbandon`. Cheap, idempotent, removes the implicit dependency.

### H3. Modal dismissal path

**Verdict: CONFIRMED — primary root cause.**

- Inside-modal Escape (`question.go:265-267`) returns `DismissQuestionMsg`.
- Inside-modal Ctrl-Q (`question.go:269-272`) returns `DismissQuestionMsg`.
- `DismissQuestionMsg` handler (`app.go:1630-1633`):
  ```go
  case DismissQuestionMsg:
      m.showQuestion = false
      m.questionModel = m.questionModel.Hide()
      return m, nil
  ```
  It does NOT call `m.supervisor.CancelQuestion(activeRequestID, "user dismissed")`.

The question stays in `q.entries`. The MCP call remains parked. The
`PendingQuestions` count in the status bar stays at ≥1. The user sees the
modal disappear and reasonably assumes "I dismissed the question", but
claude is still mid-turn awaiting the tool_result. Input continues to
queue. Escape (with the modal hidden) WOULD now fire bridge.Interrupt —
but the user often doesn't realize they need to do that, and even when
they do, the QUM-549 stigma ("interrupt is best-effort during MCP-tool-
wait") is well-known and discouraging.

Fix: see §5 recommendation 1.

### H4. The "agent is running / queue input" lock-in

**Verdict: CONFIRMED — expected secondary symptom.**

`SubmitMsg` handler at `app.go:657-682`:

```go
if m.turnState != TurnIdle {
    m.pendingSubmit = msg.Text
    m.input.SetPendingPreview(msg.Text)
    return m, nil
}
```

`turnState` is set to `TurnStreaming` on every assistant content / tool
call frame (`app.go:706, 732, 781`). It is only cleared in three paths:
`SessionResultMsg` (`app.go:807`), `InterruptCompletedMsg` (`app.go:835`),
and `SessionRestartingMsg` (`app.go:900`). All three are *terminal* turn
events. While the assistant turn is parked on the tool_use, none of them
have fired, so `turnState` stays `TurnStreaming`.

This is the queue-vs-submit lock-in. It is **not** a bug per se — it's
the QUM-340 design. But it conceals the wedge: the user sees their typed
prompt accumulate in the queue indicator, with no obvious explanation
of why, because the modal that triggered the parked turn was hidden
silently by an Escape press.

### H5. The Escape-doesn't-interrupt path

**Verdict: PARTIALLY REFUTED — interrupt DOES work today, but only with
the modal hidden.**

The Escape gating in `app.go`:

```
470-474:  if m.showQuestion { delegate ALL keys to questionModel; return }
541-554:  if Escape && pendingSubmit != "" { revoke queued; return }
562-565:  if Escape && (TurnStreaming || TurnThinking) && bridge != nil
              { AppendStatus("Interrupting..."); return bridge.Interrupt() }
```

So with the modal visible, Escape goes to the modal → `DismissQuestionMsg`,
NOT to bridge.Interrupt. To actually interrupt the MCP call, the user
must press Escape *after* the modal is already hidden.

`bridge.Interrupt()` → `TUIAdapter.Interrupt` (`tuiadapter.go:142-153`)
→ `UnifiedRuntime.Interrupt` (`runtime/unified.go:406-435`)
→ `Session.Interrupt` (`backend/session.go:875-927`)
→ "Cancel every in-flight async MCP handler ctx FIRST" (line 895-899).

That cancellation propagates to the question's `<-ctx.Done()`. So an
escape with the modal hidden DOES unwedge.

Caveat: the comment at `session.go:889-892` is stale — it asserts
`ask_user_question` is "non-ctx-respecting" today, citing QUM-553. The
code says otherwise. Recommend updating the comment.

**Why dmotles believed interrupt "never observed by claude":** most
likely the modal was visible when he pressed Esc, so it became a
`DismissQuestionMsg` (hide only). He didn't see the interrupt status line
("Interrupting…") fire, but did see the modal vanish, and concluded "I
pressed Esc and nothing happened to the agent." That matches a single-Esc
attempt against a visible modal. A second Esc with the modal hidden would
have worked.

---

## 4. The forensic trace

**The wedge incident did not leave a trace in weave's `activity.ndjson`.**

I scanned `.sprawl/agents/weave/activity.ndjson` (7453 lines, last entry
2026-05-21T17:14:20Z). Filtering on `ask_user_question`:

- 2026-05-11: extensive ask_user_question development / dogfooding
  (QUM-527 / QUM-535 / QUM-538 work).
- 2026-05-12 22:21:18: `mcp__sprawl__ask_user_question` (the "Spawn structure for the wave" question, slice 2 dogfood).
- 2026-05-13 20:47:20 & 20:58:27: two more dogfood calls (notification-text shape work).
- 2026-05-20 00:02:32: `session_state_changed: running` — last session boot before the binary swap.
- **No `ask_user_question` calls overnight on 2026-05-20 → 2026-05-21.**
- 2026-05-21 16:51:18: weave finishes the QUM-610 fix commit (turn end).
- 2026-05-21 16:53:58 → 17:14:20: a flurry of session_state_changed bursts as dmotles ran finn's rebase / install / paste tests, then spawned ghost (this researcher).

So the wedge happened in **a session whose log has since been rotated
or whose log file is not weave's** — most likely in the pre-2026-05-21
binary that was running at the time of dmotles's overnight detach. The
binary swap at ~16:59 (`make install` after finn's merge) would have
torn down the wedged session along with everything else. The current
weave activity log starts after that re-init.

This means we cannot extract exact timestamps for: the original
ask_user_question call, when dmotles last interacted with it, or the
exact ordering of his morning Escape / queued input. The symptom report
is the only evidence.

**Forensic conclusion:** the code analysis stands on its own. The
hypothesis-rank conclusions in §3 do not require the forensic trace to
hold, because they are derived from static reading and not log
correlation.

---

## 5. Fix recommendation

**Opinionated. Recommended in priority order.**

### F1. Wire `DismissQuestionMsg` to cancel the upstream question (PRIMARY)

In `internal/tui/app.go:1630-1633`, change:

```go
case DismissQuestionMsg:
    m.showQuestion = false
    m.questionModel = m.questionModel.Hide()
    return m, nil
```

to (sketch — exact semantics TBD by design review):

```go
case DismissQuestionMsg:
    if id := m.questionModel.activeRequestID(); id != "" && m.supervisor != nil {
        m.supervisor.CancelQuestion(id, "user dismissed via Esc/Ctrl-Q")
    }
    m.questionModel = m.questionModel.Reset()  // not Hide — full reset
    m.showQuestion = false
    return m, nil
```

`CancelQuestion` (`question_real.go:33-36` → `q.go:215-217` →
`cancelInternal`) will:

- mark the entry done, deliver `{OutcomeSessionEnded, "user dismissed…"}` on `respCh`.
- fire `OnCancel` on every consumer (so multi-consumer setups stay in sync).
- the MCP tool returns the response → claude finalizes the turn → TurnIdle → input unlocks.

**Tradeoff: drafts are lost.** Mitigation options:

- **(a) Split the bindings.** Keep `Ctrl-Q` as "hide but keep pending"
  (drafts preserved). Make plain `Esc` mean "cancel and unwedge". The
  existing `question.go:265-267` already routes Esc into
  `DismissQuestionMsg`; just have the AppModel handler distinguish
  by source. (Requires a small message-type change: e.g.
  `DismissQuestionMsg{Hard: true}` for Esc, `{Hard: false}` for Ctrl-Q.)
- **(b) Always cancel on dismiss.** Simpler, no draft preservation;
  if the user reopens via Ctrl-Q after dismissing, they answer fresh.
  This is the "if you want to cancel, you can; if you want to answer,
  do it now" stance — arguably the right default for a feature that
  blocks the entire agent.

I recommend (a) — preserves the QUM-538 spirit while closing the wedge.

### F2. Add a "blocked on user question" status-bar hint

When `m.questionModel.HasPending() && !m.showQuestion`, the status bar
should display something like:

> `■ blocked on question from <agent>: Ctrl-Q to reopen, Esc to cancel`

Today the status bar shows the question count via
`SetPendingQuestions` (`app.go:1610`) but no actionable hint about Esc
cancelling. Pair with F1: the hint must reflect the (a)-split semantic.

### F3. Defensive: cancel-on-recover in `AgentRuntime.Recover`

Before `handle.StopAbandon(ctx)` in `internal/supervisor/runtime.go:584`:

```go
if r.real != nil {  // or via interface
    r.real.CancelByAgent(spec.Name, "agent recovering")
}
```

(needs a back-reference from `AgentRuntime` to the questionQueue;
easier: have `Real.Recover` do the cancel BEFORE calling
`runtime.Recover` — `Real.Recover` already has the questions queue:
`internal/supervisor/real.go:714-745`.) Sketch:

```go
func (r *Real) Recover(ctx context.Context, agentName string) error {
    if err := agent.ValidateName(agentName); err != nil { … }
    …
    // Release any AskUserQuestion calls originating from this agent
    // before tearing down its session.
    r.questions.cancelByAgent(agentName, "agent recovering")
    …
    err := runtime.Recover(ctx)
    …
}
```

Closes the implicit dependency on `drainInflight` ordering. Idempotent
with the existing `drainInflight` (cancelInternal is no-op on already-done
entries — `q.go:199`).

### F4. Update stale comment in `session.go`

Lines 889-892 currently claim ask_user_question is non-ctx-respecting.
Replace with:

```go
//   - ctx-respecting handlers (retire/delegate/merge/ask_user_question)
//     will unwind immediately.
```

Cite the actual ctx.Done() branch in `question.go:178-189`.

### F5. (Optional) bound the MCP call with a long timeout

Out of paranoia, attach a multi-hour timeout to `toolAskUserQuestion`:

```go
ctx, cancel := context.WithTimeout(ctx, 24*time.Hour)
defer cancel()
resp, err := s.sup.AskUserQuestion(ctx, req)
```

This bounds the worst-case "I went on vacation" scenario, returning a
clean `OutcomeSessionEnded` instead of leaving an entry in the queue. Low
priority — F1 closes the user-action gap; F5 only covers the
hypothetical "user never returns" case.

---

## 6. Open questions

1. **Should an unanswered question survive across `sprawl enter`
   restarts?** Today the question queue is in-memory; on sprawl restart
   the queue is empty. If `RecoverAgents` resumes the *agent* but its
   prior MCP call was lost (subprocess died with sprawl), claude
   re-resumes its transcript with an unanswered tool_use — does claude
   re-issue the tool? Empirical test: induce sprawl crash mid-question,
   restart, watch for claude's behavior on resume. **My read:** claude's
   `--resume` reattaches to the transcript; the unanswered tool_use is
   the last frame; claude re-issues. Worth verifying in a sandbox.

2. **Multi-consumer race on dismiss-vs-resolve.** If the dismiss path
   from F1(a) fires CancelQuestion while another consumer (e.g.,
   theoretical slack bridge) is simultaneously Resolving, the resolve
   wins (cancelInternal returns false on already-done entry,
   `q.go:224`). Is that the right precedence? Probably yes — resolve
   = the user supplied an answer — but worth a design line.

3. **What's the right `Outcome` for a user-cancelled question?** Today
   the only outcomes are `answered / declined / session_ended /
   agent_retired / tui_unavailable`. A user-dismiss is neither
   session_ended (the session is alive) nor agent_retired (the agent is
   alive). Worth adding a sixth: `OutcomeUserDismissed` so the calling
   agent can distinguish "user opted not to answer" from "the system
   tore me down." Affects prompt design — the calling agent should know
   whether to retry or give up.

4. **Should the modal auto-cancel after a long idle?** Related to F5
   but UX-side: even if F1 lands, a question left up for hours during
   an active session ties up the assistant turn. A "this question has
   been open for >1h, cancel?" prompt would be defensive. Probably
   premature.

5. **Why does dmotles say "the original ask_user_question MCP call still
   visible in viewport/banner"?** The QUM-497 in-flight banner shows
   `MCPCallStartedMsg` → `MCPCallEndedMsg`. If the call never ends,
   the banner stays. So the banner-still-visible part is consistent
   with the wedge. But the *viewport* shows the tool call card
   permanently (it's part of the assistant message). That's also
   consistent. Both observations are by-design; not bugs.

---

## 7. Reflection (researcher notes)

**Surprising:**

- The Recover path doesn't proactively cancel pending questions for the
  recovered agent. I expected the symmetry of Retire/Kill (which both
  call cancelByAgent) to also hold for Recover. The reason it doesn't
  break today is purely accidental — drainInflight on the abandoned
  session's reader exit picks up the slack.
- The DismissQuestionMsg → no-supervisor-call wire was a deliberate
  design choice (QUM-538 draft preservation), but no test asserts
  "after dismiss the question is still pending in the queue" — meaning
  the contract that "dismiss preserves drafts" silently doubles as
  "dismiss preserves the wedge."
- The comment at `session.go:889-892` calling ask_user_question
  "non-ctx-respecting" is at odds with the actual code; that was
  presumably true when the comment was written but the question queue
  has since been retrofit to respect ctx. Stale comments in
  high-stakes concurrency code are an active liability.

**Unanswered:**

- Forensic gap (§4): the actual wedge incident's logs are gone. We're
  reasoning from code + symptom report. If the fix lands and the
  symptom recurs, that's the falsification.
- The exact propagation of `bridgeCtx` through the bubbletea command
  graph during `bridge.Interrupt`: I traced it down to
  `Session.Interrupt` cancelling `s.inflight` cancellers — but did not
  verify in a sandbox that an MCP-tool-wait-blocked turn actually
  finalizes after the cancel. The test scaffold for this exists
  (`session_interrupt_bounded_test.go`); a focused test that drives a
  real ask_user_question + interrupt would close the loop.

**If I had more time:**

- Write a sandbox e2e harness that reproduces the wedge end-to-end
  (cf. `scripts/test-ask-user-question-e2e.sh`) by *not* answering the
  modal and instead just pressing Esc twice — assert that the agent's
  next turn fires. That would gate F1 the same way the existing harness
  gates the answer path.
- Audit every TUI modal's "dismiss" path for symmetric upstream
  cancel — confirm modal dismisses don't leak supervisor state
  elsewhere (palette, confirm, error). I would not be surprised if a
  similar latent bug exists in the palette dispatch path.
- Verify the claude `--resume` behavior across an unanswered tool_use
  (open question 1).

