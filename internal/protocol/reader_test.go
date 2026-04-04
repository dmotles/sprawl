package protocol

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestReaderSystemInit(t *testing.T) {
	input := `{"type":"system","subtype":"init","session_id":"sess-1","cwd":"/tmp","tools":["Bash","Read"],"model":"claude-sonnet-4-6","permissionMode":"bypassPermissions","claude_code_version":"2.1.87","apiKeySource":"user","uuid":"uuid-1"}` + "\n"
	r := NewReader(strings.NewReader(input))

	msg, err := r.Next()
	if err != nil {
		t.Fatalf("Next() error: %v", err)
	}
	if msg == nil {
		t.Fatal("Next() returned nil message")
	}
	if msg.Type != "system" {
		t.Errorf("Type = %q, want %q", msg.Type, "system")
	}
	if msg.Subtype != "init" {
		t.Errorf("Subtype = %q, want %q", msg.Subtype, "init")
	}
	if msg.UUID != "uuid-1" {
		t.Errorf("UUID = %q, want %q", msg.UUID, "uuid-1")
	}
	if msg.SessionID != "sess-1" {
		t.Errorf("SessionID = %q, want %q", msg.SessionID, "sess-1")
	}
	if msg.Raw == nil {
		t.Fatal("Raw is nil, expected populated json.RawMessage")
	}

	var init SystemInit
	if err := ParseAs(msg, &init); err != nil {
		t.Fatalf("ParseAs(SystemInit) error: %v", err)
	}
	if init.CWD != "/tmp" {
		t.Errorf("CWD = %q, want %q", init.CWD, "/tmp")
	}
	if len(init.Tools) != 2 || init.Tools[0] != "Bash" || init.Tools[1] != "Read" {
		t.Errorf("Tools = %v, want [Bash Read]", init.Tools)
	}
	if init.Model != "claude-sonnet-4-6" {
		t.Errorf("Model = %q, want %q", init.Model, "claude-sonnet-4-6")
	}
	if init.ClaudeVersion != "2.1.87" {
		t.Errorf("ClaudeVersion = %q, want %q", init.ClaudeVersion, "2.1.87")
	}
	if init.PermissionMode != "bypassPermissions" {
		t.Errorf("PermissionMode = %q, want %q", init.PermissionMode, "bypassPermissions")
	}
	if init.APIKeySource != "user" {
		t.Errorf("APIKeySource = %q, want %q", init.APIKeySource, "user")
	}
}

func TestReaderAssistantMessage(t *testing.T) {
	input := `{"type":"assistant","uuid":"msg-1","session_id":"sess-1","message":{"id":"msg_01","role":"assistant","content":[{"type":"text","text":"Hello"}]},"parent_tool_use_id":null}` + "\n"
	r := NewReader(strings.NewReader(input))

	msg, err := r.Next()
	if err != nil {
		t.Fatalf("Next() error: %v", err)
	}
	if msg == nil {
		t.Fatal("Next() returned nil message")
	}
	if msg.Type != "assistant" {
		t.Errorf("Type = %q, want %q", msg.Type, "assistant")
	}
	if msg.UUID != "msg-1" {
		t.Errorf("UUID = %q, want %q", msg.UUID, "msg-1")
	}

	var am AssistantMessage
	if err := ParseAs(msg, &am); err != nil {
		t.Fatalf("ParseAs(AssistantMessage) error: %v", err)
	}
	if am.Content == nil {
		t.Error("Content is nil, expected non-nil json.RawMessage")
	}
	if am.ParentToolUseID != nil {
		t.Errorf("ParentToolUseID = %v, want nil", am.ParentToolUseID)
	}
}

