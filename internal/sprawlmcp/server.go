package sprawlmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	agentpkg "github.com/dmotles/sprawl/internal/agent"
	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/config"
	"github.com/dmotles/sprawl/internal/sprawlmcp/calllog"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/supervisor"
	"github.com/dmotles/sprawl/internal/tui"
)

// resolveAgentTarget reads the canonical "agent" key from the supplied
// values, falling back to the deprecated "agent_name" synonym (QUM-666).
// When only the deprecated key is populated, a structured slog warning
// is emitted naming the tool, the caller, and the deprecated key.
//
// Returns an error naming the canonical key 'agent' when neither value
// is populated. The error explicitly avoids the legacy misleading
// "must not be empty" phrasing so the caller can correct the key name.
//
// TODO(QUM-666): remove the agent_name fallback after one release of
// soak time so the LLM stops being trained on the deprecated key.
func resolveAgentTarget(agent, agentName, tool, caller string) (string, error) {
	if agent != "" {
		return agent, nil
	}
	if agentName != "" {
		slog.Warn("mcp.deprecated_parameter",
			"tool", tool,
			"caller", caller,
			"deprecated", "agent_name",
			"canonical", "agent")
		return agentName, nil
	}
	return "", fmt.Errorf("missing required field 'agent' (deprecated synonym 'agent_name' also accepted)")
}

// askUserQuestionRestrictedError is the canonical structured error text the
// tool returns when an ineligible caller (engineer or researcher) invokes
// ask_user_question. It is intentionally a JSON document so callers can
// programmatically detect this case. See QUM-527.
const askUserQuestionRestrictedError = `{"error":"ask_user_question is restricted to weave and managers; escalate to your parent instead"}`

// MsgSender accepts an opaque tea.Msg-typed value and dispatches it into
// the host TUI program. The sprawlmcp package keeps the type as `any` to
// avoid importing bubbletea (cmd/enter.go performs the type-erasure dance
// with the real tea.Program.Send). (QUM-497)
type MsgSender func(msg any)

// Server implements host.MCPServer for the sprawl MCP server.
type Server struct {
	sup       supervisor.Supervisor
	callLog   *calllog.Logger
	msgSender atomic.Pointer[MsgSender] // QUM-497: TUI in-flight indicator hook
	cfg       *config.Config            // QUM-722: project config for pause defaults; may be nil
}

// New creates a new MCP server backed by the given supervisor.
func New(sup supervisor.Supervisor) *Server {
	return &Server{sup: sup, callLog: calllog.NewNoop()}
}

// WithConfig attaches the loaded project config so MCP tools can read
// project-level defaults (e.g. pause_timeout_seconds). QUM-722. Returns
// the receiver for chaining. nil clears any prior config.
func (s *Server) WithConfig(c *config.Config) *Server {
	s.cfg = c
	return s
}

// SetMsgSender installs (or clears) the TUI message sender used to surface
// MCPCallStartedMsg / MCPCallEndedMsg events for in-flight tool calls
// (QUM-497). Pass nil to clear (e.g. on TUI shutdown). Safe to call
// concurrently — the field is backed by an atomic pointer.
func (s *Server) SetMsgSender(send MsgSender) {
	if send == nil {
		s.msgSender.Store(nil)
		return
	}
	fn := send
	s.msgSender.Store(&fn)
}

func (s *Server) emitMsg(msg any) {
	if p := s.msgSender.Load(); p != nil && *p != nil {
		(*p)(msg)
	}
}

// WithCallLog attaches a per-call observability logger to the server
// (QUM-494). Returns the receiver for chaining. nil resets to no-op.
func (s *Server) WithCallLog(l *calllog.Logger) *Server {
	if l == nil {
		l = calllog.NewNoop()
	}
	s.callLog = l
	return s
}

// HandleMessage handles a JSON-RPC message per the MCP protocol.
func (s *Server) HandleMessage(ctx context.Context, msg json.RawMessage) (json.RawMessage, error) {
	var req jsonRPCRequest
	if err := json.Unmarshal(msg, &req); err != nil {
		return nil, err
	}

	switch req.Method {
	case "initialize":
		return s.handleInitialize(req.ID)
	case "tools/list":
		return s.handleToolsList(req.ID)
	case "tools/call":
		return s.handleToolsCall(ctx, req.ID, req.Params)
	case "notifications/initialized":
		return nil, nil
	default:
		return jsonRPCError(req.ID, -32601, fmt.Sprintf("method not found: %s", req.Method))
	}
}

