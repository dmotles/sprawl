# Unified Agent Runtime

**Status**: Proposed  
**Author**: ghost (researcher)  
**Date**: 2026-04-30  
**Tracking**: QUM-392

## 1. Problem Statement

Sprawl has two separate systems for running a Claude subprocess:

| Capability | Bridge (root/weave) | Runner (children) |
|---|---|---|
| Streams stdout to TUI | ✅ via `protocol.Message` → `tea.Msg` | ❌ writes to JSONL file, polled every 2s |
| Message queue drain between turns | ❌ no queue; relies on 2s tree-tick backstop | ✅ `ListPending` → `BuildQueueFlushPrompt` |
| Interrupt delivery (kill turn, drain, restart) | ❌ ESC sends `Interrupt()` but no queue drain | ✅ `ControlSignal` channel + poke/wake files |
| `report_status` notifies parent | ❌ weave has no parent runtime to notify | ✅ `ReportStatus` → `InterruptDelivery()` on parent |
| ESC interrupt from TUI | ✅ via `Bridge.Interrupt()` | ❌ no TUI connection |
| Crash recovery / resume | ✅ via `restartFunc` + `resumeFailureWindow` | ✅ via `restartWithResume()` |

This split means:

1. **Weave can't receive messages reliably.** The root agent relies on a 2s polling backstop (`AgentTreeMsg` → `peekAndDrainCmd`) to discover messages from children. There's no interrupt-on-arrival for the root session.
2. **Children can't stream output to the TUI.** Child output goes to `activity.ndjson` files read periodically; there's no real-time streaming path.
3. **No unified interrupt model.** The root uses TUI ESC → `Bridge.Interrupt()`, while children use `ControlSignal` channels. These are incompatible abstractions.

## 2. Current Architecture Deep Dive

### 2.1 Bridge Path (Root / Weave)

```
cmd/enter.go → newSessionImpl()
  ├── rootinit.Prepare() → PreparedSession (system prompt, session ID, resume decision)
  ├── backendclaude.NewAdapter().Start() → backend.Session
  ├── enterBridgeSession wraps backend.Session as tui.BridgeSession
  └── tui.NewBridge(ctx, bridgeSession) → *tui.Bridge

Bridge drives the TUI:
  Bridge.Initialize()     → SessionInitializedMsg
  Bridge.SendMessage(txt) → UserMessageSentMsg → Bridge.WaitForEvent() loop
  Bridge.WaitForEvent()   → reads from <-chan *protocol.Message
                          → mapProtocolMessage() → AssistantTextMsg / ToolCallMsg / SessionResultMsg
  Bridge.Interrupt()      → InterruptResultMsg
```

**Key insight**: The Bridge is a thin adapter that converts `backend.Session`'s channel-based event stream into Bubble Tea `tea.Msg` types. It has NO message queue awareness. The root agent receives messages via `peekAndDrainCmd` on the 2s `AgentTreeMsg` tick, which builds `InboxDrainMsg` and feeds it into the next prompt when idle.

### 2.2 Runner Path (Children)

```
supervisor/runtime_launcher.go → inProcessRuntimeStarter.Start()
  ├── buildRunnerDeps(spec) → RunnerDeps (env, file I/O, process factory)
  ├── agentloop.StartRunner(ctx, deps, name) → *Runner
  │     ├── Loads agent state, writes system prompt
  │     ├── StartBackendProcess() or startProcess() → ProcessManager
  │     └── Returns Runner with initialPrompt set
  └── go runner.Run(ctx)  ← runs in background goroutine

Runner.Run() loop:
  1. Send initial prompt with interrupt support
  2. Loop forever:
     a. Check kill sentinel, agent state
     b. Acquire work lock
     c. Check poke file → send poke message
     d. Check task queue → send task prompt
     e. Check interrupt queue → BuildInterruptFlushPrompt()
     f. Check async queue → BuildQueueFlushPrompt()
     g. Check inbox messages → build "read with sprawl messages read" prompt
     h. Check wake file → send wake prompt
     i. Release lock, sleep 3s, repeat
```

