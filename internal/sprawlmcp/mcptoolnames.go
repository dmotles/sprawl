package sprawlmcp

// MCPToolNames returns the Claude-addressable MCP tool names (with
// mcp__sprawl-ops__ prefix) for all tools defined in toolDefinitions().
func MCPToolNames() []string {
	defs := toolDefinitions()
	names := make([]string, len(defs))
	for i, def := range defs {
		names[i] = "mcp__sprawl-ops__" + def["name"].(string)
	}
	return names
}