func TestReaderResultSuccess(t *testing.T) {
	input := `{"type":"result","subtype":"success","result":"Done","is_error":false,"duration_ms":5000,"num_turns":2,"total_cost_usd":0.03,"stop_reason":"end_turn","uuid":"r-1","session_id":"sess-1"}` + "\n"
	r := NewReader(strings.NewReader(input))

	msg, err := r.Next()
	if err != nil {
		t.Fatalf("Next() error: %v", err)
	}
	if msg == nil {
		t.Fatal("Next() returned nil message")
	}

	var rm ResultMessage
	if err := ParseAs(msg, &rm); err != nil {
		t.Fatalf("ParseAs(ResultMessage) error: %v", err)
	}
	if rm.Subtype != "success" {
		t.Errorf("Subtype = %q, want %q", rm.Subtype, "success")
	}
	if rm.Result != "Done" {
		t.Errorf("Result = %q, want %q", rm.Result, "Done")
	}
	if rm.IsError {
		t.Error("IsError = true, want false")
	}
	if rm.DurationMs != 5000 {
		t.Errorf("DurationMs = %d, want %d", rm.DurationMs, 5000)
	}
	if rm.NumTurns != 2 {
		t.Errorf("NumTurns = %d, want %d", rm.NumTurns, 2)
	}
	if rm.TotalCostUsd != 0.03 {
		t.Errorf("TotalCostUsd = %f, want %f", rm.TotalCostUsd, 0.03)
	}
	if rm.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want %q", rm.StopReason, "end_turn")
	}
}

func TestReaderResultError(t *testing.T) {
	input := `{"type":"result","subtype":"error_max_turns","is_error":true,"duration_ms":30000,"num_turns":10,"errors":["Maximum turns (10) reached"],"total_cost_usd":0.12,"uuid":"r-2","session_id":"sess-1"}` + "\n"
	r := NewReader(strings.NewReader(input))

	msg, err := r.Next()
	if err != nil {
		t.Fatalf("Next() error: %v", err)
	}
	if msg == nil {
		t.Fatal("Next() returned nil message")
	}
	if msg.Subtype != "error_max_turns" {
		t.Errorf("Subtype = %q, want %q", msg.Subtype, "error_max_turns")
	}

	var rm ResultMessage
	if err := ParseAs(msg, &rm); err != nil {
		t.Fatalf("ParseAs(ResultMessage) error: %v", err)
	}
	if !rm.IsError {
		t.Error("IsError = false, want true")
	}
	if rm.DurationMs != 30000 {
		t.Errorf("DurationMs = %d, want %d", rm.DurationMs, 30000)
	}
	if rm.NumTurns != 10 {
		t.Errorf("NumTurns = %d, want %d", rm.NumTurns, 10)
	}
	if len(rm.Errors) != 1 {
		t.Fatalf("len(Errors) = %d, want 1", len(rm.Errors))
	}
	if rm.Errors[0] != "Maximum turns (10) reached" {
		t.Errorf("Errors[0] = %q, want %q", rm.Errors[0], "Maximum turns (10) reached")
	}
	if rm.TotalCostUsd != 0.12 {
		t.Errorf("TotalCostUsd = %f, want %f", rm.TotalCostUsd, 0.12)
	}
	if rm.Subtype != "error_max_turns" {
		t.Errorf("Subtype = %q, want %q", rm.Subtype, "error_max_turns")
	}
}

func TestReaderControlRequest(t *testing.T) {
	input := `{"type":"control_request","request_id":"req-1","request":{"subtype":"can_use_tool","tool_name":"Bash"},"uuid":"cr-1","session_id":"sess-1"}` + "\n"
	r := NewReader(strings.NewReader(input))

	msg, err := r.Next()
	if err != nil {
		t.Fatalf("Next() error: %v", err)
	}
	if msg == nil {
		t.Fatal("Next() returned nil message")
	}
	if msg.Type != "control_request" {
		t.Errorf("Type = %q, want %q", msg.Type, "control_request")
	}

	var cr ControlRequest
	if err := ParseAs(msg, &cr); err != nil {
		t.Fatalf("ParseAs(ControlRequest) error: %v", err)
	}
	if cr.RequestID != "req-1" {
		t.Errorf("RequestID = %q, want %q", cr.RequestID, "req-1")
	}
	if cr.Request == nil {
		t.Fatal("Request is nil, expected non-nil json.RawMessage")
	}
	// Verify the request payload is valid JSON containing expected fields
	var reqPayload map[string]any
	if err := json.Unmarshal(cr.Request, &reqPayload); err != nil {
		t.Fatalf("Unmarshal Request payload: %v", err)
	}
	if reqPayload["subtype"] != "can_use_tool" {
		t.Errorf("Request.subtype = %v, want %q", reqPayload["subtype"], "can_use_tool")
	}
	if reqPayload["tool_name"] != "Bash" {
		t.Errorf("Request.tool_name = %v, want %q", reqPayload["tool_name"], "Bash")
	}
}

