package supervisor

import (
	"context"
	"time"

	"github.com/dmotles/sprawl/internal/agent"
	"github.com/dmotles/sprawl/internal/agentloop"
	"github.com/dmotles/sprawl/internal/agentops"
	"github.com/dmotles/sprawl/internal/state"
)

// MergeOutcome re-exports agentops.MergeOutcome for supervisor consumers. See
// QUM-511 / QUM-489: callers (notably the MCP toolMerge handler) need to
// distinguish a real merge from a no-op (zero new commits) so they can
// surface the truth instead of flattening to a generic success message.
type MergeOutcome = agentops.MergeOutcome

// AgentInfo describes an agent's current state as seen by the supervisor.
type AgentInfo struct {
	Name              string  `json:"name"`
	Type              string  `json:"type"`
	Family            string  `json:"family"`
	Parent            string  `json:"parent"`
	Status            string  `json:"status"`
	Branch            string  `json:"branch"`
	TreePath          string  `json:"tree_path,omitempty"`
	LastReportType    string  `json:"last_report_type,omitempty"`
	LastReportState   string  `json:"last_report_state,omitempty"`
	LastReportMessage string  `json:"last_report_message,omitempty"`
	LastReportDetail  string  `json:"last_report_detail,omitempty"`
	TotalCostUsd      float64 `json:"total_cost_usd,omitempty"`
	ProcessAlive      *bool   `json:"process_alive"`
	// SubprocessAlive is the ground-truth "is a live RuntimeHandle attached"
	// boolean. Distinct from ProcessAlive (the liveness-projection field):
	// after QUM-727, the two should agree in steady state.
	SubprocessAlive bool `json:"subprocess_alive"`
	// EventbusSubscribed is true iff the runtime's per-agent EventBus has at
	// least one live subscriber. QUM-727 invariant: terminal-Status agents
	// (stopped/faulted/killed/retired) must read false here.
	EventbusSubscribed bool `json:"eventbus_subscribed"`
	// EventbusSubCount is the exact subscriber count, surfaced for debugging
	// fan-out load. Omitted when zero.
	EventbusSubCount int       `json:"eventbus_sub_count,omitempty"`
	InTurn           bool      `json:"in_turn"`
	LastActivityAt   time.Time `json:"last_activity_at,omitempty"`
	// Liveness is the unified-projection token (QUM-722) for this agent.
	Liveness string `json:"liveness"`
	// Subagent indicates the agent shares its parent's worktree/branch. QUM-709.
	Subagent bool `json:"subagent,omitempty"`
	// SharedWorktreeWith is the parent agent name when Subagent is true. QUM-709.
	SharedWorktreeWith string `json:"shared_worktree_with,omitempty"`
}

// PauseOptions configures a Supervisor.Pause call. QUM-722.
type PauseOptions struct {
	Timeout time.Duration
	Cascade bool
}

// PauseResult is returned by Supervisor.Pause. Outcome is one of:
// "paused" (clean) or "escalated_to_kill" (timeout escalation). WaitMs
// records elapsed wall time spent in the pause flow. Cascade lists the
// descendant agent names walked when Cascade=true. QUM-722.
type PauseResult struct {
	Outcome string   `json:"outcome"`
	WaitMs  int64    `json:"wait_ms"`
	Cascade []string `json:"cascade,omitempty"`
}

// SendMessageResult is returned by Supervisor.SendMessage. The canonical
// QUM-550 send_message tool replaces send_async + send_interrupt; this
// result struct collapses their two distinct return shapes into one.
//
// Interrupted is best-effort: true iff interrupt=true was honored at the
// supervisor layer. The recipient runtime may still observe the interrupt
// asynchronously (see QUM-549 for the MCP-tool-wait blind spot).
type SendMessageResult struct {
	MessageID   string `json:"message_id"`
	QueuedAt    string `json:"queued_at"` // RFC3339
	Interrupted bool   `json:"interrupted"`
}