**Key insight**: The Runner is a full agent loop that manages prompt injection between turns. It wraps `ProcessManager.SendPrompt()` with `SendPromptWithInterrupt()` which polls for interrupts/pokes/wakes during a turn. Output goes to `ObserverWriter` → log file + `ActivityRing` → `activity.ndjson`. No connection to TUI.

### 2.3 Supervisor / Runtime Registry

```
supervisor.Real owns:
  ├── RuntimeRegistry (map[string]*AgentRuntime)
  ├── RuntimeStarter (inProcessRuntimeStarter)
  └── Per-agent: AgentRuntime { handle: runnerHandle, snapshot: RuntimeSnapshot }

runnerHandle wraps:
  ├── runner: *agentloop.Runner
  ├── controlCh: chan ControlSignal
  ├── done: chan struct{} (closed when runner.Run exits)
  └── Methods: Interrupt, Wake, InterruptDelivery, Stop
```

### 2.4 Backend Session Layer

Both paths share the same `backend.Session` underneath:
- `backend.session.StartTurn()` sends a user message and returns `<-chan *protocol.Message`
- `backend.session.readTurn()` reads from transport, handles MCP control requests inline, forwards events
- `backend.session.Interrupt()` sends a control_request with subtype "interrupt"
- Identity propagation via `backend.WithCallerIdentity(ctx, identity)` for MCP tool calls

## 3. Unified Runtime Design

### 3.1 Core Abstraction: `UnifiedRuntime`

The key insight is that both Bridge and Runner need the same underlying capabilities — they just expose them differently. The unified runtime should:

1. Own a `backend.Session` (the Claude subprocess)
2. Provide a real-time event stream (for TUI rendering)
3. Manage a between-turns message queue (for injecting messages as prompts)
4. Support interrupt delivery (kill current turn, drain queue, restart)
5. Emit lifecycle events (started, stopped, turn complete) for parent notification

```go
package runtime

// UnifiedRuntime manages a Claude subprocess with full lifecycle support.
// It replaces both tui.Bridge (root) and agentloop.Runner (children).
type UnifiedRuntime struct {
    session     backend.Session
    config      RuntimeConfig
    eventBus    *EventBus       // fan-out to multiple subscribers
    queue       *MessageQueue   // between-turns message queue
    loop        *TurnLoop       // manages prompt injection cycle
    mu          sync.RWMutex
    state       RuntimeState
}

// RuntimeConfig configures a unified runtime instance.
type RuntimeConfig struct {
    Name        string
    SprawlRoot  string
    SessionSpec backend.SessionSpec
    InitSpec    backend.InitSpec
    IsRoot      bool  // controls whether to run autonomous loop or wait for TUI input
    Observer    backend.Observer // optional: activity ring, log writer
}

// RuntimeState tracks the current lifecycle state.
type RuntimeState int

const (
    StateIdle RuntimeState = iota
    StateTurnActive
    StateInterrupting
    StateStopped
)
```

### 3.2 Event Bus: Real-Time Streaming for All Agents

The event bus replaces the Bridge's direct `<-chan *protocol.Message` with a fan-out model. This means the TUI can subscribe to ANY agent's event stream — not just root.

```go
// EventBus fans out protocol events to multiple subscribers.
type EventBus struct {
    mu          sync.RWMutex
    subscribers map[int]chan RuntimeEvent
    nextID      int
}

// RuntimeEvent wraps a protocol message with runtime metadata.
type RuntimeEvent struct {
    Type    RuntimeEventType
    Message *protocol.Message  // nil for lifecycle events
    Prompt  string             // set for TurnStarted events
    Result  *protocol.ResultMessage // set for TurnCompleted events
    Error   error              // set for TurnFailed events
}

type RuntimeEventType int

const (
    EventProtocolMessage RuntimeEventType = iota
    EventTurnStarted
    EventTurnCompleted
    EventTurnFailed
    EventInterrupted
    EventQueueDrained
    EventStopped
)

func (bus *EventBus) Subscribe(buffer int) (<-chan RuntimeEvent, func()) { ... }
func (bus *EventBus) Publish(event RuntimeEvent) { ... }
```

