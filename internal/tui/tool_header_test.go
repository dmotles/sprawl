package tui

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// QUM-419: per-tool header formatters extract (mainArg, kvPairs) from raw
// tool_use input JSON. Table-driven coverage for every dedicated formatter
// plus edge cases (empty, invalid, missing fields, unknown tool).
func TestFormatToolHeader(t *testing.T) {
	cases := []struct {
		name     string
		tool     string
		input    string
		wantMain string
		wantKV   []KVPair
	}{
		{
			name:     "Bash command only",
			tool:     "Bash",
			input:    `{"command":"ls -la /tmp"}`,
			wantMain: `"ls -la /tmp"`,
		},
		{
			name:     "Bash with description and timeout",
			tool:     "Bash",
			input:    `{"command":"make test","description":"run tests","timeout":120000}`,
			wantMain: `"make test"`,
			wantKV: []KVPair{
				{Key: "description", Value: `"run tests"`},
				{Key: "timeout", Value: "120000"},
			},
		},
		{
			name:     "Bash multi-line command collapses newlines",
			tool:     "Bash",
			input:    `{"command":"a\nb\nc"}`,
			wantMain: `"a ; b ; c"`,
		},
		{
			name:     "Bash missing command",
			tool:     "Bash",
			input:    `{}`,
			wantMain: "",
		},
		{
			name:     "Read file_path only",
			tool:     "Read",
			input:    `{"file_path":"/tmp/main.go"}`,
			wantMain: "/tmp/main.go",
		},
		{
			name:     "Read with offset and limit",
			tool:     "Read",
			input:    `{"file_path":"/tmp/main.go","offset":100,"limit":80}`,
			wantMain: "/tmp/main.go",
			wantKV: []KVPair{
				{Key: "offset", Value: "100"},
				{Key: "limit", Value: "80"},
			},
		},
		{
			name:     "View aliases Read",
			tool:     "View",
			input:    `{"file_path":"/etc/hosts","limit":40}`,
			wantMain: "/etc/hosts",
			wantKV:   []KVPair{{Key: "limit", Value: "40"}},
		},
		{
			name:     "Edit with replace_all",
			tool:     "Edit",
			input:    `{"file_path":"/tmp/a","old_string":"x","new_string":"y","replace_all":true}`,
			wantMain: "/tmp/a",
			wantKV:   []KVPair{{Key: "replace_all", Value: "true"}},
		},
		{
			name:     "Edit no replace_all",
			tool:     "Edit",
			input:    `{"file_path":"/tmp/a","old_string":"x","new_string":"y"}`,
			wantMain: "/tmp/a",
		},
		{
			name:     "MultiEdit edits count",
			tool:     "MultiEdit",
			input:    `{"file_path":"/tmp/x","edits":[{},{},{}]}`,
			wantMain: "/tmp/x",
			wantKV:   []KVPair{{Key: "edits", Value: "3"}},
		},
		{
			name:     "Write file_path only",
			tool:     "Write",
			input:    `{"file_path":"/tmp/out.txt","content":"hello"}`,
			wantMain: "/tmp/out.txt",
		},
		{
			name:     "Glob pattern only",
			tool:     "Glob",
			input:    `{"pattern":"**/*.go"}`,
			wantMain: `"**/*.go"`,
		},
		{
			name:     "Glob with path",
			tool:     "Glob",
			input:    `{"pattern":"*.go","path":"internal"}`,
			wantMain: `"*.go"`,
			wantKV:   []KVPair{{Key: "path", Value: "internal"}},
		},
		{
			name:     "Grep pattern with output_mode and -i",
			tool:     "Grep",
			input:    `{"pattern":"TODO","output_mode":"content","-i":true,"path":"internal/tui"}`,
			wantMain: `"TODO"`,
			wantKV: []KVPair{
				{Key: "path", Value: "internal/tui"},
				{Key: "output_mode", Value: "content"},
				{Key: "-i", Value: "true"},
			},
		},
		{
			name:     "LS path with ignore",
			tool:     "LS",
			input:    `{"path":"/tmp","ignore":["node_modules","dist"]}`,
			wantMain: "/tmp",
			wantKV:   []KVPair{{Key: "ignore", Value: "2"}},
		},
		{
			name:     "Task description with prompt char count",
			tool:     "Task",
			input:    `{"description":"audit code","subagent_type":"oracle","prompt":"hello"}`,
			wantMain: `"audit code"`,
			wantKV: []KVPair{
				{Key: "subagent_type", Value: "oracle"},
				{Key: "prompt", Value: "5c"},
			},
		},
		{
			name:     "Agent aliases Task",
			tool:     "Agent",
			input:    `{"description":"plan","prompt":"abc"}`,
			wantMain: `"plan"`,
			wantKV:   []KVPair{{Key: "prompt", Value: "3c"}},
		},
		{
			name:     "WebFetch url and prompt",
			tool:     "WebFetch",
			input:    `{"url":"https://example.com","prompt":"summarize"}`,
			wantMain: "https://example.com",
			wantKV:   []KVPair{{Key: "prompt", Value: "9c"}},
		},
		{
			name:     "WebSearch query",
			tool:     "WebSearch",
			input:    `{"query":"go generics tutorial"}`,
			wantMain: `"go generics tutorial"`,
		},
		{
			name:     "ToolSearch with max_results",
			tool:     "ToolSearch",
			input:    `{"query":"slack","max_results":5}`,
			wantMain: `"slack"`,
			wantKV:   []KVPair{{Key: "max_results", Value: "5"}},
		},
		{
			name:     "NotebookRead path and cell_id",
			tool:     "NotebookRead",
			input:    `{"notebook_path":"/tmp/x.ipynb","cell_id":"abc"}`,
			wantMain: "/tmp/x.ipynb",
			wantKV:   []KVPair{{Key: "cell_id", Value: "abc"}},
		},
		{
			name:     "MCP sprawl send_message",
			tool:     "mcp__sprawl__send_message",
			input:    `{"to":"weave","body":"hi","interrupt":false}`,
			wantMain: "weave",
			wantKV:   []KVPair{{Key: "body", Value: "hi"}},
		},
		{
			name:     "MCP linear save_issue",
			tool:     "mcp__linear__save_issue",
			input:    `{"id":"QUM-419","state":"In Progress"}`,
			wantMain: "QUM-419",
			wantKV:   []KVPair{{Key: "state", Value: "In Progress"}}, // actually no — "In Progress" has space → quoted
		},
		{
			name:     "Empty input returns zero",
			tool:     "Bash",
			input:    ``,
			wantMain: "",
		},
		{
			name:     "Invalid JSON returns zero",
			tool:     "Bash",
			input:    `{not json`,
			wantMain: "",
		},
		{
			name:     "Unknown tool falls back to generic",
			tool:     "MysteryTool",
			input:    `{"alpha":"a","beta":"b"}`,
			wantMain: "",
			wantKV: []KVPair{
				{Key: "alpha", Value: "a"},
				{Key: "beta", Value: "b"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotMain, gotKV := FormatToolHeader(tc.tool, json.RawMessage(tc.input))
			if gotMain != tc.wantMain {
				t.Errorf("mainArg = %q, want %q", gotMain, tc.wantMain)
			}
			// The "In Progress" case in the table is intentionally lenient:
			// scalarString quotes whitespace-containing strings, so the
			// expected value embeds the quotes. The hand-written want above
			// reflects that.
			if tc.name == "MCP linear save_issue" {
				want := []KVPair{{Key: "state", Value: `"In Progress"`}}
				if !reflect.DeepEqual(gotKV, want) {
					t.Errorf("kvPairs = %#v, want %#v", gotKV, want)
				}
				return
			}
			if !reflect.DeepEqual(gotKV, tc.wantKV) {
				t.Errorf("kvPairs = %#v, want %#v", gotKV, tc.wantKV)
			}
		})
	}
}

// QUM-419: FormatToolDisplayName collapses MCP-prefixed tool names to their
// terminal segment so the header reads naturally.
func TestFormatToolDisplayName(t *testing.T) {
	cases := map[string]string{
		"Bash":                      "Bash",
		"mcp__sprawl__send_message": "send_message",
		"mcp__linear__get_issue":    "get_issue",
		"mcp__weird":                "mcp__weird", // not enough segments → verbatim
	}
	for in, want := range cases {
		if got := FormatToolDisplayName(in); got != want {
			t.Errorf("FormatToolDisplayName(%q) = %q, want %q", in, got, want)
		}
	}
}

// QUM-419: RenderKVPairs renders the trailing `(k1=v1, k2=v2)` segment.
func TestRenderKVPairs(t *testing.T) {
	if got := RenderKVPairs(nil); got != "" {
		t.Errorf("RenderKVPairs(nil) = %q, want empty", got)
	}
	got := RenderKVPairs([]KVPair{{Key: "a", Value: "1"}, {Key: "b", Value: "two"}})
	want := "(a=1, b=two)"
	if got != want {
		t.Errorf("RenderKVPairs(...) = %q, want %q", got, want)
	}
}

// QUM-419: scalarString must not crash on nil/array/object values — the
// generic + MCP formatters defensively skip non-scalars.
func TestScalarString_NonScalarReturnsEmpty(t *testing.T) {
	for _, v := range []any{nil, []any{1, 2}, map[string]any{"x": 1}} {
		if got := scalarString(v); got != "" {
			t.Errorf("scalarString(%v) = %q, want empty", v, got)
		}
	}
}

// QUM-419: helper to surface formatter implementation drift — every entry in
// the formatter registry must produce a non-zero return for at least one
// canonical input. Guards against an empty switch arm landing in the registry.
func TestFormatToolHeader_RegistryCoverage(t *testing.T) {
	canonicalInputs := map[string]string{
		"Bash":         `{"command":"x"}`,
		"Read":         `{"file_path":"/x"}`,
		"View":         `{"file_path":"/x"}`,
		"Edit":         `{"file_path":"/x"}`,
		"MultiEdit":    `{"file_path":"/x"}`,
		"Write":        `{"file_path":"/x"}`,
		"Glob":         `{"pattern":"*"}`,
		"Grep":         `{"pattern":"x"}`,
		"LS":           `{"path":"/x"}`,
		"Task":         `{"description":"x"}`,
		"Agent":        `{"description":"x"}`,
		"WebFetch":     `{"url":"https://x"}`,
		"WebSearch":    `{"query":"x"}`,
		"NotebookRead": `{"notebook_path":"/x"}`,
		"NotebookEdit": `{"notebook_path":"/x"}`,
		"ToolSearch":   `{"query":"x"}`,
	}
	for name := range toolFormatters {
		input, ok := canonicalInputs[name]
		if !ok {
			t.Errorf("registry has %q but no canonical input in test", name)
			continue
		}
		main, _ := FormatToolHeader(name, json.RawMessage(input))
		if strings.TrimSpace(main) == "" {
			t.Errorf("formatter %q produced empty mainArg for canonical input %s", name, input)
		}
	}
}
