//go:build sprawl_test

// Build-tag-gated test seam used by the QUM-606/QUM-724 live-wake e2e
// harness. `_test_induce_wedge` drives a deterministic backend session
// fault on a named agent's runtime so the harness can validate that
// mcp__sprawl__wake survives an MCP-request-ctx cancel and that the
// recovered subprocess actually outlives the wake call. Compiled in
// ONLY when the `sprawl_test` build tag is set (e.g. `go build -tags
// sprawl_test`). Production builds do not include this file, so the tool
// is wholly absent from the production MCP surface.

package sprawlmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	backendpkg "github.com/dmotles/sprawl/internal/backend"
)

const injectToolName = "_test_induce_wedge"

// injectExpectedToolNames is the set of tool names contributed by the
// inject seam. Used by `TestServer_ToolsList` to keep its count
// assertion build-tag-aware.
var injectExpectedToolNames = []string{injectToolName}

// injectToolDefinitions returns the MCP tool definition for
// `_test_induce_wedge`. Appended to the canonical tool list when the
// `sprawl_test` build tag is enabled. See QUM-606.
func injectToolDefinitions() []map[string]any {
	return []map[string]any{
		{
			"name":        injectToolName,
			"description": "QUM-606 test-only tool (build-tag `sprawl_test`). Forces a deterministic terminal fault on a named agent's backend session. fault_class ∈ {subscriber_wedged, hang_timeout}; defaults to subscriber_wedged. DO NOT use in production agent prompts.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"agent_name": map[string]any{
						"type":        "string",
						"description": "Target agent name.",
					},
					"fault_class": map[string]any{
						"type":        "string",
						"description": "Fault sentinel to induce.",
						"enum":        []string{"subscriber_wedged", "hang_timeout"},
					},
				},
				"required": []string{"agent_name"},
			},
		},
	}
}

// toolInduceWedge dispatches the `_test_induce_wedge` MCP tool.
func (s *Server) toolInduceWedge(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		AgentName  string `json:"agent_name"`
		FaultClass string `json:"fault_class"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if p.AgentName == "" {
		return "", errors.New("agent_name is required")
	}
	var fault error
	switch p.FaultClass {
	case "", "subscriber_wedged":
		fault = backendpkg.ErrSubscriberWedged
	case "hang_timeout":
		fault = backendpkg.ErrHangTimeout
	default:
		return "", fmt.Errorf("unknown fault_class %q (want subscriber_wedged | hang_timeout)", p.FaultClass)
	}
	if err := s.sup.InduceTerminalFault(ctx, p.AgentName, fault); err != nil {
		return "", err
	}
	return fmt.Sprintf("Induced %s terminal fault on %s", p.FaultClass, p.AgentName), nil
}

// dispatchInjectTool routes `_test_induce_wedge` calls to toolInduceWedge.
// Returns (text, matched, err) when name matches; otherwise (-, false, nil).
func (s *Server) dispatchInjectTool(ctx context.Context, name string, args json.RawMessage) (text string, matched bool, err error) {
	if name == injectToolName {
		text, err = s.toolInduceWedge(ctx, args)
		return text, true, err
	}
	return "", false, nil
}