### 3.3 Message Queue: Unified Between-Turns Delivery

The queue replaces the Runner's file-based polling (poke files, wake files, `ListPending()`) with an in-memory queue backed by the existing on-disk persistence.

```go
// MessageQueue manages between-turns message injection.
type MessageQueue struct {
    mu       sync.Mutex
    items    []QueueItem
    signal   chan struct{} // signaled when items are added
}

type QueueItem struct {
    Class   string // "interrupt", "async", "inbox", "task"
    Prompt  string
    EntryIDs []string // for post-delivery cleanup
}

// Enqueue adds an item and signals the turn loop.
func (q *MessageQueue) Enqueue(item QueueItem) { ... }

// DrainAll returns all queued items, prioritized: interrupt > task > async > inbox.
func (q *MessageQueue) DrainAll() []QueueItem { ... }

// Signal returns the channel that fires when items arrive.
func (q *MessageQueue) Signal() <-chan struct{} { ... }
```

### 3.4 Turn Loop: The Heart of the Runtime

The turn loop is the key unification point. It replaces both:
- The Bridge's `WaitForEvent()` → `Update()` → `SendMessage()` cycle (TUI-driven)
- The Runner's `Run()` loop (autonomous)

The critical design decision: **the turn loop always runs autonomously**, checking for queued work between turns. For the root agent, user input is just another queue item. This means the root agent gets the same interrupt-delivery and message-drain capabilities as children.

```go
// TurnLoop manages the prompt → wait → drain → loop cycle.
type TurnLoop struct {
    runtime  *UnifiedRuntime
    config   TurnLoopConfig
}

type TurnLoopConfig struct {
    // InitialPrompt is sent as the first turn (children only).
    InitialPrompt string
    // AutoLoop controls whether the loop checks for queued work
    // between turns (true for children, true for root with queue-drain).
    AutoLoop bool
}

func (l *TurnLoop) Run(ctx context.Context) error {
    // 1. Send initial prompt if set
    if l.config.InitialPrompt != "" {
        l.executeTurn(ctx, l.config.InitialPrompt)
    }

    for {
        select {
        case <-ctx.Done():
            return nil
        default:
        }

        // 2. Check for queued work (interrupts, messages, tasks)
        items := l.runtime.queue.DrainAll()
        if len(items) > 0 {
            prompt := buildCompositePrompt(items)
            l.executeTurn(ctx, prompt)
            continue
        }

        // 3. Wait for new work or context cancellation
        select {
        case <-ctx.Done():
            return nil
        case <-l.runtime.queue.Signal():
            continue
        }
    }
}

func (l *TurnLoop) executeTurn(ctx context.Context, prompt string) {
    l.runtime.eventBus.Publish(RuntimeEvent{Type: EventTurnStarted, Prompt: prompt})

    events, err := l.runtime.session.StartTurn(ctx, prompt, ...)
    if err != nil {
        l.runtime.eventBus.Publish(RuntimeEvent{Type: EventTurnFailed, Error: err})
        return
    }

    // Stream events to subscribers AND check for interrupts
    for msg := range events {
        l.runtime.eventBus.Publish(RuntimeEvent{
            Type: EventProtocolMessage, Message: msg,
        })
        if msg.Type == "result" {
            var result protocol.ResultMessage
            _ = protocol.ParseAs(msg, &result)
            l.runtime.eventBus.Publish(RuntimeEvent{
                Type: EventTurnCompleted, Result: &result,
            })
        }
    }
}
```

### 3.5 TUI Integration: Bridge Becomes a Subscriber

The TUI no longer needs a special Bridge. Instead, it subscribes to the runtime's event bus and translates events to `tea.Msg` types:

