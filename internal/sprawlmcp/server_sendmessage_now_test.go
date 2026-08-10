// QUM-1186 lane 2 / D6: the send_message `interrupt` parameter is renamed to
// `now`. Parameter rename only — the preemption semantics (ancestor gate,
// queue-jump, best-effort mid-turn preemption per QUM-549) are unchanged, and
// agentloop.ClassInterrupt keeps its name because it is embedded in on-disk
// queue filenames.
//
// The legacy-key test below is the load-bearing one. encoding/json silently
// DROPS an unknown property, so without a hard error a stale caller passing
// `interrupt: true` gets `now == false` — an urgent message quietly downgraded
// to cooperative, with nobody told. That is the same silent-false-claim class
// QUM-1185 exists to remove, and it would have been introduced by the fix for
// it. The same trap is already documented on the `agent_name` shim in server.go.
//
// MUTATION LOG.
//
//	C4  leave an `interrupt` property in the schema alongside `now`.
//	    → SchemaAdvertisesNow_AndNotInterrupt FAILED: "inputSchema still
//	      advertises 'interrupt'". A presence-only assertion would have passed.
//	C5  delete the now= guidance from the description without adding a
//	    replacement.
//	    → same test FAILED: "description never teaches `now=`". The
//	      absence-only half passed happily, which is why both directions are
//	      asserted.
//	C6  red-first: the legacy-key and now-forwarding tests were written before
//	    the handler existed and failed with the schema still carrying
//	    `interrupt`.
package sprawlmcp

import (
	"context"
	"strings"
	"testing"
)

// TestToolSendMessage_SchemaAdvertisesNow_AndNotInterrupt pins BOTH directions.
//
// The absence half is not redundant: a schema that carries `now` AND a leftover
// `interrupt` satisfies a presence-only assertion while still advertising a
// parameter the handler no longer reads. That is precisely lane 1's
// "the deletion reaches the implementation but not the advertisement".
func TestToolSendMessage_SchemaAdvertisesNow_AndNotInterrupt(t *testing.T) {
	srv := New(&mockSupervisorSendMessage{})
	resp, err := srv.HandleMessage(context.Background(), makeJSONRPCRequest(90, "tools/list", nil))
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	parsed := parseJSONRPCResponse(t, resp)
	result := parsed["result"].(map[string]any)
	tools := result["tools"].([]any)

	var found map[string]any
	for _, tAny := range tools {
		tm := tAny.(map[string]any)
		if tm["name"] == "send_message" {
			found = tm
			break
		}
	}
	if found == nil {
		t.Fatal("send_message missing from tools/list")
	}
	schema := found["inputSchema"].(map[string]any)
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		// Without this, props is nil and the `interrupt`-is-absent assertion
		// below passes vacuously.
		t.Fatalf("send_message inputSchema: properties missing or not an object: %v", schema["properties"])
	}

	if _, ok := props["now"]; !ok {
		t.Error("send_message inputSchema: missing 'now' property")
	}
	if _, ok := props["interrupt"]; ok {
		t.Error("send_message inputSchema still advertises 'interrupt' — it was renamed to 'now'; leaving both makes the schema teach a parameter the handler does not read")
	}

	required, _ := schema["required"].([]any)
	gotRequired := map[string]bool{}
	for _, r := range required {
		gotRequired[r.(string)] = true
	}
	if gotRequired["now"] {
		t.Error("send_message required: 'now' must be optional (defaults to false)")
	}
	// Asserted alongside the above: a schema that dropped `required` entirely
	// would satisfy "now is optional" while silently making `to` and `body`
	// optional too.
	if !gotRequired["to"] || !gotRequired["body"] {
		t.Errorf("send_message required = %v, want both 'to' and 'body' still required", gotRequired)
	}

	// The prose is a contract surface too: an agent reads the description as
	// truth. A description still saying interrupt= while the schema says now=
	// is the advertisement half of the same defect.
	desc, _ := found["description"].(string)
	if strings.Contains(desc, "interrupt=") {
		t.Errorf("send_message description still teaches `interrupt=`:\n%s", desc)
	}
	// Both directions. Absence alone is satisfied by DELETING the guidance, which
	// would leave the agent with a `now` parameter and no account of what it does.
	if !strings.Contains(desc, "now=") {
		t.Errorf("send_message description never teaches `now=` — removing the old guidance without adding the new one leaves the parameter undocumented:\n%s", desc)
	}
}

// TestToolSendMessage_NowTrue_ForwardsPreemption pins that the renamed
// parameter still reaches the supervisor — a rename that dropped the wiring
// would leave every send cooperative and no other test here would notice.
func TestToolSendMessage_NowTrue_ForwardsPreemption(t *testing.T) {
	mock := &mockSupervisorSendMessage{}
	srv := New(mock)

	msg := makeJSONRPCRequest(91, "tools/call", map[string]any{
		"name":      "send_message",
		"arguments": map[string]any{"to": "alice", "body": "stop", "now": true},
	})
	if _, err := srv.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	if mock.sendMessageCalls != 1 {
		t.Fatalf("SendMessage calls = %d, want 1", mock.sendMessageCalls)
	}
	if !mock.sendMessageNow {
		t.Error("now = false at the supervisor, want true — the renamed parameter is not wired through")
	}
}