// LastReport is the structured last_report_* block from an agent's state.
type LastReport struct {
	Type    string `json:"type,omitempty"`
	Message string `json:"message,omitempty"`
	At      string `json:"at,omitempty"`
	State   string `json:"state,omitempty"`  // working, blocked, complete, failure
	Detail  string `json:"detail,omitempty"` // long-form detail, optional
}

// ReportStatusResult is returned by Supervisor.ReportStatus.
type ReportStatusResult struct {
	ReportedAt string `json:"reported_at"` // RFC3339
}

// PeekResult is returned by Supervisor.Peek. See
// docs/designs/messaging-overhaul.md §4.2.4.
type PeekResult struct {
	Status     string                    `json:"status"`
	LastReport LastReport                `json:"last_report"`
	Activity   []agentloop.ActivityEntry `json:"activity"`
	// InTurn reflects backend.Session.InTurn() for the
	// target agent's registered runtime (QUM-585). False when no runtime is
	// registered or the handle doesn't surface this signal.
	InTurn bool `json:"in_turn"`
	// Liveness is the unified-projection token (QUM-722).
	Liveness string `json:"liveness"`
	// Subagent indicates the agent shares its parent's worktree/branch. QUM-709.
	Subagent bool `json:"subagent,omitempty"`
	// SharedWorktreeWith is the parent agent name when Subagent is true. QUM-709.
	SharedWorktreeWith string `json:"shared_worktree_with,omitempty"`
}

// MessageSummary is a compact listing entry for a message in the caller's mailbox.
// See docs/designs/messaging-overhaul.md — returned by Supervisor.MessagesList
// and MessagesPeek.
type MessageSummary struct {
	ID        string `json:"id"`      // short ID if available, otherwise full ID
	FullID    string `json:"full_id"` // canonical maildir ID
	From      string `json:"from"`
	Subject   string `json:"subject"`
	Timestamp string `json:"timestamp"` // RFC3339
	Read      bool   `json:"read"`      // true iff sitting in cur/ (or archive/)
	Dir       string `json:"dir"`       // "new" | "cur" | "archive"
}

// MessagesListResult is returned by Supervisor.MessagesList.
type MessagesListResult struct {
	Agent    string           `json:"agent"`
	Filter   string           `json:"filter"`
	Count    int              `json:"count"`
	Messages []MessageSummary `json:"messages"`
}

// MessagesReadResult is returned by Supervisor.MessagesRead.
type MessagesReadResult struct {
	ID        string `json:"id"`
	FullID    string `json:"full_id"`
	From      string `json:"from"`
	To        string `json:"to"`
	Subject   string `json:"subject"`
	Body      string `json:"body"`
	Timestamp string `json:"timestamp"`
	Dir       string `json:"dir"`        // dir after auto-mark-read (cur/archive/sent)
	WasUnread bool   `json:"was_unread"` // true iff found in new/ at read time
}

// MessagesArchiveResult is returned by Supervisor.MessagesArchive.
type MessagesArchiveResult struct {
	ID       string `json:"id"`
	FullID   string `json:"full_id"`
	Archived bool   `json:"archived"`
}

// MessagesArchiveAllResult is returned by Supervisor.MessagesArchiveAll.
type MessagesArchiveAllResult struct {
	ArchivedCount int  `json:"archived_count"`
	Archived      bool `json:"archived"`
}

// MessagesPeekResult is returned by Supervisor.MessagesPeek. Provides a cheap
// "do I have mail?" summary without requiring a full list.
type MessagesPeekResult struct {
	Agent       string           `json:"agent"`
	UnreadCount int              `json:"unread_count"`
	Preview     []MessageSummary `json:"preview"` // up to 5, newest-first
}

// SpawnRequest holds parameters for spawning a new agent.
type SpawnRequest struct {
	Family   string `json:"family"`
	Type     string `json:"type"`
	Prompt   string `json:"prompt"`
	Branch   string `json:"branch"`
	Subagent bool   `json:"subagent,omitempty"`
}