func (s *Server) handleInitialize(id json.RawMessage) (json.RawMessage, error) {
	return jsonRPCResult(id, map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    "sprawl",
			"version": "1.0.0",
		},
	})
}

func (s *Server) handleToolsList(id json.RawMessage) (json.RawMessage, error) {
	return jsonRPCResult(id, map[string]any{
		"tools": toolDefinitions(),
	})
}

func (s *Server) handleToolsCall(ctx context.Context, id json.RawMessage, params json.RawMessage) (json.RawMessage, error) {
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if params != nil {
		if err := json.Unmarshal(params, &call); err != nil {
			return jsonRPCError(id, -32602, "invalid params")
		}
	}

	caller := backendpkg.CallerIdentity(ctx)
	var argsAny any
	if len(call.Arguments) > 0 {
		_ = json.Unmarshal(call.Arguments, &argsAny)
	}
	startedAt := time.Now()
	ctx, callID := s.callLog.Begin(ctx, call.Name, caller, argsAny)
	// QUM-497: surface the call to the host TUI's status bar so a hung tool
	// is visible long before the user reaches for Ctrl-C. callID may be empty
	// when the calllog is in noop mode — in that case we synthesize a stable
	// id from the tool+caller+time so the TUI can still pair Started/Ended.
	mcpID := callID
	if mcpID == "" {
		mcpID = fmt.Sprintf("noop-%s-%s-%d", call.Name, caller, startedAt.UnixNano())
	}
	s.emitMsg(tui.MCPCallStartedMsg{
		CallID:  mcpID,
		Tool:    call.Name,
		Caller:  caller,
		Started: startedAt,
	})
	endMCP := func(status, _ string) {
		s.emitMsg(tui.MCPCallEndedMsg{
			CallID:   mcpID,
			Status:   status,
			Duration: time.Since(startedAt),
		})
	}

	// QUM-729: route every exit path (ok / error / panic) through a single
	// defer so End is always emitted exactly once, even if future code in
	// this function gains another early return.
	endStatus := "ok"
	endErr := ""
	defer func() {
		s.callLog.End(callID, endStatus, endErr)
		endMCP(endStatus, endErr)
	}()

	var (
		text     string
		err      error
		panicErr any
		panicked bool
	)
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicErr = r
				panicked = true
			}
		}()
		text, err = s.dispatchTool(ctx, call.Name, call.Arguments)
	}()

	if panicked {
		endStatus = "panic"
		endErr = fmt.Sprintf("%v", panicErr)
		// Re-panic AFTER deferred End fires.
		panic(panicErr)
	}

	if err != nil {
		endStatus = "error"
		endErr = err.Error()
		var ute *unknownToolError
		if ok := isUnknownToolError(err, &ute); ok {
			return jsonRPCError(id, -32602, ute.Error())
		}
		return toolErrorResult(id, err.Error())
	}
	return toolSuccessResult(id, text)
}

func (s *Server) dispatchTool(ctx context.Context, name string, args json.RawMessage) (string, error) {
	switch name {
	case "spawn":
		return s.toolSpawn(ctx, args)
	case "status":
		return s.toolStatus(ctx)
	case "delegate":
		return s.toolDelegate(ctx, args)
	case "send_message":
		return s.toolSendMessage(ctx, args)
	case "peek":
		return s.toolPeek(ctx, args)
	case "report_status":
		return s.toolReportStatus(ctx, args)
	case "merge":
		return s.toolMerge(ctx, args)
	case "retire":
		return s.toolRetire(ctx, args)
	case "kill":
		return s.toolKill(ctx, args)
	case "pause":
		return s.toolPause(ctx, args)
	case "wake":
		return s.toolWake(ctx, args)
	case "handoff":
		return s.toolHandoff(ctx, args)
	case "messages_list":
		return s.toolMessagesList(ctx, args)
	case "messages_read":
		return s.toolMessagesRead(ctx, args)
	case "messages_archive":
		return s.toolMessagesArchive(ctx, args)
	case "messages_peek":
		return s.toolMessagesPeek(ctx)
	case "ask_user_question":
		return s.toolAskUserQuestion(ctx, args)
	case "_test_sleep":
		if !testToolsEnabled() {
			return "", &unknownToolError{name: name}
		}
		return s.toolTestSleep(ctx, args)
	default:
		// QUM-606: build-tag-gated `_test_induce_wedge` is dispatched via
		// the inject seam — present only when `sprawl_test` is set.
		if text, matched, err := s.dispatchInjectTool(ctx, name, args); matched {
			return text, err
		}
		// Unknown tools get a JSON-RPC error, not a tool content error
		return "", &unknownToolError{name: name}
	}
}

