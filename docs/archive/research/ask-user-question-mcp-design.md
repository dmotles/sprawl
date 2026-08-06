# `mcp__sprawl__ask_user_question` — design spike

**Author:** ghost (researcher)
**Branch:** `dmotles/research-mcp-ask-user-question-tui-modal`
**Date:** 2026-05-08
**Status:** Findings — no code changes. Hand to weave/dmotles for design review.

## Executive summary

Replacing the harness `AskUserQuestion` (broken under `--print --output-format
stream-json`) with `mcp__sprawl__ask_user_question` is mechanically a clean
extension of the patterns already in the repo:

- **MCP↔TUI bridge:** the existing `handoff` MCP tool already publishes onto a
  supervisor-owned channel that `cmd/enter.go` forwards into the TUI as a
  `tea.Msg`. We can reuse the same shape, but with one important upgrade —
  the question request needs a **per-call response channel** carried inside
  the channel payload so the MCP handler can block on it. Handoff is
  fire-and-forget; this is request/response.
- **TUI modal:** `internal/tui/confirm.go` and `internal/tui/error_dialog.go`
  show the canonical "modal overlay" pattern: a `showX bool` gate in
  `AppModel`, a sub-model owning state, key routing in `Update`, and a final
  `content = …View()` clobber in `View()`. The new modal is the same shape,
  with internal state for cursor/selection/text-input across N questions.
- **`huh` is not a fit** for v1 — it targets bubbletea v1; sprawl is on
  `charm.land/bubbletea/v2`. We can compose the modal from `bubbles/v2`
  primitives (textinput, viewport) plus hand-rolled select/multi-select. The
  UX surface is small enough that doing this is cheaper than a multi-day
  port effort.
- **Queue manager:** lives in the supervisor layer, not the TUI, so it
  survives TUI session restarts (`/handoff`). FIFO ordering, owned by a
  `sync.Mutex`, exposes one `<-chan PendingQuestion` to the host. Pending
  questions persist across the EOF→restart boundary; only a true TUI exit
  (`Ctrl-C`) closes them with a `user_unavailable` error.
- **No-TUI fallback:** when the supervisor was constructed without a TUI
  (e.g. agent is calling from a tmux root-loop weave or a headless test),
  the queue manager refuses with a structured `tui_unavailable` error — the
  agent then knows to fall back to `send_async` with a question to weave.

Key trade-offs:

1. Roll-our-own form vs huh: rolling-our-own costs ~1 day; huh would cost a
   bubbletea-version migration we don't want.