```go
// TUIAdapter subscribes to a UnifiedRuntime and produces tea.Cmds.
type TUIAdapter struct {
    runtime *UnifiedRuntime
    events  <-chan RuntimeEvent
    cancel  func()
}

func NewTUIAdapter(rt *UnifiedRuntime) *TUIAdapter {
    events, cancel := rt.EventBus().Subscribe(100)
    return &TUIAdapter{runtime: rt, events: events, cancel: cancel}
}

// WaitForEvent returns a tea.Cmd that reads the next event.
func (a *TUIAdapter) WaitForEvent() tea.Cmd {
    return func() tea.Msg {
        event := <-a.events
        return mapRuntimeEvent(event) // → AssistantTextMsg, ToolCallMsg, etc.
    }
}

// SendMessage enqueues a user message into the runtime's queue.
func (a *TUIAdapter) SendMessage(text string) tea.Cmd {
    return func() tea.Msg {
        a.runtime.Queue().Enqueue(QueueItem{
            Class:  "user",
            Prompt: text,
        })
        return UserMessageSentMsg{}
    }
}

// Interrupt requests an interrupt on the current turn.
func (a *TUIAdapter) Interrupt() tea.Cmd {
    return func() tea.Msg {
        err := a.runtime.Interrupt(context.Background())
        return InterruptResultMsg{Err: err}
    }
}
```

This means the TUI can subscribe to ANY agent's runtime — not just root. When the user switches to observing a child agent, the TUI swaps its subscription:

```go
// In AppModel.cycleAgent():
func (m *AppModel) observeAgent(name string) {
    if m.currentAdapter != nil {
        m.currentAdapter.Cancel()
    }
    if rt, ok := m.runtimeRegistry.Get(name); ok {
        m.currentAdapter = NewTUIAdapter(rt)
        // Start streaming events from this agent
    }
}
```

### 3.6 Interrupt Model

The unified runtime provides a single interrupt API that works for both TUI-initiated (ESC) and programmatic (parent→child) interrupts:

```go
func (rt *UnifiedRuntime) Interrupt(ctx context.Context) error {
    rt.mu.Lock()
    if rt.state != StateTurnActive {
        rt.mu.Unlock()
        return nil // no-op when idle
    }
    rt.state = StateInterrupting
    rt.mu.Unlock()

    err := rt.session.Interrupt(ctx)
    rt.eventBus.Publish(RuntimeEvent{Type: EventInterrupted})
    return err
}

// InterruptDelivery is the same as Interrupt but additionally signals the
// queue that new work arrived. Used by the supervisor when a parent sends
// a message to a child.
func (rt *UnifiedRuntime) InterruptDelivery(ctx context.Context) error {
    if err := rt.Interrupt(ctx); err != nil {
        return err
    }
    rt.queue.Signal() // wake the turn loop
    return nil
}
```

### 3.7 Root Agent: How It Changes

Today, the root agent's turn loop is driven by the TUI (user types → `SubmitMsg` → `Bridge.SendMessage()` → `WaitForEvent()` loop). With the unified runtime, the root agent runs the same autonomous loop as children, but with one key difference: **user input arrives via the message queue** rather than direct prompts.

The flow becomes:
1. User types in TUI → `SubmitMsg` → `runtime.Queue().Enqueue({Class: "user", Prompt: text})`
2. Turn loop wakes, drains queue, sends prompt
3. Events stream back through event bus → TUI adapter → `tea.Msg` types
4. When a child calls `report_status`, supervisor calls `runtime.Queue().Enqueue({Class: "interrupt", ...})` on the parent
5. Between turns, the loop drains the queue — root gets child notifications injected automatically

This eliminates the 2s polling backstop and gives the root agent the same interrupt-delivery guarantees as children.

### 3.8 How `report_status` Parent Notification Works

The existing flow in `supervisor.Real.ReportStatus()` already does:
1. Persist report to agent state
2. Look up parent's runtime
3. If parent has started runtime → `InterruptDelivery()`

With the unified runtime, this works identically for root and children:
- If parent is a mid-tier agent (child with its own children): `InterruptDelivery()` → interrupt current turn → queue drain → child sees report
- If parent is root (weave): `InterruptDelivery()` → interrupt current turn (if any) → queue drain → weave sees report in next prompt

The only change is that root now HAS a runtime with an `InterruptDelivery()` method, so the supervisor can deliver to it the same way.