// toolTestSleep is an internal test-only MCP tool exposed when
// SPRAWL_ENABLE_TEST_TOOLS=1. It exists for the QUM-552 sandbox repro of
// interrupt-during-MCP-tool-wait: it parks the MCP dispatch path for a
// caller-specified duration while remaining ctx-respecting, so the
// async-dispatch + interrupt-cancellation behavior added in QUM-552 can
// be exercised end-to-end against a real claude subprocess. NEVER enable
// this tool in production.
func (s *Server) toolTestSleep(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Seconds int `json:"seconds"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if p.Seconds < 0 {
		p.Seconds = 0
	}
	if p.Seconds > 60 {
		p.Seconds = 60
	}
	d := time.Duration(p.Seconds) * time.Second
	start := time.Now()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(d):
	}
	return fmt.Sprintf("slept %s", time.Since(start).Round(time.Millisecond)), nil
}

// weaveEngineerAdvisory is the non-blocking orchestration advisory appended
// to toolSpawn results when the root weave spawns type="engineer" directly
// (QUM-710 §4 / QUM-719). The advisory nudges weave toward the standard
// weave → manager → (engineer + QA) shape without rejecting the spawn.
const weaveEngineerAdvisory = `ADVISORY: weave should normally spawn an engineering manager rather than an engineer directly.
The standard shape is weave → manager → (engineer + QA). If this is a deliberate single-file,
single-commit exception, proceed; otherwise retire this agent and spawn a manager.`

func (s *Server) toolSpawn(ctx context.Context, args json.RawMessage) (string, error) {
	var req supervisor.SpawnRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	// QUM-709: validate subagent / branch interaction before forwarding.
	if req.Subagent && req.Branch != "" {
		return "", fmt.Errorf("branch must not be set when subagent is true; sub-agents share the parent's branch")
	}
	if !req.Subagent && req.Branch == "" {
		return "", fmt.Errorf("branch is required when subagent is false")
	}
	info, err := s.sup.Spawn(ctx, req)
	if err != nil {
		return "", err
	}
	data, _ := json.MarshalIndent(info, "", "  ")
	result := fmt.Sprintf("Spawned agent:\n%s", string(data))
	// QUM-719: when the root weave (empty caller identity) spawns an
	// engineer directly, append a non-blocking orchestration advisory.
	// The spawn still succeeds and the agent stays alive.
	if req.Type == "engineer" && backendpkg.CallerIdentity(ctx) == "" {
		result += "\n\n" + weaveEngineerAdvisory
	}
	return result, nil
}

func (s *Server) toolStatus(ctx context.Context) (string, error) {
	agents, err := s.sup.Status(ctx)
	if err != nil {
		return "", err
	}
	if len(agents) == 0 {
		return "No agents currently registered.", nil
	}
	data, _ := json.MarshalIndent(agents, "", "  ")
	return string(data), nil
}

func (s *Server) toolDelegate(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Agent         string `json:"agent"`
		AgentName     string `json:"agent_name"` // QUM-666: deprecated synonym
		Task          string `json:"task"`
		WakeIfOffline bool   `json:"wake_if_offline"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	caller := backendpkg.CallerIdentity(ctx)
	target, err := resolveAgentTarget(p.Agent, p.AgentName, "delegate", caller)
	if err != nil {
		return "", err
	}
	if err := s.sup.Delegate(ctx, target, p.Task, p.WakeIfOffline); err != nil {
		return "", err
	}
	return fmt.Sprintf("Delegated task to %s", target), nil
}

