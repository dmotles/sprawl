package tui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// KVPair is a single key=value parameter rendered after the main argument in a
// per-tool header line (QUM-419). Empty Value means the formatter chose to
// drop the pair; callers should ignore those.
type KVPair struct {
	Key   string
	Value string
}

// toolHeaderFormatter takes a parsed input map and returns the main argument
// string plus an ordered list of secondary k=v pairs. The renderer applies
// width-budgeting on top: if rendering kvPairs would shrink mainArg below
// MinMainArgCells, the pairs are dropped.
type toolHeaderFormatter func(input map[string]any) (string, []KVPair)

// MinMainArgCells is the threshold from the Crush implementation
// (crush/internal/ui/chat/tools.go toolParamList): kvPairs are dropped if
// rendering them would leave fewer than this many cells for the main arg.
const MinMainArgCells = 30

// toolFormatters is the registry of per-tool header formatters. Tools not in
// the map fall through to the generic formatter (formatGeneric).
var toolFormatters = map[string]toolHeaderFormatter{
	"Bash":         formatBash,
	"Read":         formatReadView,
	"View":         formatReadView,
	"Edit":         formatEdit,
	"MultiEdit":    formatMultiEdit,
	"Write":        formatWrite,
	"Glob":         formatGlob,
	"Grep":         formatGrep,
	"LS":           formatLS,
	"Task":         formatTask,
	"Agent":        formatTask,
	"WebFetch":     formatWebFetch,
	"WebSearch":    formatWebSearch,
	"NotebookRead": formatNotebookRead,
	"NotebookEdit": formatNotebookEdit,
	"ToolSearch":   formatToolSearch,
}

// FormatToolHeader returns the (mainArg, params) pair the renderer uses to
// build the compact per-tool header line. raw may be nil/empty; in that case
// both return values are zero so the renderer falls back to "tool name only".
//
// MCP-prefixed tools (mcp__<server>__<method>) get a dedicated path that
// shortens the display name to the method segment and surfaces a couple of
// the most-useful scalar keys.
func FormatToolHeader(toolName string, raw json.RawMessage) (string, []KVPair) {
	if len(raw) == 0 {
		return "", nil
	}
	var input map[string]any
	if err := json.Unmarshal(raw, &input); err != nil {
		return "", nil
	}
	if fn, ok := toolFormatters[toolName]; ok {
		return fn(input)
	}
	if strings.HasPrefix(toolName, "mcp__") {
		return formatMCP(input)
	}
	return formatGeneric(input)
}

// FormatToolDisplayName returns the tool name as it should appear in the
// header. MCP tools render as `<server>/<action>` so the server segment
// (linear/sprawl/…) is preserved alongside the action — e.g.
// `mcp__linear__save_issue` reads as `linear/save_issue` (QUM-589). The
// parser is structural: any `mcp__<server>__<action>` works, including
// future servers. Malformed names (fewer than 3 segments) and non-MCP tools
// pass through verbatim.
func FormatToolDisplayName(toolName string) string {
	if strings.HasPrefix(toolName, "mcp__") {
		parts := strings.Split(toolName, "__")
		if len(parts) >= 3 {
			return parts[1] + "/" + parts[len(parts)-1]
		}
	}
	return toolName
}

// --- per-tool formatters ---

func formatBash(input map[string]any) (string, []KVPair) {
	cmd, _ := input["command"].(string)
	// Collapse newlines so multi-line scripts read on one line. Rendered
	// unquoted (QUM-796 #2) for the cleaner inline header look.
	if cmd != "" {
		cmd = strings.ReplaceAll(cmd, "\n", " ; ")
	}
	var kv []KVPair
	if desc, ok := input["description"].(string); ok && desc != "" {
		kv = append(kv, KVPair{Key: "description", Value: strconv.Quote(desc)})
	}
	if t := numericValue(input["timeout"]); t != "" {
		kv = append(kv, KVPair{Key: "timeout", Value: t})
	}
	if b, ok := input["run_in_background"].(bool); ok && b {
		kv = append(kv, KVPair{Key: "run_in_background", Value: "true"})
	}
	return cmd, kv
}

func formatReadView(input map[string]any) (string, []KVPair) {
	path, _ := input["file_path"].(string)
	var kv []KVPair
	if v := numericValue(input["offset"]); v != "" {
		kv = append(kv, KVPair{Key: "offset", Value: v})
	}
	if v := numericValue(input["limit"]); v != "" {
		kv = append(kv, KVPair{Key: "limit", Value: v})
	}
	if p, ok := input["pages"].(string); ok && p != "" {
		kv = append(kv, KVPair{Key: "pages", Value: p})
	}
	return path, kv
}

