// Command hosttest exercises the Claude Code host protocol end-to-end.
//
// Usage:
//
//	go run cmd/hosttest/main.go "what is 2+2"
//	go run cmd/hosttest/main.go --interactive
//	go run cmd/hosttest/main.go --test-mcp "call the echo tool"
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync/atomic"

	"github.com/dmotles/sprawl/internal/host"
	"github.com/dmotles/sprawl/internal/protocol"
)

var reqSeq atomic.Int64

func nextRequestID() string {
	return fmt.Sprintf("hosttest-%d", reqSeq.Add(1))
}

func main() {
	var (
		interactive bool
		testMCP     bool
		claudePath  string
		model       string
		workDir     string
	)

	flag.BoolVar(&interactive, "interactive", false, "interactive multi-turn mode")
	flag.BoolVar(&testMCP, "test-mcp", false, "register a dummy MCP echo server and test it")
	flag.StringVar(&claudePath, "claude-path", "claude", "path to claude binary")
	flag.StringVar(&model, "model", "", "model to use")
	flag.StringVar(&workDir, "work-dir", "", "working directory")
	flag.Parse()

	prompt := strings.Join(flag.Args(), " ")
	if prompt == "" && !interactive {
		fmt.Fprintln(os.Stderr, "Usage: hosttest [flags] <prompt>")
		fmt.Fprintln(os.Stderr, "  or: hosttest --interactive")
		os.Exit(1)
	}

	if workDir == "" {
		workDir, _ = os.Getwd()
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)

	err := run(ctx, runConfig{
		claudePath:  claudePath,
		model:       model,
		workDir:     workDir,
		prompt:      prompt,
		interactive: interactive,
		testMCP:     testMCP,
	})
	cancel()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

type runConfig struct {
	claudePath  string
	model       string
	workDir     string
	prompt      string
	interactive bool
	testMCP     bool
}

func run(ctx context.Context, cfg runConfig) error {
	// Build launch args
	args := []string{
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--permission-mode", "bypassPermissions",
	}
	if cfg.model != "" {
		args = append(args, "--model", cfg.model)
	}

	// Start subprocess
	cmd := exec.CommandContext(ctx, cfg.claudePath, args...) //nolint:gosec // claudePath is user-provided CLI flag, not untrusted input
	cmd.Dir = cfg.workDir
	cmd.Stderr = os.Stderr

	env := os.Environ()
	env = append(env, "CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS=1")
	cmd.Env = env

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("creating stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("creating stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting claude: %w", err)
	}

	reader := protocol.NewReader(stdout)
	writer := protocol.NewWriter(stdin)

	transport := &subprocessTransport{
		reader: reader,
		writer: writer,
		wait:   cmd.Wait,
		kill: func() error {
			if cmd.Process != nil {
				return cmd.Process.Kill()
			}
			return nil
		},
	}

	// Set up MCP bridge if testing MCP
	var bridge *host.MCPBridge
	if cfg.testMCP {
		bridge = host.NewMCPBridge()
		bridge.Register("test-echo", &echoMCPServer{})
	}

	// Send initialize
	printLabel("init", "sending initialize request")
	initReqID := nextRequestID()
	initReq := map[string]any{
		"type":       "control_request",
		"request_id": initReqID,
		"request": map[string]any{
			"subtype": "initialize",
		},
	}
	if cfg.testMCP {
		initReq["request"] = map[string]any{
			"subtype":           "initialize",
			"sdkMcpServers":     []string{"test-echo"},
			"systemPrompt":      "You have access to MCP tools from the test-echo server.",
			"promptSuggestions": false,
		}
	}
	if err := writer.WriteJSON(initReq); err != nil {
		return fmt.Errorf("sending initialize: %w", err)
	}

	// Main read loop - process all messages
	if cfg.interactive {
		return runInteractive(ctx, transport, bridge)
	}
	return runSinglePrompt(ctx, transport, bridge, cfg.prompt)
}

func runSinglePrompt(ctx context.Context, t *subprocessTransport, bridge *host.MCPBridge, prompt string) error {
	// Wait for initialize response, then send prompt
	initialized := false
	promptSent := false

	for {
		msg, err := t.reader.Next()
		if err != nil {
			if !initialized {
				return fmt.Errorf("reading during init: %w", err)
			}
			return nil
		}

		printMessage(msg)

		switch msg.Type {
		case "control_response":
			if !initialized {
				initialized = true
				printLabel("init", "session initialized")

				// Send user prompt
				if err := t.writer.SendUserMessage(prompt); err != nil {
					return fmt.Errorf("sending prompt: %w", err)
				}
				promptSent = true
				printLabel("user", prompt)
			}

		case "control_request":
			if err := handleControlRequest(ctx, t, bridge, msg); err != nil {
				return fmt.Errorf("handling control request: %w", err)
			}

		case "result":
			if promptSent {
				var result protocol.ResultMessage
				if err := protocol.ParseAs(msg, &result); err == nil {
					printResult(&result)
				}
				_ = t.Close()
				if result.IsError {
					return fmt.Errorf("claude returned error: %v", result.Errors)
				}
				return nil
			}
		}
	}
}

func runInteractive(ctx context.Context, t *subprocessTransport, bridge *host.MCPBridge) error {
	// Wait for initialize
	for {
		msg, err := t.reader.Next()
		if err != nil {
			return fmt.Errorf("reading during init: %w", err)
		}
		printMessage(msg)
		if msg.Type == "control_response" {
			printLabel("init", "session initialized - type prompts, empty line to quit")
			break
		}
		if msg.Type == "control_request" {
			if err := handleControlRequest(ctx, t, bridge, msg); err != nil {
				return fmt.Errorf("handling control request during init: %w", err)
			}
		}
	}

	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("\n> ")
		if !scanner.Scan() {
			break
		}
		prompt := strings.TrimSpace(scanner.Text())
		if prompt == "" {
			break
		}

		if err := t.writer.SendUserMessage(prompt); err != nil {
			return fmt.Errorf("sending prompt: %w", err)
		}
		printLabel("user", prompt)

		// Read until result
		for {
			msg, err := t.reader.Next()
			if err != nil {
				return fmt.Errorf("reading response: %w", err)
			}
			printMessage(msg)

			if msg.Type == "control_request" {
				if err := handleControlRequest(ctx, t, bridge, msg); err != nil {
					return fmt.Errorf("handling control request: %w", err)
				}
				continue
			}

			if msg.Type == "result" {
				var result protocol.ResultMessage
				if err := protocol.ParseAs(msg, &result); err == nil {
					printResult(&result)
				}
				break
			}
		}
	}

	printLabel("session", "ending session")
	return t.Close()
}