// toolSendMessage dispatches the canonical send_message MCP tool (QUM-550).
// interrupt=false maps to a cooperative wake; interrupt=true forces a preempt.
func (s *Server) toolSendMessage(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		To            string `json:"to"`
		Body          string `json:"body"`
		Interrupt     bool   `json:"interrupt"`
		WakeIfOffline bool   `json:"wake_if_offline"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	result, err := s.sup.SendMessage(ctx, p.To, p.Body, p.Interrupt, p.WakeIfOffline)
	if err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshaling result: %w", err)
	}
	return string(data), nil
}

const (
	defaultPeekTail = 20
	maxPeekTail     = 200
)

func (s *Server) toolPeek(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Agent string `json:"agent"`
		Tail  int    `json:"tail"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	tail := p.Tail
	if tail <= 0 {
		tail = defaultPeekTail
	}
	if tail > maxPeekTail {
		tail = maxPeekTail
	}
	result, err := s.sup.Peek(ctx, p.Agent, tail)
	if err != nil {
		return "", err
	}
	// Round-trip the PeekResult through a generic map so we can emit both
	// "in_turn" (canonical, QUM-692) and "in_autonomous_turn" (alias retained
	// for one release; see QUM-692) with identical values.
	raw, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("marshaling result: %w", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", fmt.Errorf("re-decoding result: %w", err)
	}
	if v, ok := m["in_turn"]; ok {
		m["in_autonomous_turn"] = v
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshaling result: %w", err)
	}
	return string(data), nil
}

func (s *Server) toolReportStatus(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		State   string `json:"state"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	// Pass caller identity from context so child agents report under their
	// own name instead of the shared supervisor's callerName (QUM-387).
	agentName := backendpkg.CallerIdentity(ctx)
	result, err := s.sup.ReportStatus(ctx, agentName, p.State, p.Summary)
	if err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshaling result: %w", err)
	}
	return string(data), nil
}

func (s *Server) toolMerge(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Agent      string `json:"agent"`
		AgentName  string `json:"agent_name"` // QUM-666: deprecated synonym
		Message    string `json:"message"`
		NoValidate bool   `json:"no_validate"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	// QUM-487: pass child agent identity from the request context so the
	// supervisor's per-call agentops deps reflect the caller (not the
	// long-lived supervisor process's SPRAWL_AGENT_IDENTITY).
	caller := backendpkg.CallerIdentity(ctx)
	target, err := resolveAgentTarget(p.Agent, p.AgentName, "merge", caller)
	if err != nil {
		return "", err
	}
	outcome, err := s.sup.Merge(ctx, caller, target, p.Message, p.NoValidate)
	if err != nil {
		return "", err
	}
	prefix := ""
	if outcome != nil && outcome.QueuedBehind != "" {
		prefix = fmt.Sprintf("Queued behind merge of %s (waited %s). ", outcome.QueuedBehind, outcome.QueueWait.Round(time.Millisecond))
	}
	if outcome != nil && outcome.NoOp {
		return prefix + fmt.Sprintf("Nothing to merge: %s has no new commits", target), nil
	}
	return prefix + fmt.Sprintf("Merged agent %s", target), nil
}

