package sprawlmcp

import (
	"context"
	"strings"
	"testing"
)

// QUM-1186 acceptance criterion: `delegate` and `report_status` are gone from
// the tool catalog, and calling either over MCP returns an unknown-tool error.
//
// This is asserted POSITIVELY rather than left to the absence of a passing
// test. A deletion verified only by "the old test no longer exists" is
// exactly the vacuous green this slice is meant to avoid — nothing would
// notice if a tool were quietly re-registered.
//
// deletedToolNames is deliberately a literal list, not derived from the
// catalog: deriving it from the same source it checks would make the
// assertion true by construction.
var deletedToolNames = []string{"delegate", "report_status"}

func TestDeletedTools_AbsentFromCatalog(t *testing.T) {
	srv := New(&mockSupervisor{})
	resp, err := srv.HandleMessage(context.Background(), makeJSONRPCRequest(9001, "tools/list", nil))
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	parsed := parseJSONRPCResponse(t, resp)
	result, ok := parsed["result"].(map[string]any)
	if !ok {
		t.Fatal("missing result")
	}
	toolsRaw, ok := result["tools"].([]any)
	if !ok {
		t.Fatal("missing tools array")
	}

	present := map[string]bool{}
	for _, raw := range toolsRaw {
		tool, ok := raw.(map[string]any)
		if !ok {
			t.Fatal("tool is not an object")
		}
		if name, ok := tool["name"].(string); ok {
			present[name] = true
		}
	}
	// Sanity floor: if the catalog came back empty this test would pass
	// vacuously for every deleted name.
	if len(present) < 5 {
		t.Fatalf("tool catalog has only %d entries; refusing to conclude anything from it", len(present))
	}
	for _, name := range deletedToolNames {
		if present[name] {
			t.Errorf("deleted tool %q is still advertised in tools/list", name)
		}
	}
}

func TestDeletedTools_ReturnUnknownToolError(t *testing.T) {
	for _, name := range deletedToolNames {
		t.Run(name, func(t *testing.T) {
			srv := New(&mockSupervisor{})
			msg := makeJSONRPCRequest(9002, "tools/call", map[string]any{
				"name":      name,
				"arguments": map[string]any{"agent": "alice", "task": "do X", "state": "working", "summary": "x"},
			})
			resp, err := srv.HandleMessage(context.Background(), msg)
			if err != nil {
				t.Fatalf("HandleMessage: %v", err)
			}
			parsed := parseJSONRPCResponse(t, resp)
			errObj, ok := parsed["error"].(map[string]any)
			if !ok {
				t.Fatalf("calling deleted tool %q did not produce a JSON-RPC error: %v", name, parsed)
			}
			gotMsg, _ := errObj["message"].(string)
			if !strings.Contains(gotMsg, "unknown tool") || !strings.Contains(gotMsg, name) {
				t.Errorf("error message = %q, want an unknown-tool error naming %q", gotMsg, name)
			}
		})
	}
}

// TestMessagesList_FilterEnumHasNoStatusMode guards a gap found in code review
// of QUM-1186: internal/messages.List stopped accepting filter:"status" when
// the status_change envelope class was deleted, but the messages_list tool
// schema still ADVERTISED it in its enum.
//
// That combination is worse than either half alone. An agent reading the tool
// schema would see "status" as a valid choice, call it, and get a hard
// "invalid filter" error from a mode the tool itself offered — the tool lying
// about its own surface. Asserted against the live catalog, so the schema and
// the implementation cannot drift apart again.
func TestMessagesList_FilterEnumHasNoStatusMode(t *testing.T) {
	for _, def := range baseToolDefinitions() {
		if def["name"] != "messages_list" {
			continue
		}
		schema, ok := def["inputSchema"].(map[string]any)
		if !ok {
			t.Fatal("messages_list has no inputSchema")
		}
		props, ok := schema["properties"].(map[string]any)
		if !ok {
			t.Fatal("messages_list inputSchema has no properties")
		}
		filter, ok := props["filter"].(map[string]any)
		if !ok {
			t.Fatal("messages_list has no filter property")
		}
		enum, ok := filter["enum"].([]string)
		if !ok {
			t.Fatalf("messages_list filter enum is %T, want []string", filter["enum"])
		}
		if len(enum) == 0 {
			t.Fatal("messages_list filter enum is empty; refusing to conclude anything from it")
		}
		for _, v := range enum {
			if v == "status" {
				t.Errorf("messages_list still advertises filter=%q, but internal/messages.List rejects it", v)
			}
		}
		// Positive half: the modes that DO survive must still be advertised,
		// so this test cannot pass by the enum having been emptied.
		for _, want := range []string{"all", "unread", "read", "archived"} {
			found := false
			for _, v := range enum {
				if v == want {
					found = true
				}
			}
			if !found {
				t.Errorf("messages_list filter enum lost the surviving mode %q", want)
			}
		}
		return
	}
	t.Fatal("messages_list not found in the tool catalog")
}