func TestReaderRateLimitEvent(t *testing.T) {
	input := `{"type":"rate_limit_event","rate_limit_info":{"status":"rate_limited","resetsAt":1700000000,"rateLimitType":"tokens"},"uuid":"rl-1","session_id":"sess-1"}` + "\n"
	r := NewReader(strings.NewReader(input))

	msg, err := r.Next()
	if err != nil {
		t.Fatalf("Next() error: %v", err)
	}
	if msg == nil {
		t.Fatal("Next() returned nil message")
	}
	if msg.Type != "rate_limit_event" {
		t.Errorf("Type = %q, want %q", msg.Type, "rate_limit_event")
	}

	var rle RateLimitEvent
	if err := ParseAs(msg, &rle); err != nil {
		t.Fatalf("ParseAs(RateLimitEvent) error: %v", err)
	}
	if rle.RateLimitInfo == nil {
		t.Fatal("RateLimitInfo is nil")
	}
	if rle.RateLimitInfo.Status != "rate_limited" {
		t.Errorf("Status = %q, want %q", rle.RateLimitInfo.Status, "rate_limited")
	}
	if rle.RateLimitInfo.ResetsAt != 1700000000 {
		t.Errorf("ResetsAt = %d, want %d", rle.RateLimitInfo.ResetsAt, 1700000000)
	}
	if rle.RateLimitInfo.RateLimitType != "tokens" {
		t.Errorf("RateLimitType = %q, want %q", rle.RateLimitInfo.RateLimitType, "tokens")
	}
}

func TestReaderSessionStateChanged(t *testing.T) {
	input := `{"type":"system","subtype":"session_state_changed","state":"idle","uuid":"sc-1","session_id":"sess-1"}` + "\n"
	r := NewReader(strings.NewReader(input))

	msg, err := r.Next()
	if err != nil {
		t.Fatalf("Next() error: %v", err)
	}
	if msg == nil {
		t.Fatal("Next() returned nil message")
	}
	if msg.Type != "system" {
		t.Errorf("Type = %q, want %q", msg.Type, "system")
	}
	if msg.Subtype != "session_state_changed" {
		t.Errorf("Subtype = %q, want %q", msg.Subtype, "session_state_changed")
	}

	var ssc SessionStateChanged
	if err := ParseAs(msg, &ssc); err != nil {
		t.Fatalf("ParseAs(SessionStateChanged) error: %v", err)
	}
	if ssc.State != "idle" {
		t.Errorf("State = %q, want %q", ssc.State, "idle")
	}
}

func TestReaderStreamEvent(t *testing.T) {
	input := `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}},"parent_tool_use_id":null,"uuid":"se-1","session_id":"sess-1"}` + "\n"
	r := NewReader(strings.NewReader(input))

	msg, err := r.Next()
	if err != nil {
		t.Fatalf("Next() error: %v", err)
	}
	if msg == nil {
		t.Fatal("Next() returned nil message")
	}
	if msg.Type != "stream_event" {
		t.Errorf("Type = %q, want %q", msg.Type, "stream_event")
	}

	var se StreamEvent
	if err := ParseAs(msg, &se); err != nil {
		t.Fatalf("ParseAs(StreamEvent) error: %v", err)
	}
	if se.Event == nil {
		t.Error("Event is nil, expected non-nil json.RawMessage")
	}
	// Verify event content is parseable
	var eventPayload map[string]any
	if err := json.Unmarshal(se.Event, &eventPayload); err != nil {
		t.Fatalf("Unmarshal Event: %v", err)
	}
	if eventPayload["type"] != "content_block_delta" {
		t.Errorf("Event.type = %v, want %q", eventPayload["type"], "content_block_delta")
	}
}