func (s *Server) toolRetire(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Agent     string `json:"agent"`
		AgentName string `json:"agent_name"` // QUM-666: deprecated synonym
		Merge     bool   `json:"merge"`
		Abandon   bool   `json:"abandon"`
		Cascade   bool   `json:"cascade"`
		Validate  *bool  `json:"validate"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if p.Merge && p.Abandon {
		return "", fmt.Errorf("merge and abandon are mutually exclusive")
	}
	noValidate := p.Validate != nil && !*p.Validate
	// QUM-487: see toolMerge for rationale.
	caller := backendpkg.CallerIdentity(ctx)
	target, err := resolveAgentTarget(p.Agent, p.AgentName, "retire", caller)
	if err != nil {
		return "", err
	}
	if err := s.sup.Retire(ctx, caller, target, p.Merge, p.Abandon, p.Cascade, noValidate); err != nil {
		return "", err
	}
	switch {
	case p.Cascade && p.Abandon:
		return fmt.Sprintf("Retired agent %s and descendants (branches abandoned)", target), nil
	case p.Cascade && p.Merge:
		return fmt.Sprintf("Merged and retired agent %s and descendants", target), nil
	case p.Cascade:
		return fmt.Sprintf("Retired agent %s and descendants", target), nil
	case p.Abandon:
		return fmt.Sprintf("Retired agent %s (branch abandoned)", target), nil
	case p.Merge:
		return fmt.Sprintf("Merged and retired agent %s", target), nil
	default:
		return fmt.Sprintf("Retired agent %s", target), nil
	}
}

func (s *Server) toolKill(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Agent     string `json:"agent"`
		AgentName string `json:"agent_name"` // QUM-666: deprecated synonym
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	caller := backendpkg.CallerIdentity(ctx)
	target, err := resolveAgentTarget(p.Agent, p.AgentName, "kill", caller)
	if err != nil {
		return "", err
	}
	if err := s.sup.Kill(ctx, target); err != nil {
		return "", err
	}
	return fmt.Sprintf("Killed agent %s", target), nil
}

// toolPause dispatches the pause MCP tool (QUM-722). Cascade defaults to
// true; an omitted timeout_seconds falls back to the project config's
// PauseTimeout (or config.DefaultPauseTimeoutSeconds when no config is loaded).
func (s *Server) toolPause(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Agent          string `json:"agent"`
		AgentName      string `json:"agent_name"`
		TimeoutSeconds *int   `json:"timeout_seconds"`
		Cascade        *bool  `json:"cascade"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	caller := backendpkg.CallerIdentity(ctx)
	target, err := resolveAgentTarget(p.Agent, p.AgentName, "pause", caller)
	if err != nil {
		return "", err
	}
	var timeout time.Duration
	switch {
	case p.TimeoutSeconds != nil && *p.TimeoutSeconds > 0:
		timeout = time.Duration(*p.TimeoutSeconds) * time.Second
	case s.cfg != nil:
		timeout = s.cfg.PauseTimeout()
	default:
		timeout = time.Duration(config.DefaultPauseTimeoutSeconds) * time.Second
	}
	cascade := true
	if p.Cascade != nil {
		cascade = *p.Cascade
	}
	opts := supervisor.PauseOptions{
		Timeout: timeout,
		Cascade: cascade,
	}
	res, err := s.sup.Pause(ctx, target, opts)
	if err != nil {
		return "", err
	}
	if res == nil {
		return fmt.Sprintf("Paused agent %s", target), nil
	}
	switch res.Outcome {
	case "paused":
		return fmt.Sprintf("Agent %s paused cleanly (waited %dms)", target, res.WaitMs), nil
	case "escalated_to_kill":
		return fmt.Sprintf("Escalated pause of agent %s to kill (timeout after %dms)", target, res.WaitMs), nil
	default:
		return fmt.Sprintf("Pause of agent %s completed (outcome=%s)", target, res.Outcome), nil
	}
}

// toolWake dispatches the wake MCP tool (QUM-724, renamed from recover/
// QUM-601 with expanded scope). On success returns a JSON-marshaled WakeResult
// so callers can branch on Mode. ErrWakeNotNeeded is treated as a success
// ack ("Agent <name> already running; no wake needed"). Other supervisor
// errors propagate as tool-error content (IsError=true).
func (s *Server) toolWake(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Agent     string `json:"agent"`
		AgentName string `json:"agent_name"` // QUM-666: deprecated synonym
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	caller := backendpkg.CallerIdentity(ctx)
	target, err := resolveAgentTarget(p.Agent, p.AgentName, "wake", caller)
	if err != nil {
		return "", err
	}
	res, err := s.sup.Wake(ctx, target, agentpkg.WakeReasonBare, "")
	if err != nil {
		if errors.Is(err, supervisor.ErrWakeNotNeeded) {
			return fmt.Sprintf("Agent %s already running; no wake needed", target), nil
		}
		return "", err
	}
	if res == nil {
		return fmt.Sprintf("Woke agent %s", target), nil
	}
	data, mErr := json.Marshal(res)
	if mErr != nil {
		return "", fmt.Errorf("marshaling WakeResult: %w", mErr)
	}
	return string(data), nil
}

func (s *Server) toolHandoff(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if err := s.sup.Handoff(ctx, p.Summary); err != nil {
		return "", err
	}
	return "Handoff recorded. Session will restart momentarily with fresh context.", nil
}

func (s *Server) toolMessagesList(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Filter string `json:"filter"`
		Limit  int    `json:"limit"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &p); err != nil {
			return "", fmt.Errorf("invalid arguments: %w", err)
		}
	}
	result, err := s.sup.MessagesList(ctx, p.Filter, p.Limit)
	if err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshaling result: %w", err)
	}
	return string(data), nil
}

func (s *Server) toolMessagesRead(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	result, err := s.sup.MessagesRead(ctx, p.ID)
	if err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshaling result: %w", err)
	}
	return string(data), nil
}

func (s *Server) toolMessagesArchive(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		ID  string `json:"id"`
		All bool   `json:"all"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	if p.All {
		result, err := s.sup.MessagesArchiveAll(ctx, "all")
		if err != nil {
			return "", err
		}
		data, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return "", fmt.Errorf("marshaling result: %w", err)
		}
		return string(data), nil
	}

	if p.ID == "" {
		return "", fmt.Errorf("either 'id' or 'all' must be provided")
	}

	result, err := s.sup.MessagesArchive(ctx, p.ID)
	if err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshaling result: %w", err)
	}
	return string(data), nil
}

