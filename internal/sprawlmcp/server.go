package sprawlmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dmotles/sprawl/internal/supervisor"
)

// Server implements host.MCPServer for the sprawl-ops MCP server.
type Server struct {
	sup supervisor.Supervisor
}

// New creates a new MCP server backed by the given supervisor.
func New(sup supervisor.Supervisor) *Server {
	return &Server{sup: sup}
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
			"name":    "sprawl-ops",
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

	text, err := s.dispatchTool(ctx, call.Name, call.Arguments)
	if err != nil {
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
	case "sprawl_spawn":
		return s.toolSpawn(ctx, args)
	case "sprawl_status":
		return s.toolStatus(ctx)
	case "sprawl_delegate":
		return s.toolDelegate(ctx, args)
	case "sprawl_send_async":
		return s.toolSendAsync(ctx, args)
	case "sprawl_send_interrupt":
		return s.toolSendInterrupt(ctx, args)
	case "sprawl_peek":
		return s.toolPeek(ctx, args)
	case "sprawl_report_status":
		return s.toolReportStatus(ctx, args)
	case "sprawl_message":
		return s.toolMessage(ctx, args)
	case "sprawl_merge":
		return s.toolMerge(ctx, args)
	case "sprawl_retire":
		return s.toolRetire(ctx, args)
	case "sprawl_kill":
		return s.toolKill(ctx, args)
	case "sprawl_handoff":
		return s.toolHandoff(ctx, args)
	default:
		// Unknown tools get a JSON-RPC error, not a tool content error
		return "", &unknownToolError{name: name}
	}
}

func (s *Server) toolSpawn(ctx context.Context, args json.RawMessage) (string, error) {
	var req supervisor.SpawnRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	info, err := s.sup.Spawn(ctx, req)
	if err != nil {
		return "", err
	}
	data, _ := json.MarshalIndent(info, "", "  ")
	return fmt.Sprintf("Spawned agent:\n%s", string(data)), nil
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
		AgentName string `json:"agent_name"`
		Task      string `json:"task"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if err := s.sup.Delegate(ctx, p.AgentName, p.Task); err != nil {
		return "", err
	}
	return fmt.Sprintf("Delegated task to %s", p.AgentName), nil
}

func (s *Server) toolMessage(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		AgentName string `json:"agent_name"`
		Subject   string `json:"subject"`
		Body      string `json:"body"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	// Deprecated alias: behaves as sprawl_send_async. Writes Maildir + enqueues
	// harness queue entry. Return a short ack for backwards compatibility.
	if _, err := s.sup.SendAsync(ctx, p.AgentName, p.Subject, p.Body, "", nil); err != nil {
		return "", err
	}
	return fmt.Sprintf("Message sent to %s: %s", p.AgentName, p.Subject), nil
}

func (s *Server) toolSendAsync(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		To      string   `json:"to"`
		Subject string   `json:"subject"`
		Body    string   `json:"body"`
		ReplyTo string   `json:"reply_to"`
		Tags    []string `json:"tags"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	result, err := s.sup.SendAsync(ctx, p.To, p.Subject, p.Body, p.ReplyTo, p.Tags)
	if err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshaling result: %w", err)
	}
	return string(data), nil
}

func (s *Server) toolSendInterrupt(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		To         string `json:"to"`
		Subject    string `json:"subject"`
		Body       string `json:"body"`
		ResumeHint string `json:"resume_hint"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	result, err := s.sup.SendInterrupt(ctx, p.To, p.Subject, p.Body, p.ResumeHint)
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
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshaling result: %w", err)
	}
	return string(data), nil
}

func (s *Server) toolReportStatus(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		State   string `json:"state"`
		Summary string `json:"summary"`
		Detail  string `json:"detail"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	// Empty agentName → supervisor uses its own callerName.
	result, err := s.sup.ReportStatus(ctx, "", p.State, p.Summary, p.Detail)
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
		AgentName  string `json:"agent_name"`
		Message    string `json:"message"`
		NoValidate bool   `json:"no_validate"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if err := s.sup.Merge(ctx, p.AgentName, p.Message, p.NoValidate); err != nil {
		return "", err
	}
	return fmt.Sprintf("Merged agent %s", p.AgentName), nil
}

func (s *Server) toolRetire(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		AgentName string `json:"agent_name"`
		Merge     bool   `json:"merge"`
		Abandon   bool   `json:"abandon"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if err := s.sup.Retire(ctx, p.AgentName, p.Merge, p.Abandon); err != nil {
		return "", err
	}
	return fmt.Sprintf("Retired agent %s", p.AgentName), nil
}

func (s *Server) toolKill(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		AgentName string `json:"agent_name"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if err := s.sup.Kill(ctx, p.AgentName); err != nil {
		return "", err
	}
	return fmt.Sprintf("Killed agent %s", p.AgentName), nil
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