## 4. Interface Surface

```go
// UnifiedRuntime is the public API.
type UnifiedRuntime interface {
    // Lifecycle
    Start(ctx context.Context) error
    Stop(ctx context.Context) error
    State() RuntimeState

    // Turn management
    Interrupt(ctx context.Context) error
    InterruptDelivery(ctx context.Context) error

    // Queue (for injecting messages/tasks between turns)
    Queue() *MessageQueue

    // Event streaming (for TUI and observers)
    EventBus() *EventBus

    // Identity
    Name() string
    SessionID() string
    Capabilities() backend.Capabilities
}
```

## 5. Testing Strategy

### 5.1 Unit Tests with Mock Backend

The primary testing approach uses a mock `backend.Session` that simulates the Claude subprocess without actually running one:

```go
type MockSession struct {
    turnResponses []protocol.Message
    initErr       error
    turnErr       error
    interrupted   bool
}

func (m *MockSession) StartTurn(ctx context.Context, prompt string, spec ...backend.TurnSpec) (<-chan *protocol.Message, error) {
    ch := make(chan *protocol.Message, len(m.turnResponses))
    for _, msg := range m.turnResponses {
        ch <- &msg
    }
    close(ch)
    return ch, m.turnErr
}
```

Test cases:
- **Turn lifecycle**: start turn → stream events → result → turn complete
- **Queue drain**: enqueue 3 messages → turn completes → next turn receives composite prompt
- **Interrupt delivery**: mid-turn interrupt → turn stops → queue drain → next turn
- **Event bus fan-out**: 3 subscribers → all receive same events
- **Priority ordering**: interrupt > task > async > inbox messages
- **Crash recovery**: session returns error → runtime attempts resume

### 5.2 Integration Tests with Real Claude

A test harness that spawns a real `claude` subprocess to validate the full stack:

```go
func TestUnifiedRuntime_RealClaude(t *testing.T) {
    if _, err := exec.LookPath("claude"); err != nil {
        t.Skip("claude binary not found")
    }

    rt := NewUnifiedRuntime(RuntimeConfig{
        Name:       "test-agent",
        SprawlRoot: t.TempDir(),
        SessionSpec: backend.SessionSpec{
            WorkDir:        t.TempDir(),
            Model:          "sonnet",
            PermissionMode: "bypassPermissions",
        },
    })

    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()

    // Subscribe to events
    events, unsub := rt.EventBus().Subscribe(100)
    defer unsub()

    // Start runtime
    require.NoError(t, rt.Start(ctx))
    defer rt.Stop(ctx)

    // Send a message
    rt.Queue().Enqueue(QueueItem{
        Class:  "user",
        Prompt: "Say exactly: HELLO_TEST_123",
    })

    // Verify streaming works
    var gotText bool
    for event := range events {
        if event.Type == EventProtocolMessage && event.Message.Type == "assistant" {
            gotText = true
        }
        if event.Type == EventTurnCompleted {
            break
        }
    }
    assert.True(t, gotText, "should have received assistant text events")

    // Test interrupt
    rt.Queue().Enqueue(QueueItem{
        Class:  "user",
        Prompt: "Write a very long essay about the history of computing",
    })
    time.Sleep(2 * time.Second) // let it start streaming
    require.NoError(t, rt.Interrupt(ctx))
    // Verify turn ends quickly after interrupt
}
```

### 5.3 Testing Order

1. **Phase 1**: Unit tests with mocks (no real subprocess needed)
   - Event bus subscribe/unsubscribe/fan-out
   - Message queue enqueue/drain/priority
   - Turn loop: idle → active → complete → idle
   - Interrupt: active → interrupting → idle
   - Crash recovery: turn error → restart with resume

2. **Phase 2**: Integration tests with real claude
   - Basic turn: send prompt → receive events → result
   - Interrupt: start long turn → interrupt → verify stop
   - Queue drain: enqueue during turn → verify delivery after turn
   - MCP tool bridge: verify MCP tools work through unified runtime