2. Persisting the queue across restart vs failing fast: I recommend
   persisting because `/handoff` is benign (the next session's TUI re-binds
   to the same supervisor's channel), but flushing on Ctrl-C; details in
   §3.1.
3. Storing the response channel in supervisor memory only (not on disk):
   a server crash drops the question — acceptable for v1, the agent will
   eventually time out at the model layer.

---

## Thread 1 — Bubbletea / bubbles ecosystem

### What's already in `internal/tui/`

The TUI follows a single-`AppModel`-with-many-sub-models pattern. Modals are
implemented as sub-models toggled by a `bool` flag and rendered last in
`View()`. Concrete examples worth modeling on:

- **`internal/tui/confirm.go`** — minimal modal: `visible` flag,
  `Show()/Hide()`, key-routed `Update(KeyPressMsg)` that emits a result msg
  (`ConfirmResultMsg{Confirmed bool}`), centered `View()` with
  `lipgloss.NewStyle().Border(...).Padding(...).Width(...)` rendered into a
  `lipgloss.Place(width, height, Center, Center, ...)` overlay.
- **`internal/tui/error_dialog.go`** — same structure, accepts only `r`/`q`.
- **`internal/tui/palette.go`** — richer modal: filter input, list cursor,
  multi-mode (command vs agent), ESC dismisses via `ClosePaletteMsg`.
  Demonstrates the "owns all keypresses while visible" pattern (`app.go:418`).
- **`internal/tui/app.go:1458-1472`** — the layering chain in `View()`:

  ```go
  if m.showPalette { content = m.palette.View() }
  if m.showHelp     { content = m.help.View() }
  if m.showConfirm  { content = m.confirm.View() }
  if m.showError    { content = m.errorDialog.View() }
  ```

  Modals replace the entire content; they don't compose. That's fine for our
  use case because the question modal also wants the full screen for legibility.

- **Modal gating in input paths** — `app.go:300, 312, 388, 410, 418, 516`
  show the canonical "if any modal up, suppress" guard. Adding
  `m.showQuestion` is one new boolean threaded through the same set of
  guards.

- **Status bar** — `internal/tui/statusbar.go` renders persistent indicators
  for turn state, session ID, cost, etc. The "N questions queued" indicator
  lives here as a new field with `SetPendingQuestions(n int)`.

### Patterns we'll reuse (not invent)

- `KeyPressMsg` routing: we own all keys when modal visible.
- `lipgloss.Place(...)` for centering.
- `tea.Cmd` returning a result msg (e.g. `QuestionAnsweredMsg`) instead of
  doing work in `Update`. Test-friendly.
- `bubbles/v2` is on `charm.land/bubbles/v2 v2.1.0`. We have `spinner`
  already; `textinput` is available there for the free-text answer field.

### Sub-model: `QuestionModel`

Sketch:

```go
type QuestionModel struct {
    theme   *Theme
    width   int
    height  int
    visible bool

    // Current request — nil when idle.
    req *PendingQuestion // see Thread 2

    // Per-question state. Indexed parallel to req.Questions.
    answers []QuestionDraft
    qIdx    int   // currently focused question

    // State for the focused question.
    cursor       int
    multiPicked  map[int]struct{} // for multi-select
    customInput  textinput.Model  // for free-text "Other..."
    inputMode    inputMode        // selecting | typing | declining
}

type QuestionDraft struct {
    SelectedIdxs []int   // -1 means "decline", -2 means custom text
    CustomText   string
}

type inputMode int
const (
    modeSelect inputMode = iota
    modeText
)
```

**Key plan (modal-active):**

| Key            | Action                                                    |
|----------------|-----------------------------------------------------------|
| `↑`/`↓`/`j`/`k`| Move cursor in option list                                |
| `Space`        | Toggle option (multi-select); ignored on single-select    |
| `Enter`        | Single-select: pick + advance to next question. Multi: confirm picks + advance. On last question: submit the whole batch. |
| `Tab` / `n`    | Skip to next question without picking (validation later)  |
| `o`            | Enter free-text "Other" answer (textinput focus)          |
| `d`            | Decline this question — special "I'd rather just talk"    |
| `D`            | Decline ALL questions and dismiss the modal               |
| `Esc`          | Dismiss modal (NOT decline — agent stays blocked)         |
| `Ctrl-Q`       | Same as `Esc` (mnemonic: quiet)                           |

**Dismiss/restore:** `Esc` flips `m.showQuestion = false` but **does not
clear `m.questionModel.req`**. A new keybind — proposal `Ctrl-?` (or `?` from
the tree panel) and a status-bar click target — re-shows the modal with all
draft state preserved. The status bar shows `🔔 weave is asking (Ctrl-? to
view)`. When 2+ questions queue, badge becomes `🔔 weave +2 more`.

### Why not `huh`

- `charmbracelet/huh` v0.6.0+ is built on `github.com/charmbracelet/bubbletea`
  (v1). Sprawl is on `charm.land/bubbletea/v2 v2.0.3` and
  `charm.land/lipgloss/v2 v2.0.2`. The two namespaces don't interop; a huh
  `Form` cannot be embedded in a v2 `tea.Model`. Even if we vendored a v2
  port, huh's UX (one field per page, animated transitions) doesn't match
  the TUI's existing static modal aesthetic.
- We need exactly three field types (single-select, multi-select, text).
  `bubbles/v2/list` + `bubbles/v2/textinput` covers it in <300 lines. The
  decline/skip semantics are sprawl-specific and would have to be retrofitted
  onto huh anyway.

### Modal does not block the event loop

Bubble Tea's `Update` is a pure function — no goroutine is parked. The MCP
handler blocks on a Go channel; the TUI just toggles flags and routes keys.
This is the same shape as `ConfirmModel` (key→msg→model update→re-render).

---

## Thread 2 — MCP ↔ TUI bridging

### Existing precedent: `handoff`

```
weave → MCP tool "handoff"
   ↓
sprawlmcp.toolHandoff      (server.go:391)
   ↓
supervisor.Handoff()       (real.go:503) — persists summary, writes signal file,
                                            non-blocking send on chan<-struct{}
   ↓ returns immediately ("Handoff recorded.")
                            ┊
HandoffRequested chan ─────►cmd/enter.go:616 goroutine ─► tea.Program.Send(HandoffRequestedMsg{})
                                                          ↓
                                                          AppModel.Update (app.go:787)
```

Handoff is **fire-and-forget**: the MCP tool returns "Handoff recorded.
Session will restart momentarily." before the TUI even teardowns. There is
**no synchronous response path today**.

### What our tool needs

The MCP handler must:

1. Generate a `request_id`.
2. Hand a `PendingQuestion` value (with an embedded response channel) to the
   supervisor's question queue.
3. Block on `<-respCh` (with `ctx.Done()` cancellation).
4. On wake: marshal the answer to JSON, return it as the tool result text.

Type signatures:

```go
// internal/supervisor/question.go (new file)

type Question struct {
    ID          string   `json:"id"`            // stable id, agent-supplied or generated
    Header      string   `json:"header"`        // short label (e.g. "Scope")
    Prompt      string   `json:"question"`      // the actual question
    MultiSelect bool     `json:"multi_select"`
    Options     []QOption `json:"options"`      // 1..N pre-baked options
    AllowCustom bool     `json:"allow_custom"`  // free-text "Other"; default true
    AllowDecline bool    `json:"allow_decline"` // single-question decline; default true
}

type QOption struct {
    Label       string `json:"label"`
    Description string `json:"description,omitempty"`
}

type QuestionRequest struct {
    RequestID string     `json:"request_id"` // uuid
    From      string     `json:"from"`       // agent identity (CallerIdentity(ctx))
    Questions []Question `json:"questions"`
}

// PendingQuestion is the queue element. Owned by Supervisor; visible to
// host (TUI). respCh is private so callers can't accidentally double-resolve.
type PendingQuestion struct {
    Req       QuestionRequest
    EnqueuedAt time.Time

    // hook so the queue manager can mark resolved exactly once.
    respCh chan<- QuestionResponse
}

type QuestionResponse struct {
    RequestID string         `json:"request_id"`
    Answers   []QuestionAnswer `json:"answers"`         // one per question, may be partial
    Outcome   string         `json:"outcome"`           // "answered" | "declined" | "session_ended" | "tui_unavailable"
    Note      string         `json:"note,omitempty"`    // free-form, e.g. "user dismissed via Ctrl-C"
}

type QuestionAnswer struct {
    QuestionID  string   `json:"question_id"`
    Selected    []string `json:"selected,omitempty"`     // option labels picked
    CustomText  string   `json:"custom_text,omitempty"`  // when user typed "Other"
    Declined    bool     `json:"declined,omitempty"`     // per-question decline
}
```

Supervisor-side queue:

```go
type questionQueue struct {
    mu      sync.Mutex
    pending []*pendingEntry
    waiters chan struct{} // signals new arrivals to host poller (buffered 1)

    // shutdown closes all open requests with outcome="session_ended"
    closed bool
}

type pendingEntry struct {
    pq    *PendingQuestion
    cancel context.CancelFunc
}

// AskQuestion enqueues and blocks until response or ctx done. MCP handler entry.
func (q *questionQueue) AskQuestion(ctx context.Context, req QuestionRequest) (QuestionResponse, error)

// Next returns the head (or nil) without dequeueing. Host calls this to peek for status bar.
func (q *questionQueue) Next() *PendingQuestion

// Take removes the head and returns it. Host calls when ready to display.
func (q *questionQueue) Take() *PendingQuestion

// Resolve writes a response and removes the entry. Idempotent.
func (q *questionQueue) Resolve(requestID string, resp QuestionResponse) bool

// Cancel closes a single request (e.g. agent retired).
func (q *questionQueue) Cancel(requestID string, reason string) bool

// Snapshot returns the queue depth + head agent name for status bar.
func (q *questionQueue) Snapshot() (depth int, headAgent string)
```

Add to the `Supervisor` interface:

```go
AskUserQuestion(ctx context.Context, req QuestionRequest) (QuestionResponse, error)
QuestionsChanged() <-chan struct{}        // ticks on enqueue/cancel; for status-bar refresh
ResolveQuestion(requestID string, resp QuestionResponse) bool
CancelQuestion(requestID string, reason string) bool
PeekQuestions() (depth int, head *PendingQuestion)
```

### MCP tool wiring

```go
// internal/sprawlmcp/server.go new dispatch case
case "ask_user_question":
    return s.toolAskUserQuestion(ctx, args)

func (s *Server) toolAskUserQuestion(ctx context.Context, args json.RawMessage) (string, error) {
    var p struct {
        Questions []supervisor.Question `json:"questions"`
    }
    if err := json.Unmarshal(args, &p); err != nil {
        return "", fmt.Errorf("invalid arguments: %w", err)
    }
    if len(p.Questions) == 0 {
        return "", fmt.Errorf("at least one question required")
    }
    req := supervisor.QuestionRequest{
        RequestID: uuid.NewString(),
        From:      backendpkg.CallerIdentity(ctx),  // see internal/backend/context.go:16
        Questions: p.Questions,
    }
    resp, err := s.sup.AskUserQuestion(ctx, req)
    if err != nil {
        return "", err
    }
    out, _ := json.MarshalIndent(resp, "", "  ")
    return string(out), nil
}
```

Tool definition (matches `AskUserQuestion` muscle memory; see harness sample
captured in `weave/activity.ndjson` line 3132):

```json
{
  "name": "ask_user_question",
  "description": "Surface a multiple-choice question to the human user. Blocks until the user answers or declines.",
  "inputSchema": {
    "type": "object",
    "properties": {
      "questions": {
        "type": "array",
        "items": {
          "type": "object",
          "required": ["question", "options"],
          "properties": {
            "id":          {"type": "string"},
            "header":      {"type": "string"},
            "question":    {"type": "string"},
            "multi_select":{"type": "boolean"},
            "options": {
              "type": "array",
              "items": {
                "type": "object",
                "required": ["label"],
                "properties": {
                  "label":       {"type": "string"},
                  "description": {"type": "string"}
                }
              }
            }
          }
        }
      }
    },
    "required": ["questions"]
  }
}
```

(`AllowCustom` and `AllowDecline` default to true server-side so callers
don't need to set them.)

### TUI bridge wiring

In `cmd/enter.go`'s `onStart` closure, add a third forwarder goroutine
mirroring the handoff one (`cmd/enter.go:616-632`):

```go
go func() {
    ch := sup.QuestionsChanged()
    for {
        select {
        case <-handoffDone:
            return
        case _, ok := <-ch:
            if !ok { return }
            depth, head := sup.PeekQuestions()
            send(tui.QuestionsAvailableMsg{Depth: depth, Head: head})
        }
    }
}()
```

New `tea.Msg` types in `internal/tui/messages.go`:

```go
type QuestionsAvailableMsg struct {
    Depth int                       // total queue depth
    Head  *supervisor.PendingQuestion // currently-front; may be nil if cancelled
}

type QuestionAnsweredMsg struct {
    RequestID string
    Response  supervisor.QuestionResponse
}

type ShowQuestionMsg struct{} // user keybind to re-summon

type DismissQuestionMsg struct{} // user keybind to hide (preserve state)
```

`AppModel.Update` handles `QuestionsAvailableMsg` by:
1. Updating status bar `SetPendingQuestions(depth)`.
2. If no other modal up AND `m.questionModel.req == nil` (no in-flight),
   call `sup.PeekQuestions()` to claim the head, set
   `m.questionModel.req = head`, flip `m.showQuestion = true`.
3. If a question modal already in flight (we're showing question 1 of N),
   just refresh the badge — don't preempt.

`QuestionAnsweredMsg` flows the other way: TUI's `QuestionModel` calls
`sup.ResolveQuestion(...)` (synchronous), then emits
`DismissQuestionMsg`. The MCP handler wakes and returns to claude.

### Concurrency safety

- `questionQueue.mu` serializes all queue mutations. The MCP handler does
  the channel make + enqueue under the lock, then unlocks before blocking
  on the channel.
- `Resolve` and `Cancel` are idempotent and check for already-closed
  channels before sending. Use `select { case respCh <- resp: default: }`
  with a `done bool` guard so a TUI race (modal answers while the tool
  context cancels) can't double-write.
- Multiple agents calling concurrently → FIFO by enqueue order, single
  modal at a time. Status-bar shows depth.

### Storage

In-memory only. The pending queue lives on `Real` next to `handoff`. We do
NOT persist questions to disk for v1 because:
- Persistence costs us nothing the model layer doesn't already give us
  (each agent has a turn-level retry).
- A weave subprocess crash already invalidates the response channel, so
  there's no recovery path for the blocked agent in any case.

---

## Thread 3 — Concurrency, edge cases, failure modes

### 3.1 `/handoff` while a question is pending

`/handoff` triggers `RestartSessionMsg` which closes the bridge and runs
`FinalizeHandoff` + `Prepare` + `newSession` (`tui/app.go:817-848`). The
**supervisor process is not torn down** — it persists across the bridge
restart (this is the whole reason `cmd/enter.go` builds `sup` once and
hands it to `NewAppModel`).

Therefore: questions in the queue **survive** `/handoff`. After the new
session boots:
- The new `AppModel` is constructed; it doesn't know about pending questions.
- `cmd/enter.go`'s forwarder goroutine is still alive (it's keyed on the
  same `sup`), so we add: on `RestartCompleteMsg`, AppModel proactively
  calls `sup.PeekQuestions()` and re-shows the modal if non-empty.

This is a *feature*: a user can `/handoff`, the next session inherits the
question, and the agent is still blocked (still inside its single MCP call,
which doesn't care about session-bridge identity).

**Caveat:** the agent's MCP call's `ctx` may be tied to the old session's
backend. Need to verify in implementation whether
`backend.WithCallerIdentity(ctx, …)` ctx survives a `/handoff` — I think it
does because the MCP server is its own goroutine inside the supervisor
process, not inside the session subprocess. Open question for thread 3 below.

### 3.2 Agent killed/retired while pending

`Supervisor.Retire(...)` and `Supervisor.Kill(...)` both terminate a child
agent. The child's MCP call ctx is cancelled by the runtime when the
subprocess dies. The MCP handler's `select { case <-respCh: case <-ctx.Done(): }`
unblocks via ctx.Done() and returns an error to the (now-dead) caller. The
queue entry is leaked unless we clean up.

Fix: wrap the supervisor's `Retire`/`Kill` paths to call
`questionQueue.CancelByAgent(agentName, "agent retired")`, which
walks the queue and resolves any matching entries with
`outcome="agent_retired"` — same as `Cancel` but matches by `From`. Any
entries that have already advanced to "active modal" need the modal
dismissed too — emit `CancelQuestionMsg{RequestID}` to the TUI.

Because `Retire` may cascade (parent retires children), do this in a single
queue lock pass by collecting all matching IDs first.

### 3.3 User declines

Two flavors:

- **Per-question decline (`d` key):** `QuestionAnswer.Declined = true` for
  that question; other questions in the same request can still have answers
  or be declined too. `QuestionResponse.Outcome = "answered"` (or
  `"partial"`?) since *some* engagement happened.
- **Whole-request decline (`D` key, "I'd rather just talk"):** all
  per-question answers cleared, `QuestionResponse.Outcome = "declined"`.

I recommend a single `outcome` field with values:
- `"answered"` — at least one question got a non-decline answer
- `"declined"` — all questions declined (whole-request decline OR every
  per-question decline)
- `"session_ended"` — TUI exited (Ctrl-C confirm) before answer
- `"agent_retired"` — issuing agent was killed/retired
- `"tui_unavailable"` — no TUI registered with supervisor

Agent prompt guidance: when `outcome != "answered"`, the agent should NOT
re-ask immediately; it should `send_async` to weave or fall back to plain
text. Phrasing in the tool's description doc should be explicit.

### 3.4 Free-text answers

The `o` ("Other") keybind focuses a `textinput.Model`. On Enter the typed
string lands in `QuestionAnswer.CustomText` and `Selected` stays empty (or
`["__custom__"]` sentinel — pick one). I lean **empty `Selected` +
non-empty `CustomText`** so the wire format is unambiguous.

### 3.5 No TUI running

Two sub-cases:

- **Tmux root-loop weave** (legacy): the supervisor exists but no
  `QuestionsChanged()` listener is wired. We need a mode bit on the
  supervisor: `RegisterQuestionConsumer(name string)` so the queue can
  detect "no consumer" and fail-fast with `outcome="tui_unavailable"`.
  Returning a structured failure (rather than blocking forever) lets the
  agent fall back to `send_async`.
- **MCP-only sandboxed test**: same path; no consumer registered.

Implementation: `questionQueue` tracks a single registered consumer (the
TUI forwarder goroutine calls `RegisterQuestionConsumer("tui")` on start
and `UnregisterQuestionConsumer()` on shutdown). `AskQuestion` checks
under the lock before enqueuing; if no consumer, immediately returns
`outcome="tui_unavailable"`.

### 3.6 Schema parity with harness `AskUserQuestion`

From the captured calls in `weave/activity.ndjson:3132`:

```json
{
  "questions": [
    {
      "question": "Scope — which issues should the manager drive?",
      "header": "Scope",
      "multiSelect": false,
      "options": [
        {"label": "QUM-493 cluster only", "description": "..."}
      ]
    }
  ]
}
```

Harness uses **camelCase** (`multiSelect`). Sprawl MCP tools use
**snake_case** elsewhere (`agent_name`, `reply_to`). I recommend:
- Server-side, accept BOTH `multi_select` and `multiSelect` (lenient JSON
  decoder) so weave's muscle memory works.
- Tool description in the spec uses `multi_select` as canonical to match
  sprawl convention.
- Add `description` field on options identically.
- Drop `header` (it's pure UI sugar; the question text is enough). Or
  keep it — marginal cost. Recommend keep, harmless and useful.

### 3.7 The "calling-agent-IS-weave" case

Weave is the most likely caller, and the modal pops in weave's own TUI.
That's fine — the agent identity in the modal banner reads "weave is
asking" which is honest. But the UX implication is: **weave is blocked on
its own tool call while the user types**, meaning weave can't be sending
asyncs or doing background work. This is the intended semantics; just
flag it in the design discussion. (If we ever want non-blocking ask, it's
a separate tool.)

---

## Recommended design (consolidated)

### Wire-level (MCP)

Tool name: `mcp__sprawl__ask_user_question`. Accepts
`{ questions: [{question, header?, multi_select?, options:[{label,description?}]}] }`.

Returns JSON:
```json
{
  "request_id": "uuid",
  "outcome": "answered|declined|session_ended|agent_retired|tui_unavailable",
  "answers": [
    {"question_id": "q1", "selected": ["..."], "custom_text": "", "declined": false}
  ],
  "note": "optional human-readable string"
}
```

### Internal type plumbing

```
                                   ┌──────────────────┐
   weave tool call                  │  AppModel        │
   ───────────────────────►         │  showQuestion    │
   sprawlmcp.toolAskUserQuestion    │  questionModel   │
        │                           │  (sub-model)     │
        │ ctx, QuestionRequest      └──────▲───────────┘
        ▼                                  │ tea.Msg: QuestionsAvailableMsg
   supervisor.AskUserQuestion              │           DismissQuestionMsg
        │                                  │           ShowQuestionMsg
        │  enqueue → questionQueue ───────►│ (cmd/enter.go forwarder goroutine)
        │                                  │
        │   block on respCh <───── ResolveQuestion ◄─── QuestionAnsweredMsg
        ▼                                  │             from QuestionModel.Update
   QuestionResponse                        │
        │                                  │
        ▼                                  │
   tool result (JSON)                      │
   ───────────────────────►                │
   weave receives answer                   │
                                           │
                                ┌──────────┴────────────┐
                                │ ctrl-? rebinds modal  │
                                │ Esc dismisses (state  │
                                │     preserved)        │
                                └───────────────────────┘
```

### Files touched (estimate, for the implementation issue)

| Area | New | Modified |
|------|-----|----------|
| `internal/supervisor/question.go`         | ✓ | — |
| `internal/supervisor/supervisor.go`       | — | + 5 interface methods |
| `internal/supervisor/real.go`             | — | + queue field, init in `NewReal`, hooks in `Retire`/`Kill`/`Shutdown` |
| `internal/sprawlmcp/server.go`            | — | + dispatch case + `toolAskUserQuestion` |
| `internal/sprawlmcp/tools.go`             | — | + tool definition |
| `internal/sprawlmcp/mcptoolnames.go`      | — | + `"ask_user_question"` |
| `internal/tui/question.go`                | ✓ | (new modal sub-model, ~300 LOC) |
| `internal/tui/messages.go`                | — | + 4 msg types |
| `internal/tui/app.go`                     | — | + `showQuestion` flag, `questionModel` field, gating, View() composition, `RestartCompleteMsg` re-poll |
| `internal/tui/statusbar.go`               | — | + `SetPendingQuestions` |
| `cmd/enter.go`                            | — | + forwarder goroutine in `onStart` |
| `internal/rootinit/tools.go`              | — | (separate task: drop `"AskUserQuestion"` from `RootTools`) |

Tests: unit (queue concurrency), unit (modal key routing & state preserve),
integration (mcp-tool→queue→tui→answer round-trip with a fake TUI consumer).

---

## Alternatives considered

1. **Use `huh` as the form library.** Rejected — see Thread 1; bubbletea v1
   vs v2 namespace incompatibility.

2. **Bypass MCP and use a special protocol message on the bridge.** Rejected
   — MCP tool surface is the contract every agent already speaks. Adding a
   new bridge protocol message would only help weave; we want the same tool
   available to children.

3. **Fire-and-forget tool + structured async response via inbox.** I.e. the
   tool returns "queued" immediately, the user answers, and the answer
   lands as an inbox message the agent reads on next turn. Rejected for v1
   because:
   - Loses the "blocking" UX the harness `AskUserQuestion` provides; the
     calling model has to write context-juggling logic to wait for answers.
   - Doesn't fix the bug we have (weave answering raw JSON manually).
   - Worth keeping in our back pocket for the future-paging extension; see §below.

4. **Persist queue to disk.** Rejected for v1 (see Thread 2 §Storage). Reopen
   if we hit "supervisor crashed mid-question, agent stuck" reports.

5. **Per-agent question modals, allow concurrent display.** Rejected — UX
   nightmare with overlapping modals. FIFO is simpler and matches "human
   can only think about one question at a time."

---

## Open questions for weave / dmotles

1. **Modal dismiss keybind.** Proposed `Ctrl-?` to re-summon; does this
   conflict with anything? `?` is already used for help-toggle when not in
   the input panel (`app.go:396`). Possibly use `Ctrl-Q` instead, mnemonic
   "question."

2. **MCP ctx survives `/handoff`?** I'm 95% sure yes (the supervisor and
   its MCP server live in the parent weave process, not in the session
   subprocess), but the implementation should add a regression test.

3. **`outcome="partial"` worth distinguishing from `"answered"`?** I
   conflated them above; a model might want to re-ask only the declined
   ones. Easy to add later by checking `answers[i].declined` per question.

4. **Should weave's own questions be allowed when no children exist?**
   I.e. weave-asking-the-user, the most common case. Yes — orthogonal to
   anything in the queue manager. Calling it out for the docs.

5. **camelCase vs snake_case in the tool schema** — the safest move is
   accept both, document snake_case as canonical. Want to confirm.

6. **Tool description / agent prompting.** The tool description text needs
   to instruct agents: "use this when you have a discrete decision the
   human should make; don't use it for general clarification — use
   `send_async` to weave for that." Phrasing TBD; suggest dmotles writes
   the final copy.

7. **First-class deprecation of harness `AskUserQuestion`.** Per the
   prompt this is a separate task — drop `"AskUserQuestion"` from
   `internal/rootinit/tools.go:14`. Worth confirming whether we drop it
   immediately on the same release as the MCP tool, or one release later
   to give weave's prompt a chance to migrate. (My opinion: drop
   immediately, and add a system-prompt hint pointing to the new tool.)

---

## Risk register

| # | Risk | Likelihood | Impact | Mitigation |
|---|------|-----------|--------|-----------|
| R1 | Queue leak when an agent's ctx is cancelled but no `Cancel` is wired | M | M | `AskQuestion` selects on `ctx.Done()` and removes itself from the queue under the lock |
| R2 | Modal preempts a more important modal (e.g. error dialog about a session crash) | L | M | Gate `QuestionsAvailableMsg` on `!showError && !showConfirm`, defer until those clear |
| R3 | User dismisses with Esc and forgets — agent blocked indefinitely | M | M | Persistent status-bar badge `🔔 weave is asking` + a "1m elapsed" reminder banner in the viewport every N minutes |
| R4 | Two modals queued; user answers first, second fails to auto-show | L | M | After `Resolve`, `QuestionModel.Reset()` then re-emit `QuestionsAvailableMsg{Depth: …}` from the answer-completion handler |
| R5 | Tests need a TUI test harness for the modal — none exists today for confirm/error dialog either | M | L | Pattern `confirm_test.go` + add a `tui_test.go` integration test that drives keys directly into `QuestionModel.Update` |
| R6 | huh-style keybinds users expect (Tab to advance fields) won't work — ours is enter-driven | L | L | Document keybinds in `internal/tui/help.go` |
| R7 | `multiSelect` casing inconsistency causes silent agent failure | M | M | Lenient decoder + integration test asserting both forms work |
| R8 | Long question text overflows the modal; lipgloss `MaxWidth` truncates | M | L | Add `bubbles/v2/viewport` inside the modal to allow scrolling for long questions |
| R9 | Concurrent `AskQuestion` from same agent (re-entrancy) | L | L | Allow it; FIFO is FIFO. Document that the model should not do this |
| R10 | TUI goroutine receives `QuestionsAvailableMsg` after AppModel quit (channel still alive) | L | L | Same `handoffDone` close pattern (`cmd/enter.go:566`) |

---

## Future-paging extension (NOT v1)

The end goal: when the user is away from the TUI, page them on Slack /
Telegram with the question, accept their reply remotely, surface it back
into the queue.

**Where to put the seam:** the `questionQueue` consumer is currently
"the TUI" via the `cmd/enter.go` forwarder goroutine. Generalize to:

```go
type QuestionConsumer interface {
    OnEnqueue(req *PendingQuestion)   // notify
    OnCancel(requestID string)        // notify
    Name() string
}

func (q *questionQueue) RegisterConsumer(c QuestionConsumer) error
```

Multiple consumers register; **all** receive enqueue/cancel notifications.
ANY consumer can call `Resolve(requestID, resp)` to win the race. This
gives us:
- TUI consumer (today's design)
- A Slack/Telegram bot consumer (future): listens for `OnEnqueue`, posts a
  message with inline buttons / numbered reply hints, and on user reply
  parses the reply and calls `Resolve`. Same shape as the TUI, just with a
  different transport.

The bot would live in a separate `internal/notifications/` package
(probably `internal/notifications/slack/`) and be wired in `cmd/enter.go`
behind a feature flag (`SPRAWL_REMOTE_PAGING_SLACK_TOKEN` env var).

Avoiding the corner-paint:
- Keep `QuestionRequest`, `QuestionResponse`, `Question` JSON-pure (already
  are; no Go-internal pointers).
- Make `RegisterConsumer` allow N consumers from day one even if only the
  TUI uses it.
- Store request metadata (`From`, `EnqueuedAt`, `Questions`) in a form
  that's easy to render to text/markdown (already true — Slack consumer
  just calls `json.MarshalIndent`).
- Don't bake the modal into `Resolve` — the TUI consumer dismisses its own
  modal in response to a `Resolve` notification.

That's it. The design above doesn't paint a corner.

---

## Reflections

**Surprising:** how clean the existing `handoff` precedent is — a single
buffered channel + a forwarder goroutine + a tea.Msg is genuinely all you
need. Same shape works for questions if you just stuff a per-call
response channel inside the message.

**Surprising^2:** `huh` doesn't drop in. I expected at most a small port;
the v1/v2 bubbletea split makes it materially worse than rolling our own.
Worth flagging to dmotles in case there's appetite for migrating bubbletea
v2 → newer stack in the future.

**Open in my mind:**
- Does the MCP request ctx really survive a `/handoff`? Worth a one-shot
  test before we start coding (spawn a child that calls a synthetic
  long-blocking MCP tool, fire `/handoff`, check whether the tool's ctx
  cancels).
- What does the agent's prompt look like to teach it the new tool's
  semantics? (Prompt-engineering question, not architecture.)
- Status-bar UX for "2 more queued" — does the existing `statusbar.go`
  have room? I didn't measure widths.

**If I had more time:**
- Would build a small E2E harness that scripts the full round trip
  (tool call → modal answer → response back to model) using a fake
  agent loop, similar to `scripts/test-handoff-e2e.sh`.
- Would test interaction with the existing `pendingSubmit` (queued user
  message) flow — should typing a queued message still work while the
  question modal is up? My instinct says no (modal blocks input), but
  the user might disagree.
- Would draft the actual tool description string and run it past dmotles.

---

*End of findings.*
