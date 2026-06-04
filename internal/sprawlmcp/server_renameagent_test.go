// QUM-666: tests covering the rename of agent_name → agent across all
// agent-targeting MCP tools (delegate, merge, retire, kill, recover).
// `peek` already uses the canonical `agent` key and is unaffected.
//
// Three behaviors are exercised for each renamed tool:
//   1. Canonical `agent` key → success, supervisor sees the target name.
//   2. Deprecated `agent_name` key → success AND a structured slog warning
//      is emitted naming the tool, the deprecated key, and the caller.
//   3. Neither key present → tool-error response whose text names the
//      canonical key `agent` (NOT the misleading "must not be empty").

package sprawlmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// captureSlog installs a JSON slog handler writing to a fresh buffer as
// the default logger for the duration of the test, then restores the
// previous default on cleanup.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	h := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// findDeprecationLog scans buf line-by-line and returns the first record
// whose msg matches "mcp.deprecated_parameter". Returns nil when absent.
func findDeprecationLog(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	for _, line := range strings.Split(buf.String(), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec["msg"] == "mcp.deprecated_parameter" {
			return rec
		}
	}
	return nil
}

// extractToolErrorText returns the text from a tool-error response (where
// result.isError == true). Fatals if isError is false or the shape is off.
func extractToolErrorText(t *testing.T, resp json.RawMessage) string {
	t.Helper()
	parsed := parseJSONRPCResponse(t, resp)
	if e, ok := parsed["error"]; ok {
		t.Fatalf("got top-level JSON-RPC error, expected tool-error result: %v", e)
	}
	result, ok := parsed["result"].(map[string]any)
	if !ok {
		t.Fatal("missing result")
	}
	if isErr, _ := result["isError"].(bool); !isErr {
		t.Fatalf("isError = false, want true. result=%v", result)
	}
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatal("missing content")
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	return text
}

// renamedToolCall builds a tools/call JSON-RPC request for one of the five
// renamed tools with the given agent-key/value, plus tool-specific extra
// required args (e.g. delegate's task).
func renamedToolCall(t *testing.T, id int, tool, key, value string) json.RawMessage {
	t.Helper()
	args := map[string]any{}
	if key != "" {
		args[key] = value
	}
	if tool == "delegate" {
		args["task"] = "do thing"
	}
	return makeJSONRPCRequest(id, "tools/call", map[string]any{
		"name":      tool,
		"arguments": args,
	})
}

// supervisorTarget extracts the agent name recorded by the mock supervisor
// for a given tool. recoverAwareSupervisor wraps mockSupervisor so we read
// from the outer struct only for the recover tool.
func supervisorTarget(tool string, mock *mockSupervisor, rec *recoverAwareSupervisor) string {
	switch tool {
	case "delegate":
		return mock.delegateAgent
	case "merge":
		return mock.mergeAgent
	case "retire":
		return mock.retireAgent
	case "kill":
		return mock.killAgent
	case "recover":
		return rec.recoverAgent
	}
	return ""
}

var renamedAgentToolNames = []string{"delegate", "merge", "retire", "kill", "recover"}

// newSupForRenamed returns a supervisor wired to record the agent name
// regardless of which renamed tool is dispatched. recoverAwareSupervisor
// embeds mockSupervisor so its address satisfies supervisor.Supervisor.
func newSupForRenamed() (*mockSupervisor, *recoverAwareSupervisor) {
	rec := newRecoverAware()
	return rec.mockSupervisor, rec
}

// --- Canonical-key success ----------------------------------------------