3. **Phase 3**: TUI adapter tests
   - Mock runtime → verify tea.Msg production
   - Agent switching → verify subscription swap
   - ESC interrupt → verify forwarding

## 6. Migration Plan

### 6.1 Phase 1: Build and Test Standalone (1-2 days)

Create `internal/runtime/` package with:
- `unified.go` — core runtime
- `eventbus.go` — event fan-out
- `queue.go` — message queue
- `turnloop.go` — turn management
- `tuiadapter.go` — TUI bridge replacement
- Full unit test coverage

This phase has zero production impact — it's a new package with no callers.

### 6.2 Phase 2: Migrate Children First (1-2 days)

**Why children first**: Children are simpler (no TUI interaction, no restartFunc), and their current Runner is already close to what the unified runtime provides. The risk surface is smaller.

Steps:
1. Make `inProcessRuntimeStarter` create a `UnifiedRuntime` instead of calling `agentloop.StartRunner`
2. The `runnerHandle` wraps a `UnifiedRuntime` instead of a `*agentloop.Runner`
3. Verify: `make test`, smoke test with real spawned agents
4. The TUI's activity panel for children now subscribes to the event bus (real-time streaming!) instead of polling `activity.ndjson`

**Rollback**: Revert to `agentloop.StartRunner` wrapper. The `RuntimeHandle` interface doesn't change, so the supervisor is unaffected.

### 6.3 Phase 3: Migrate Root (1-2 days)

**Why root second**: The root has more complexity (TUI integration, restartFunc, resume-failure handling, handoff). Doing it second means we've already proven the runtime works on children.

Steps:
1. In `cmd/enter.go`, replace `newSessionImpl` / `tui.Bridge` with a `UnifiedRuntime` + `TUIAdapter`
2. User input goes through `runtime.Queue().Enqueue()` instead of `Bridge.SendMessage()`
3. `peekAndDrainCmd` is replaced by the runtime's autonomous queue drain
4. `InboxArrivalMsg` feeds into `runtime.Queue()` instead of triggering a separate drain path
5. `restartFunc` recreates the `UnifiedRuntime` instead of the Bridge
6. Verify: full E2E tests (`make test-notify-tui-e2e`, `make test-handoff-e2e`)

**Rollback**: Revert to Bridge-based flow. The TUI adapter is a drop-in replacement for Bridge.

### 6.4 Phase 4: Cleanup (1 day)

1. Remove `agentloop.Runner` and `agentloop.SendPromptWithInterrupt`
2. Remove `tui.Bridge` and `tui.BridgeSession`
3. Remove poke/wake file polling (replaced by in-memory queue + `ControlSignal`)
4. Update `docs/` and `CLAUDE.md`

### 6.5 Risk Assessment

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Root agent turn loop doesn't feel as responsive | Medium | High | User input bypasses queue priority; immediate send path |
| Interrupt timing changes break tests | Medium | Medium | Keep `SendPromptWithInterrupt` polling as fallback |
| MCP tool bridge threading breaks | Low | High | Identity propagation unchanged; same `backend.Session` |
| Resume/handoff cycle breaks | Medium | High | Phase 3 runs all E2E tests before merge |
| Activity ring / transcript tailing regresses | Low | Medium | Event bus replaces polling; test both paths |

## 7. Key Design Decisions

### 7.1 Why Not Just Add a Queue to Bridge?

The Bridge is deliberately thin — it's a Bubble Tea adapter, not a runtime. Adding queue management, interrupt delivery, and autonomous loop logic to Bridge would make it a second Runner. Better to have one runtime that both root and children use.

### 7.2 Why Not Just Add Streaming to Runner?

The Runner writes to an `io.Writer` (log file + activity ring). Adding TUI streaming would require either (a) the Runner knowing about Bubble Tea (bad coupling) or (b) a pub-sub layer between Runner and TUI. Option (b) is essentially the event bus proposed here — at which point you might as well build the unified runtime.

### 7.3 User Input: Queue vs Direct Send