func TestReaderEmptyLines(t *testing.T) {
	input := "\n\n" + `{"type":"result","subtype":"success","uuid":"r-1","session_id":"sess-1","is_error":false,"duration_ms":100,"num_turns":1,"total_cost_usd":0.01}` + "\n\n"
	r := NewReader(strings.NewReader(input))

	msg, err := r.Next()
	if err != nil {
		t.Fatalf("Next() error: %v", err)
	}
	if msg == nil {
		t.Fatal("Next() returned nil message")
	}
	if msg.Type != "result" {
		t.Errorf("Type = %q, want %q", msg.Type, "result")
	}

	// Should get EOF after the single message
	msg2, err2 := r.Next()
	if !errors.Is(err2, io.EOF) {
		t.Errorf("second Next() error = %v, want io.EOF", err2)
	}
	if msg2 != nil {
		t.Errorf("second Next() msg = %v, want nil", msg2)
	}
}

func TestReaderMalformedJSON(t *testing.T) {
	input := "not json\n"
	r := NewReader(strings.NewReader(input))

	_, err := r.Next()
	if err == nil {
		t.Fatal("Next() expected error for malformed JSON, got nil")
	}
	if errors.Is(err, io.EOF) {
		t.Fatal("Next() returned io.EOF, expected JSON parse error")
	}
}

func TestReaderEOF(t *testing.T) {
	r := NewReader(strings.NewReader(""))

	msg, err := r.Next()
	if !errors.Is(err, io.EOF) {
		t.Errorf("Next() error = %v, want io.EOF", err)
	}
	if msg != nil {
		t.Errorf("Next() msg = %v, want nil", msg)
	}
}

func TestReaderMultipleMessages(t *testing.T) {
	lines := []string{
		`{"type":"system","subtype":"init","uuid":"u1","session_id":"s1","cwd":"/tmp","tools":[],"model":"m","permissionMode":"p","claude_code_version":"1","apiKeySource":"k"}`,
		`{"type":"assistant","uuid":"u2","session_id":"s1","message":{"role":"assistant","content":[]},"parent_tool_use_id":null}`,
		`{"type":"result","subtype":"success","uuid":"u3","session_id":"s1","is_error":false,"duration_ms":100,"num_turns":1,"total_cost_usd":0.01}`,
	}
	input := strings.Join(lines, "\n") + "\n"
	r := NewReader(strings.NewReader(input))

	expectedTypes := []string{"system", "assistant", "result"}
	expectedUUIDs := []string{"u1", "u2", "u3"}

	for i := range 3 {
		msg, err := r.Next()
		if err != nil {
			t.Fatalf("Next() #%d error: %v", i, err)
		}
		if msg == nil {
			t.Fatalf("Next() #%d returned nil message", i)
		}
		if msg.Type != expectedTypes[i] {
			t.Errorf("msg #%d Type = %q, want %q", i, msg.Type, expectedTypes[i])
		}
		if msg.UUID != expectedUUIDs[i] {
			t.Errorf("msg #%d UUID = %q, want %q", i, msg.UUID, expectedUUIDs[i])
		}
		if msg.Raw == nil {
			t.Errorf("msg #%d Raw is nil", i)
		}
	}

	// Fourth call should be EOF
	msg, err := r.Next()
	if !errors.Is(err, io.EOF) {
		t.Errorf("fourth Next() error = %v, want io.EOF", err)
	}
	if msg != nil {
		t.Errorf("fourth Next() msg = %v, want nil", msg)
	}
}

func TestReaderLargeMessage(t *testing.T) {
	// Build a ~500KB result string
	bigResult := strings.Repeat("x", 500*1024)
	line := `{"type":"result","subtype":"success","result":"` + bigResult + `","is_error":false,"duration_ms":1,"num_turns":1,"total_cost_usd":0.0,"uuid":"big-1","session_id":"s1"}` + "\n"
	r := NewReader(strings.NewReader(line))

	msg, err := r.Next()
	if err != nil {
		t.Fatalf("Next() error: %v", err)
	}
	if msg == nil {
		t.Fatal("Next() returned nil message")
	}

	var rm ResultMessage
	if err := ParseAs(msg, &rm); err != nil {
		t.Fatalf("ParseAs error: %v", err)
	}
	if len(rm.Result) != 500*1024 {
		t.Errorf("Result length = %d, want %d", len(rm.Result), 500*1024)
	}
}

func TestParseAsNilRaw(t *testing.T) {
	msg := &Message{
		Type: "system",
		Raw:  nil,
	}

	var init SystemInit
	err := ParseAs(msg, &init)
	if err == nil {
		t.Fatal("ParseAs with nil Raw expected error, got nil")
	}
}