func formatEdit(input map[string]any) (string, []KVPair) {
	path, _ := input["file_path"].(string)
	var kv []KVPair
	if b, ok := input["replace_all"].(bool); ok && b {
		kv = append(kv, KVPair{Key: "replace_all", Value: "true"})
	}
	return path, kv
}

func formatMultiEdit(input map[string]any) (string, []KVPair) {
	path, _ := input["file_path"].(string)
	var kv []KVPair
	if edits, ok := input["edits"].([]any); ok {
		kv = append(kv, KVPair{Key: "edits", Value: strconv.Itoa(len(edits))})
	}
	return path, kv
}

func formatWrite(input map[string]any) (string, []KVPair) {
	path, _ := input["file_path"].(string)
	return path, nil
}

func formatGlob(input map[string]any) (string, []KVPair) {
	pattern, _ := input["pattern"].(string)
	if pattern != "" {
		pattern = strconv.Quote(pattern)
	}
	var kv []KVPair
	if p, ok := input["path"].(string); ok && p != "" {
		kv = append(kv, KVPair{Key: "path", Value: p})
	}
	return pattern, kv
}

func formatGrep(input map[string]any) (string, []KVPair) {
	pattern, _ := input["pattern"].(string)
	if pattern != "" {
		pattern = strconv.Quote(pattern)
	}
	var kv []KVPair
	if p, ok := input["path"].(string); ok && p != "" {
		kv = append(kv, KVPair{Key: "path", Value: p})
	}
	if g, ok := input["glob"].(string); ok && g != "" {
		kv = append(kv, KVPair{Key: "glob", Value: g})
	}
	if t, ok := input["type"].(string); ok && t != "" {
		kv = append(kv, KVPair{Key: "type", Value: t})
	}
	if m, ok := input["output_mode"].(string); ok && m != "" {
		kv = append(kv, KVPair{Key: "output_mode", Value: m})
	}
	if b, ok := input["-i"].(bool); ok && b {
		kv = append(kv, KVPair{Key: "-i", Value: "true"})
	}
	if v := numericValue(input["-C"]); v != "" {
		kv = append(kv, KVPair{Key: "-C", Value: v})
	}
	if v := numericValue(input["head_limit"]); v != "" {
		kv = append(kv, KVPair{Key: "head_limit", Value: v})
	}
	if b, ok := input["multiline"].(bool); ok && b {
		kv = append(kv, KVPair{Key: "multiline", Value: "true"})
	}
	return pattern, kv
}

func formatLS(input map[string]any) (string, []KVPair) {
	path, _ := input["path"].(string)
	var kv []KVPair
	if ig, ok := input["ignore"].([]any); ok && len(ig) > 0 {
		kv = append(kv, KVPair{Key: "ignore", Value: strconv.Itoa(len(ig))})
	}
	return path, kv
}

func formatTask(input map[string]any) (string, []KVPair) {
	main, _ := input["description"].(string)
	if main == "" {
		main, _ = input["subagent_type"].(string)
	}
	if main != "" {
		main = strconv.Quote(main)
	}
	var kv []KVPair
	if st, ok := input["subagent_type"].(string); ok && st != "" {
		kv = append(kv, KVPair{Key: "subagent_type", Value: st})
	}
	if p, ok := input["prompt"].(string); ok && p != "" {
		kv = append(kv, KVPair{Key: "prompt", Value: fmt.Sprintf("%dc", len([]rune(p)))})
	}
	return main, kv
}

func formatWebFetch(input map[string]any) (string, []KVPair) {
	url, _ := input["url"].(string)
	var kv []KVPair
	if p, ok := input["prompt"].(string); ok && p != "" {
		kv = append(kv, KVPair{Key: "prompt", Value: fmt.Sprintf("%dc", len([]rune(p)))})
	}
	return url, kv
}

func formatWebSearch(input map[string]any) (string, []KVPair) {
	q, _ := input["query"].(string)
	if q != "" {
		q = strconv.Quote(q)
	}
	var kv []KVPair
	if d, ok := input["allowed_domains"].([]any); ok && len(d) > 0 {
		kv = append(kv, KVPair{Key: "allowed_domains", Value: strconv.Itoa(len(d))})
	}
	if d, ok := input["blocked_domains"].([]any); ok && len(d) > 0 {
		kv = append(kv, KVPair{Key: "blocked_domains", Value: strconv.Itoa(len(d))})
	}
	return q, kv
}