A concern with routing user input through the queue is latency — the user types, the message goes into a queue, the turn loop picks it up. In practice this is negligible (<1ms) because the turn loop is either (a) blocked on `queue.Signal()` and wakes immediately, or (b) finishing a turn and will drain on the next iteration.

However, to preserve the current "type and see immediate response" feel, user input should be highest priority in the queue drain and, when the runtime is idle, triggers an immediate wake.

### 7.4 Preserving the Observer Pattern

The existing `Observer` interface (`OnMessage(*protocol.Message)`) is used by the `ObserverWriter` for logging and `ActivityRing` for the activity panel. The unified runtime should continue to support this — the event bus calls `observer.OnMessage()` in addition to publishing to subscribers. This preserves backward compatibility with the activity file format.

## 8. Appendix: File Inventory

### Files to Create
- `internal/runtime/unified.go`
- `internal/runtime/eventbus.go`
- `internal/runtime/queue.go`
- `internal/runtime/turnloop.go`
- `internal/tuiruntime/tuiadapter.go` (split out of `internal/runtime` in QUM-431)
- `internal/runtime/*_test.go`

### Files to Modify (Phase 2 — Children)
- `internal/supervisor/runtime_launcher.go` — swap `agentloop.StartRunner` for `runtime.New`
- `internal/supervisor/runtime.go` — `RuntimeHandle` wraps `runtime.UnifiedRuntime`

### Files to Modify (Phase 3 — Root)
- `cmd/enter.go` — replace Bridge with TUIAdapter + UnifiedRuntime
- `internal/tui/app.go` — accept TUIAdapter instead of Bridge
- `cmd/enter_notify.go` — feed InboxArrivalMsg into runtime queue

### Files to Remove (Phase 4)
- `internal/tui/bridge.go` (replaced by TUIAdapter)
- `internal/agentloop/runner.go` (replaced by UnifiedRuntime turn loop)
- `internal/agentloop/runner_backend.go` (`claudeBackendProcess` → unified runtime uses `backend.Session` directly)

### Files Preserved
- `internal/backend/session.go` — unchanged, used by unified runtime
- `internal/backend/claude/adapter.go` — unchanged, creates backend.Session
- `internal/agentloop/process.go` — `Observer` interface reused
- `internal/agentloop/queue.go` — on-disk queue persistence reused
- `internal/supervisor/real.go` — supervisor delegates to runtime, minimal changes

## 9. Forward-Compat Requirement: TDD Sub-Agents (QUM-408)

**The unified runtime engineer spawn path MUST pass `--agents <json>` to claude
for engineer agents.** This wires Claude Code's `Agent` tool with the curated
TDD sub-agent set (`oracle`, `test-writer`, `test-critic`, `implementer`,
`code-reviewer`, `qa-validator`) defined in `internal/agent/subagents.go`.

### Contract

- The current spawn path (`agentloop.BuildAgentSessionSpec`) populates
  `backend.SessionSpec.Agents` with `agent.TDDSubAgentsJSON()` if and only if
  `agentState.Type == "engineer"`.
- The Claude adapter (`internal/backend/claude/adapter.go`) threads
  `SessionSpec.Agents` into `claudecli.LaunchOpts.Agents`, which emits the
  `--agents <json>` argv pair.
- Researchers, managers, and weave do NOT receive the flag — they have
  different roles and should not run the engineer TDD workflow.

### When migrating to UnifiedRuntime

When the spawn path is rewritten as part of QUM-396 / QUM-398:

1. Continue to populate `backend.SessionSpec.Agents` (or its successor field)
   from `agent.TDDSubAgentsJSON()` for engineer agents.
2. Preserve the `agentState.Type == "engineer"` gate. Do not broaden it.
3. Preserve the regression test
   `TestBuildAgentSessionSpec_AgentsByAgentType` (or port it to the unified
   runtime equivalent), which asserts engineer specs carry the TDD JSON and
   non-engineer specs do not.

### History

The wiring was originally added in `f4546ab` and dropped during the in-process
agent-loop refactor (`ce30c36`). It was restored in QUM-408. dmotles confirmed
engineer outcomes were stronger when these sub-agents were available; do not
re-regress.