func (s *Server) toolMessagesPeek(ctx context.Context) (string, error) {
	result, err := s.sup.MessagesPeek(ctx)
	if err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshaling result: %w", err)
	}
	return string(data), nil
}

// toolAskUserQuestion dispatches the ask_user_question MCP tool. It performs
// the server-side eligibility gate (only root weave and manager-type agents
// may call), strict snake_case argument parsing, and blocks on
// supervisor.AskUserQuestion until the user responds, declines, or the queue
// is cancelled. The QuestionResponse is marshaled as JSON in the tool result
// text. See QUM-527 §"Eligibility (server-side gate)" / §"Wire-level schema".
func (s *Server) toolAskUserQuestion(ctx context.Context, args json.RawMessage) (string, error) {
	caller := backendpkg.CallerIdentity(ctx)
	if err := s.askUserQuestionEligibility(ctx, caller); err != nil {
		return "", err
	}

	var p struct {
		Questions []supervisor.Question `json:"questions"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if len(p.Questions) == 0 {
		return "", fmt.Errorf("ask_user_question: questions[] must not be empty")
	}
	for i, q := range p.Questions {
		if q.Prompt == "" {
			return "", fmt.Errorf("ask_user_question: questions[%d].question is required", i)
		}
		if len(q.Options) == 0 {
			return "", fmt.Errorf("ask_user_question: questions[%d].options must not be empty", i)
		}
	}

	requestID, err := state.GenerateUUID()
	if err != nil {
		return "", fmt.Errorf("ask_user_question: generating request id: %w", err)
	}
	req := supervisor.QuestionRequest{
		RequestID: requestID,
		From:      caller,
		Questions: p.Questions,
	}
	resp, err := s.sup.AskUserQuestion(ctx, req)
	if err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshaling QuestionResponse: %w", err)
	}
	return string(data), nil
}

// askUserQuestionEligibility enforces the server-side eligibility gate.
// Empty caller identity (root weave) is allowed. Otherwise the caller's
// agent record is looked up via Supervisor.Status and only Type=="manager"
// or Type=="root" passes. Engineer and researcher callers (and any other
// type) get the canonical structured restriction error.
func (s *Server) askUserQuestionEligibility(ctx context.Context, caller string) error {
	if caller == "" {
		// Root weave session — no per-agent record, always allowed.
		return nil
	}
	agents, err := s.sup.Status(ctx)
	if err != nil {
		return fmt.Errorf("ask_user_question: looking up caller %q: %w", caller, err)
	}
	for _, a := range agents {
		if a.Name == caller {
			if a.Type == "manager" || a.Type == "root" {
				return nil
			}
			return errors.New(askUserQuestionRestrictedError)
		}
	}
	// Unknown caller — be conservative and reject.
	return errors.New(askUserQuestionRestrictedError)
}

// unknownToolError is used to distinguish unknown tool errors from supervisor errors.
type unknownToolError struct {
	name string
}

func (e *unknownToolError) Error() string {
	return fmt.Sprintf("unknown tool: %s", e.name)
}

func isUnknownToolError(err error, target **unknownToolError) bool {
	return errors.As(err, target)
}

// JSON-RPC helpers

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

func jsonRPCResult(id json.RawMessage, result any) (json.RawMessage, error) {
	resp := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	}
	return json.Marshal(resp)
}

func jsonRPCError(id json.RawMessage, code int, message string) (json.RawMessage, error) {
	resp := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	}
	return json.Marshal(resp)
}

func toolSuccessResult(id json.RawMessage, text string) (json.RawMessage, error) {
	return jsonRPCResult(id, map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": text},
		},
	})
}

func toolErrorResult(id json.RawMessage, errMsg string) (json.RawMessage, error) {
	return jsonRPCResult(id, map[string]any{
		"isError": true,
		"content": []map[string]any{
			{"type": "text", "text": errMsg},
		},
	})
}