// TestToolSendMessage_LegacyInterruptKey_HardErrors is the anti-silent-downgrade
// assertion, and the reason it is a hard error rather than a tolerated synonym.
//
// Without it, `{"interrupt": true}` unmarshals into a struct with no such field,
// `now` stays false, and the send silently becomes cooperative. The call
// SUCCEEDS, so neither the caller nor any existing test can tell. The error must
// name the rename so a stale caller can fix itself.
func TestToolSendMessage_LegacyInterruptKey_HardErrors(t *testing.T) {
	mock := &mockSupervisorSendMessage{}
	srv := New(mock)

	msg := makeJSONRPCRequest(92, "tools/call", map[string]any{
		"name":      "send_message",
		"arguments": map[string]any{"to": "alice", "body": "stop", "interrupt": true},
	})
	resp, err := srv.HandleMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	parsed := parseJSONRPCResponse(t, resp)
	result, _ := parsed["result"].(map[string]any)
	if result == nil {
		t.Fatalf("no result in response: %v", parsed)
	}
	if isErr, _ := result["isError"].(bool); !isErr {
		t.Errorf("legacy `interrupt` key did not set isError; result: %v", result)
	}
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("no content in result: %v", result)
	}
	text, _ := content[0].(map[string]any)["text"].(string)
	// Pin the rename ARROW, not the two words separately. "interrupt" and "now"
	// as independent substrings are satisfied by a generic
	// `unknown parameter: interrupt` — because "u-n-k-N-O-W-n" contains "now" —
	// which teaches the stale caller nothing. This is the file's load-bearing
	// assertion, so it must not be one that cannot fail.
	if !strings.Contains(text, "renamed to `now`") {
		t.Errorf("legacy `interrupt` key did not produce an error naming the rename (want an ``interrupt` was renamed to `now`` style message); got: %s", text)
	}

	// The behavioural half. An error message that is returned but does not stop
	// the send would still deliver a silently-downgraded message.
	if mock.sendMessageCalls != 0 {
		t.Errorf("SendMessage was called %d times for a request using the legacy `interrupt` key, want 0 — accepting it downgrades an urgent message to cooperative with nobody told", mock.sendMessageCalls)
	}
}

// TestToolSendMessage_LegacyInterruptKey_NonBool_AlsoHardErrors closes the hole
// a *bool-typed guard leaves open.
//
// Decoding `interrupt` into a *bool FAILS on `"interrupt": "true"` or
// `"interrupt": 1`, so a type-checking guard sees err != nil and skips — while
// the primary unmarshal succeeded (unknown keys are dropped) and the send
// proceeds with now==false. That is the same silent urgency downgrade the guard
// exists to prevent, reached through a type mismatch instead of a name mismatch.
// Agents do emit weakly-typed payloads.
func TestToolSendMessage_LegacyInterruptKey_NonBool_AlsoHardErrors(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
	}{
		{"string", "true"},
		{"number", 1},
		{"null", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockSupervisorSendMessage{}
			srv := New(mock)

			msg := makeJSONRPCRequest(94, "tools/call", map[string]any{
				"name":      "send_message",
				"arguments": map[string]any{"to": "alice", "body": "stop", "interrupt": tc.value},
			})
			resp, err := srv.HandleMessage(context.Background(), msg)
			if err != nil {
				t.Fatalf("HandleMessage: %v", err)
			}
			parsed := parseJSONRPCResponse(t, resp)
			result, _ := parsed["result"].(map[string]any)
			if isErr, _ := result["isError"].(bool); !isErr {
				t.Errorf("legacy `interrupt` with a %s value was accepted; result: %v", tc.name, result)
			}
			if mock.sendMessageCalls != 0 {
				t.Errorf("SendMessage called %d times for a legacy `interrupt` key of type %s, want 0 — a non-bool must not slip past the guard and downgrade the send", mock.sendMessageCalls, tc.name)
			}
		})
	}
}

// TestToolSendMessage_NoNowKey_DefaultsToCooperative is the negative control for
// the legacy-key test: a normal call without `now` must still succeed. Without
// it, an over-strict rejection (e.g. refusing any request lacking `now`) would
// pass every assertion above.
func TestToolSendMessage_NoNowKey_DefaultsToCooperative(t *testing.T) {
	mock := &mockSupervisorSendMessage{}
	srv := New(mock)

	msg := makeJSONRPCRequest(93, "tools/call", map[string]any{
		"name":      "send_message",
		"arguments": map[string]any{"to": "alice", "body": "hello"},
	})
	if _, err := srv.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	if mock.sendMessageCalls != 1 {
		t.Fatalf("SendMessage calls = %d, want 1", mock.sendMessageCalls)
	}
	if mock.sendMessageNow {
		t.Error("now = true when the key was omitted, want false")
	}
}