func handleControlRequest(ctx context.Context, t *subprocessTransport, bridge *host.MCPBridge, msg *protocol.Message) error {
	var cr protocol.ControlRequest
	if err := protocol.ParseAs(msg, &cr); err != nil {
		return fmt.Errorf("parsing control request: %w", err)
	}

	// Parse subtype
	var req struct {
		Subtype    string          `json:"subtype"`
		ServerName string          `json:"server_name,omitempty"`
		Message    json.RawMessage `json:"message,omitempty"`
		ToolName   string          `json:"tool_name,omitempty"`
	}
	if err := json.Unmarshal(cr.Request, &req); err != nil {
		return fmt.Errorf("parsing control request inner: %w", err)
	}

	switch req.Subtype {
	case "can_use_tool":
		printLabel("control_request:can_use_tool", fmt.Sprintf("tool=%s → auto-approve", req.ToolName))
		return sendToolApproval(t, cr.RequestID, req.ToolName)

	case "mcp_message":
		if bridge == nil {
			printLabel("control_request:mcp_message", fmt.Sprintf("server=%s → no bridge, sending error", req.ServerName))
			return t.writer.SendControlResponse(cr.RequestID, "error", "no MCP bridge configured")
		}
		printLabel("control_request:mcp_message", fmt.Sprintf("server=%s", req.ServerName))
		resp, err := bridge.HandleIncoming(ctx, req.ServerName, req.Message)
		if err != nil {
			return t.writer.SendControlResponse(cr.RequestID, "error", err.Error())
		}
		return sendMCPResponse(t, cr.RequestID, resp)

	case "hook_callback":
		printLabel("control_request:hook_callback", "auto-approve")
		return t.writer.SendControlResponse(cr.RequestID, "success", "")

	default:
		printLabel("control_request:"+req.Subtype, "auto-approve")
		return t.writer.SendControlResponse(cr.RequestID, "success", "")
	}
}

func sendToolApproval(t *subprocessTransport, requestID, _ string) error {
	resp := map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
			"response": map[string]any{
				"behavior":  "allow",
				"toolUseID": "",
				"message":   "Allowed by hosttest",
			},
		},
	}
	return t.writer.WriteJSON(resp)
}