func TestRenamedAgentTools_AcceptCanonicalAgentKey(t *testing.T) {
	for i, tool := range renamedAgentToolNames {
		t.Run(tool, func(t *testing.T) {
			mock, rec := newSupForRenamed()
			srv := New(rec)
			msg := renamedToolCall(t, 1000+i, tool, "agent", "ratz")
			resp, err := srv.HandleMessage(context.Background(), msg)
			if err != nil {
				t.Fatalf("HandleMessage: %v", err)
			}
			parsed := parseJSONRPCResponse(t, resp)
			result, ok := parsed["result"].(map[string]any)
			if !ok {
				t.Fatalf("missing result: %v", parsed)
			}
			if isErr, _ := result["isError"].(bool); isErr {
				content, _ := result["content"].([]any)
				txt := ""
				if len(content) > 0 {
					first, _ := content[0].(map[string]any)
					txt, _ = first["text"].(string)
				}
				t.Fatalf("canonical agent key produced tool error: %q", txt)
			}
			if got := supervisorTarget(tool, mock, rec); got != "ratz" {
				t.Errorf("supervisor saw agent = %q, want %q", got, "ratz")
			}
		})
	}
}

// --- Deprecated-synonym success + deprecation log ------------------------

func TestRenamedAgentTools_AcceptDeprecatedAgentNameKeyWithWarning(t *testing.T) {
	for i, tool := range renamedAgentToolNames {
		t.Run(tool, func(t *testing.T) {
			buf := captureSlog(t)
			mock, rec := newSupForRenamed()
			srv := New(rec)

			// Inject a caller identity so the deprecation log can record it.
			ctx := withTestCallerIdentity(context.Background(), "tower")

			msg := renamedToolCall(t, 2000+i, tool, "agent_name", "ratz")
			resp, err := srv.HandleMessage(ctx, msg)
			if err != nil {
				t.Fatalf("HandleMessage: %v", err)
			}
			parsed := parseJSONRPCResponse(t, resp)
			result, ok := parsed["result"].(map[string]any)
			if !ok {
				t.Fatalf("missing result: %v", parsed)
			}
			if isErr, _ := result["isError"].(bool); isErr {
				content, _ := result["content"].([]any)
				txt := ""
				if len(content) > 0 {
					first, _ := content[0].(map[string]any)
					txt, _ = first["text"].(string)
				}
				t.Fatalf("deprecated agent_name produced tool error: %q", txt)
			}
			if got := supervisorTarget(tool, mock, rec); got != "ratz" {
				t.Errorf("supervisor saw agent = %q, want %q (deprecated synonym must still resolve)", got, "ratz")
			}

			logRec := findDeprecationLog(t, buf)
			if logRec == nil {
				t.Fatalf("expected mcp.deprecated_parameter slog record; got log:\n%s", buf.String())
			}
			if logRec["tool"] != tool {
				t.Errorf("deprecation log tool = %v, want %q", logRec["tool"], tool)
			}
			if logRec["deprecated"] != "agent_name" {
				t.Errorf("deprecation log deprecated = %v, want %q", logRec["deprecated"], "agent_name")
			}
			if logRec["canonical"] != "agent" {
				t.Errorf("deprecation log canonical = %v, want %q", logRec["canonical"], "agent")
			}
			if logRec["caller"] != "tower" {
				t.Errorf("deprecation log caller = %v, want %q", logRec["caller"], "tower")
			}
		})
	}
}

// --- Missing-key error names the canonical key ---------------------------

func TestRenamedAgentTools_MissingAgentKeyErrorNamesCanonical(t *testing.T) {
	for i, tool := range renamedAgentToolNames {
		t.Run(tool, func(t *testing.T) {
			_, rec := newSupForRenamed()
			srv := New(rec)
			msg := renamedToolCall(t, 3000+i, tool, "", "")
			resp, err := srv.HandleMessage(context.Background(), msg)
			if err != nil {
				t.Fatalf("HandleMessage: %v", err)
			}
			text := extractToolErrorText(t, resp)
			if !strings.Contains(text, "agent") {
				t.Errorf("error text %q does not name canonical key 'agent'", text)
			}
			// Reject the misleading legacy phrasing.
			if strings.Contains(text, "must not be empty") {
				t.Errorf("error text uses misleading legacy phrasing %q", text)
			}
		})
	}
}
