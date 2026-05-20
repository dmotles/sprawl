//go:build !sprawl_test

// Non-test default: `_test_induce_wedge` is absent from the MCP tool
// surface. The QUM-606 fault-injection seam is only compiled in when the
// `sprawl_test` build tag is enabled. See tools_inject_on.go.

package sprawlmcp

import (
	"context"
	"encoding/json"
)

// injectToolDefinitions returns no tools in production builds.
func injectToolDefinitions() []map[string]any { return nil }

// injectExpectedToolNames is empty in production builds; used by
// `TestServer_ToolsList` to keep its count assertion build-tag-aware.
var injectExpectedToolNames = []string{}

// dispatchInjectTool always returns matched=false in production builds.
// Returns (text, matched, err) with error last per revive's error-return lint.
func (s *Server) dispatchInjectTool(_ context.Context, _ string, _ json.RawMessage) (text string, matched bool, err error) {
	return "", false, nil
}