func formatNotebookRead(input map[string]any) (string, []KVPair) {
	p, _ := input["notebook_path"].(string)
	var kv []KVPair
	if id, ok := input["cell_id"].(string); ok && id != "" {
		kv = append(kv, KVPair{Key: "cell_id", Value: id})
	}
	return p, kv
}

func formatNotebookEdit(input map[string]any) (string, []KVPair) {
	p, _ := input["notebook_path"].(string)
	var kv []KVPair
	if id, ok := input["cell_id"].(string); ok && id != "" {
		kv = append(kv, KVPair{Key: "cell_id", Value: id})
	}
	if m, ok := input["edit_mode"].(string); ok && m != "" {
		kv = append(kv, KVPair{Key: "edit_mode", Value: m})
	}
	if t, ok := input["cell_type"].(string); ok && t != "" {
		kv = append(kv, KVPair{Key: "cell_type", Value: t})
	}
	return p, kv
}

func formatToolSearch(input map[string]any) (string, []KVPair) {
	q, _ := input["query"].(string)
	if q != "" {
		q = strconv.Quote(q)
	}
	var kv []KVPair
	if v := numericValue(input["max_results"]); v != "" {
		kv = append(kv, KVPair{Key: "max_results", Value: v})
	}
	return q, kv
}

// formatMCP surfaces the top scalar params from an MCP tool call. Used for
// every `mcp__*` tool that doesn't have a dedicated formatter. We hand-pick a
// short priority list of common keys so the header reads naturally; anything
// else falls back to the generic path so we never silently render a blank
// header for a tool we don't know about.
func formatMCP(input map[string]any) (string, []KVPair) {
	priorityMain := []string{"to", "agent", "name", "id", "query", "url"}
	priorityKV := []string{"subject", "interrupt", "state", "summary", "title", "body"}
	main := ""
	for _, k := range priorityMain {
		if v, ok := input[k].(string); ok && v != "" {
			main = v
			break
		}
	}
	var kv []KVPair
	for _, k := range priorityKV {
		if _, ok := input[k]; !ok {
			continue
		}
		// Skip default-false booleans — they're noise (e.g. `interrupt=false`
		// on every send_message). Other falses (rare) get the same treatment;
		// the caller can read the full payload via Ctrl+O if they care.
		if b, ok := input[k].(bool); ok && !b {
			continue
		}
		val := scalarString(input[k])
		if val == "" {
			continue
		}
		kv = append(kv, KVPair{Key: k, Value: val})
		if len(kv) >= 2 {
			break
		}
	}
	if main == "" && len(kv) == 0 {
		return formatGeneric(input)
	}
	return main, kv
}

// formatGeneric is the last-resort formatter for tools without a dedicated
// implementation. It picks up to three short scalar key/value pairs from the
// input map (sorted by key so output is deterministic for tests).
func formatGeneric(input map[string]any) (string, []KVPair) {
	keys := make([]string, 0, len(input))
	for k := range input {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var kv []KVPair
	for _, k := range keys {
		val := scalarString(input[k])
		if val == "" {
			continue
		}
		kv = append(kv, KVPair{Key: k, Value: val})
		if len(kv) >= 3 {
			break
		}
	}
	return "", kv
}

// numericValue returns a decimal string representation for JSON numeric values
// (always parsed as float64 by encoding/json). Returns "" for zero / missing /
// non-numeric so formatters can skip the pair entirely.
func numericValue(v any) string {
	switch n := v.(type) {
	case float64:
		if n == 0 {
			return ""
		}
		if n == float64(int64(n)) {
			return strconv.FormatInt(int64(n), 10)
		}
		return strconv.FormatFloat(n, 'g', -1, 64)
	case int:
		if n == 0 {
			return ""
		}
		return strconv.Itoa(n)
	}
	return ""
}

// scalarString renders a JSON scalar as a compact string. Strings are quoted
// only when they contain whitespace; numbers, bools come through verbatim.
// Returns "" for nil, empty strings, arrays, or objects so the caller can
// skip the pair.
func scalarString(v any) string {
	switch x := v.(type) {
	case string:
		if x == "" {
			return ""
		}
		if strings.ContainsAny(x, " \t\n") {
			return strconv.Quote(x)
		}
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	}
	return ""
}

// RenderKVPairs joins kv into the "(k1=v1, k2=v2)" suffix the renderer
// appends after the main argument. Returns "" when kv is empty so callers
// can cheaply skip an empty suffix.
func RenderKVPairs(kv []KVPair) string {
	if len(kv) == 0 {
		return ""
	}
	parts := make([]string, 0, len(kv))
	for _, p := range kv {
		parts = append(parts, p.Key+"="+p.Value)
	}
	return "(" + strings.Join(parts, ", ") + ")"
}