// WakeResult is returned by Supervisor.Wake. Mode is one of:
//   - "resumed" — the prior claude session was successfully resumed via
//     --resume <session-id>; transcript continuity preserved.
//   - "fresh"   — the resume path failed (cookie rejected or post-start health
//     probe failed); the agent is back online via a freshly-minted session.
//
// SessionRestored mirrors Mode (true iff "resumed") as an explicit boolean for
// callers that branch on session continuity. QUM-724.
type WakeResult struct {
	Mode            string `json:"mode"`
	SessionRestored bool   `json:"session_restored"`
}

// Supervisor manages agent lifecycle. All methods are safe for concurrent use.
type Supervisor interface {
	Spawn(ctx context.Context, req SpawnRequest) (*AgentInfo, error)
	Status(ctx context.Context) ([]AgentInfo, error)
	// Delegate enqueues a task for agentName. When wakeIfOffline is true and
	// the target's projected liveness is offline-but-recoverable
	// ({Paused,Killed,Died,Faulted,ResumeFailed}), the supervisor wakes the
	// agent and threads a delegate-flavored RestartInjection (built via
	// agent.BuildWakePrompt(WakeReasonDelegate, ...)) so the recipient's
	// first post-wake turn sees the task as a hard task redirect. QUM-726.
	Delegate(ctx context.Context, agentName, task string, wakeIfOffline bool) error
	// Merge merges agentName's branch up to its parent. `caller` is the
	// agent identity invoking this operation — used to override
	// SPRAWL_AGENT_IDENTITY in the per-call agentops deps so child-agent MCP
	// calls don't leak the supervisor process's identity (always "weave")
	// into the parent-equality check inside agentops.Merge. See QUM-487.
	// An empty caller falls back to backendpkg.CallerIdentity(ctx) and then
	// to the supervisor's own callerName.
	Merge(ctx context.Context, caller, agentName, message string, noValidate bool) (*MergeOutcome, error)
	// Retire retires agentName. `caller` semantics match Merge — see QUM-487.
	Retire(ctx context.Context, caller, agentName string, merge, abandon, cascade, noValidate bool) error
	Kill(ctx context.Context, agentName string) error
	// Pause politely stops the named agent at its next turn boundary,
	// preserving the transcript so the agent can be `wake`d later. Escalates
	// to a hard kill after PauseOptions.Timeout. When Cascade is true, all
	// descendants are paused first (children before parent). QUM-722.
	Pause(ctx context.Context, agentName string, opts PauseOptions) (*PauseResult, error)
	// Wake brings an offline agent back online. The accept-set is the union
	// of liveness states that present as "offline-but-recoverable":
	// {Faulted, ResumeFailed, Paused, Killed, Died}. Wake first attempts to
	// resume the prior claude session via --resume <session-id>; on cookie
	// rejection or post-start health-probe failure, it falls back once to a
	// fresh session. Returns ErrWakeNotNeeded when the live handle is still
	// healthy (no-op success). QUM-724 (renamed from Recover/QUM-601 with
	// expanded scope).
	// Wake brings agentName online via the standard wake path. QUM-726
	// extends the signature: reason names why the wake was issued and
	// injectedBody carries the message-body / task-prompt to embed in the
	// RestartInjection via agent.BuildWakePrompt. WakeReasonBare with empty
	// body preserves the old behavior — no payload, just the neutral resume
	// notice.
	Wake(ctx context.Context, agentName string, reason agent.WakeReason, injectedBody string) (*WakeResult, error)
	// InduceTerminalFault forces the named agent's backend session into the
	// terminally-faulted state with the supplied sentinel error (one of
	// backend.ErrSubscriberWedged / backend.ErrHangTimeout, or any error).
	// This is a test-only seam used by the QUM-606 live-recover e2e harness
	// and is exposed via the build-tag-gated `_test_induce_wedge` MCP tool.
	// Production callers MUST NOT invoke this — it bypasses the real fault
	// detectors.
	InduceTerminalFault(ctx context.Context, agentName string, err error) error
	// RecoverAgents iterates all persisted agents under this caller and
	// attempts to resume those in {suspended, active, running} via
	// AgentRuntime.StartResume. Skips the caller itself, missing worktrees,
	// and agents in terminal lifecycle states ({killed, retired, done}).
	// Walks the tree BFS-from-caller so parents are started before their
	// children. Per-agent failures are isolated: an error launching one
	// agent does not abort the loop. Returns (resumed, failed, errs) where
	// len(errs) == failed. QUM-372.
	RecoverAgents(ctx context.Context) (resumed, failed int, errs []error)
	Shutdown(ctx context.Context) error

	// Handoff persists a session summary (marked Handoff=true) for the
	// current weave session and writes the handoff-signal file consumed by
	// FinalizeHandoff. On success, it fires the HandoffRequested channel so
	// a host (e.g. the TUI) can tear down and restart the current session.
	// Returns an error for empty summaries or when session state is missing.
	Handoff(ctx context.Context, summary string) error

	// HandoffRequested returns a channel that receives one value each time
	// Handoff completes successfully. Consumers use it to trigger session
	// restart without blocking the MCP tool response.
	HandoffRequested() <-chan struct{}

	// PeekActivity returns up to `tail` of the most recent activity
	// entries recorded for the named agent, oldest-first. See
	// docs/designs/messaging-overhaul.md §4.4. A missing agent (no
	// activity file yet) yields an empty slice and nil error.
	PeekActivity(ctx context.Context, agentName string, tail int) ([]agentloop.ActivityEntry, error)

	// SendMessage is the canonical messaging tool (QUM-550). When interrupt is
	// false, delivery is strictly cooperative (no Session.Interrupt). When true,
	// the recipient is preempted unconditionally and the message is enqueued at
	// the front of the queue (ClassInterrupt priority).
	// QUM-726: when wakeIfOffline is true and the recipient's projected
	// liveness is offline-but-recoverable, the supervisor wakes the
	// recipient and threads a send_message-flavored RestartInjection (built
	// via agent.BuildWakePrompt(WakeReasonSendMessage, ...)) before the
	// message is enqueued so the recipient's first post-wake turn sees the
	// body. When false, an offline target returns the canonical
	// "Delivery failed: agent <name> is <state>. Set wake_if_offline: true
	// to wake and deliver." error.
	SendMessage(ctx context.Context, to, body string, interrupt, wakeIfOffline bool) (*SendMessageResult, error)

	// Peek returns an agent's status, last report, and the tail of its
	// activity ring in one call. See §4.2.4.
	Peek(ctx context.Context, agentName string, tail int) (*PeekResult, error)

	// ReportStatus is the canonical status channel: persists the reporter's
	// LastReport* fields, flips Status for complete/failure, and delivers a
	// structured async notification to the reporter's parent. See
	// docs/designs/messaging-overhaul.md §4.2.3. The reporter identity is
	// the supervisor's caller (r.callerName) when agentName is empty.
	ReportStatus(ctx context.Context, agentName, state, summary string) (*ReportStatusResult, error)

	// MessagesList returns a listing of messages in the caller's mailbox.
	// Identity is the supervisor's callerName; agents cannot read other
	// agents' mailboxes via this API. `filter` ∈ {"", "all", "unread",
	// "read", "archived"} — "" is treated as "all". `limit` ≤ 0 returns all
	// matching messages; otherwise returns the newest-first top N. See
	// QUM-316.
	MessagesList(ctx context.Context, filter string, limit int) (*MessagesListResult, error)

	// MessagesRead returns the full body of a message by its ID (short or
	// long prefix accepted). If the message was in new/, it is auto-marked
	// read (moved to cur/). Scoped to the caller's mailbox.
	MessagesRead(ctx context.Context, msgID string) (*MessagesReadResult, error)

	// MessagesArchive moves a single message (by ID prefix) from new/ or
	// cur/ into archive/. Scoped to the caller's mailbox.
	MessagesArchive(ctx context.Context, msgID string) (*MessagesArchiveResult, error)

	// MessagesArchiveAll archives messages in bulk. When mode is "all",
	// archives everything in new/ + cur/. When mode is "read", archives
	// only read messages from cur/. Scoped to the caller's mailbox.
	MessagesArchiveAll(ctx context.Context, mode string) (*MessagesArchiveAllResult, error)

	// MessagesPeek returns the unread count and a small preview (up to 5,
	// newest-first) of the caller's inbox. Intended as a cheap "do I have
	// mail?" probe.
	MessagesPeek(ctx context.Context) (*MessagesPeekResult, error)

	// RuntimeRegistry returns the in-process registry of started child
	// runtimes. Callers use it to classify recipients (e.g. unified vs.
	// legacy) when routing messages. Out-of-process Supervisor
	// implementations may return nil; consumers must treat nil as "no
	// in-process runtimes known" and fall back accordingly. See QUM-438.
	RuntimeRegistry() *RuntimeRegistry

	// RegisterRootRuntime attaches a pre-built RuntimeHandle to the in-memory
	// runtime registry under the given name, marking it Started. Used by
	// cmd/enter.go (QUM-399) to register weave's UnifiedRuntime so children's
	// report_status / send_message WakeForDelivery / ForceInterruptDelivery
	// calls reach the root via the same registry mechanism that child runtimes
	// use.
	//
	// agentState is best-effort: when nil, implementations may load from disk
	// and fall back to a synthesized minimal state. Returns the registered
	// AgentRuntime.
	RegisterRootRuntime(name string, handle RuntimeHandle, agentState *state.AgentState) (*AgentRuntime, error)

	// --- Question queue (QUM-527 slice 1) ---
	// See docs/research/ask-user-question-mcp-design.md for the full design.

	// AskUserQuestion enqueues a question for human consumption and blocks
	// until a registered QuestionConsumer (e.g. the TUI) resolves it, the
	// caller's context is cancelled, the originating agent retires, or the
	// supervisor shuts down. req.RequestID must be non-empty; the caller
	// (typically the MCP tool dispatcher) generates a stable ID.
	AskUserQuestion(ctx context.Context, req QuestionRequest) (QuestionResponse, error)

	// RegisterQuestionConsumer attaches a consumer that is notified of every
	// enqueue and cancel. Returns an error on empty name or duplicate.
	RegisterQuestionConsumer(c QuestionConsumer) error

	// UnregisterQuestionConsumer removes a consumer by name. Idempotent.
	UnregisterQuestionConsumer(name string)

	// ResolveQuestion delivers resp to the question with the given ID. Returns
	// true on first successful resolution; false on duplicate / unknown ID.
	// Safe for concurrent callers — only one wins.
	ResolveQuestion(id string, resp QuestionResponse) bool

	// CancelQuestion cancels a pending question with OutcomeSessionEnded and
	// the supplied reason. Idempotent.
	CancelQuestion(id, reason string) bool

	// CancelByAgent cancels every pending question whose originating agent
	// matches agentName, using OutcomeAgentRetired and the supplied reason.
	// Called by Retire/Kill to release blocked AskUserQuestion callers.
	CancelByAgent(agentName, reason string)

	// QuestionsChanged returns a coalesced notification channel that emits on
	// every enqueue / resolve / cancel. Consumers use it to refresh their view
	// of the queue without polling.
	QuestionsChanged() <-chan struct{}

	// PeekQuestions returns (depth, shallow-copy-of-head). Head is nil when
	// the queue is empty.
	PeekQuestions() (int, *PendingQuestion)
}