func sendMCPResponse(t *subprocessTransport, requestID string, mcpResp json.RawMessage) error {
	resp := map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
			"response": map[string]any{
				"mcp_response": mcpResp,
			},
		},
	}
	return t.writer.WriteJSON(resp)
}

// subprocessTransport wraps a Claude Code subprocess.
type subprocessTransport struct {
	reader *protocol.Reader
	writer *protocol.Writer
	wait   func() error
	kill   func() error
}

func (t *subprocessTransport) Close() error {
	closeErr := t.writer.Close()
	if closeErr != nil {
		_ = t.kill()
		return closeErr
	}
	return t.wait()
}

// echoMCPServer is a dummy MCP server that echoes back the input for testing.
type echoMCPServer struct{}

func (s *echoMCPServer) HandleMessage(_ context.Context, msg json.RawMessage) (json.RawMessage, error) {
	var req struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id,omitempty"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params,omitempty"`
	}
	if err := json.Unmarshal(msg, &req); err != nil {
		return nil, err
	}

	switch req.Method {
	case "initialize":
		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result": map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities": map[string]any{
					"tools": map[string]any{},
				},
				"serverInfo": map[string]any{
					"name":    "test-echo",
					"version": "1.0.0",
				},
			},
		}
		return json.Marshal(resp)

	case "tools/list":
		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result": map[string]any{
				"tools": []map[string]any{
					{
						"name":        "echo",
						"description": "Echoes back the input message. Use this to test MCP connectivity.",
						"inputSchema": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"message": map[string]any{
									"type":        "string",
									"description": "The message to echo back",
								},
							},
							"required": []string{"message"},
						},
					},
				},
			},
		}
		return json.Marshal(resp)

	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if req.Params != nil {
			_ = json.Unmarshal(req.Params, &params)
		}

		var args struct {
			Message string `json:"message"`
		}
		if params.Arguments != nil {
			_ = json.Unmarshal(params.Arguments, &args)
		}

		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result": map[string]any{
				"content": []map[string]any{
					{
						"type": "text",
						"text": fmt.Sprintf("Echo: %s", args.Message),
					},
				},
			},
		}
		return json.Marshal(resp)

	case "notifications/initialized":
		// Notification, no response needed
		return nil, nil

	default:
		// Unknown method, return error
		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"error": map[string]any{
				"code":    -32601,
				"message": fmt.Sprintf("method not found: %s", req.Method),
			},
		}
		return json.Marshal(resp)
	}
}

// --- Output helpers ---

func printLabel(label, text string) {
	fmt.Printf("[%s] %s\n", label, text)
}

func printMessage(msg *protocol.Message) {
	label := msg.Type
	if msg.Subtype != "" {
		label += ":" + msg.Subtype
	}

	// For control messages, print full JSON
	switch msg.Type {
	case "control_request", "control_response", "control_cancel_request":
		fmt.Printf("[%s] %s\n", label, string(msg.Raw))
	case "result":
		fmt.Printf("[%s] %s\n", label, string(msg.Raw))
	case "assistant":
		// Print a summary instead of the full message
		var am protocol.AssistantMessage
		if err := protocol.ParseAs(msg, &am); err == nil {
			fmt.Printf("[%s] uuid=%s content_len=%d\n", label, am.UUID, len(am.Content))
		} else {
			fmt.Printf("[%s] %s\n", label, string(msg.Raw))
		}
	case "system":
		fmt.Printf("[%s] %s\n", label, string(msg.Raw))
	default:
		fmt.Printf("[%s] %s\n", label, string(msg.Raw))
	}
}

func printResult(r *protocol.ResultMessage) {
	fmt.Println("---")
	fmt.Printf("Result: %s\n", r.Subtype)
	if r.Result != "" {
		// Truncate long results
		text := r.Result
		if len(text) > 500 {
			text = text[:500] + "..."
		}
		fmt.Printf("Text: %s\n", text)
	}
	fmt.Printf("Cost: $%.4f\n", r.TotalCostUsd)
	fmt.Printf("Turns: %d\n", r.NumTurns)
	fmt.Printf("Duration: %dms\n", r.DurationMs)
	if r.IsError {
		fmt.Printf("Errors: %v\n", r.Errors)
	}
	fmt.Println("---")
}
