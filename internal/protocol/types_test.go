package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestUsage_UnmarshalJSON(t *testing.T) {
	raw := `{"input_tokens":1500,"output_tokens":300,"cache_creation_input_tokens":100,"cache_read_input_tokens":50}`
	var u Usage
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if u.InputTokens != 1500 {
		t.Errorf("InputTokens = %d, want 1500", u.InputTokens)
	}
	if u.OutputTokens != 300 {
		t.Errorf("OutputTokens = %d, want 300", u.OutputTokens)
	}
	if u.CacheCreationInputTokens != 100 {
		t.Errorf("CacheCreationInputTokens = %d, want 100", u.CacheCreationInputTokens)
	}
	if u.CacheReadInputTokens != 50 {
		t.Errorf("CacheReadInputTokens = %d, want 50", u.CacheReadInputTokens)
	}
}

func TestUsage_UnmarshalJSON_MissingFields(t *testing.T) {
	raw := `{"input_tokens":2000,"output_tokens":500}`
	var u Usage
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if u.InputTokens != 2000 {
		t.Errorf("InputTokens = %d, want 2000", u.InputTokens)
	}
	if u.CacheCreationInputTokens != 0 {
		t.Errorf("CacheCreationInputTokens = %d, want 0", u.CacheCreationInputTokens)
	}
	if u.CacheReadInputTokens != 0 {
		t.Errorf("CacheReadInputTokens = %d, want 0", u.CacheReadInputTokens)
	}
}

// TestAssistantMessage_ParseUsage covers QUM-368: the inline `usage` + `model`
// blob inside AssistantMessage.Content must be extractable via ParseUsage.
func TestAssistantMessage_ParseUsage(t *testing.T) {
	// Verbatim from QUM-368 issue body (the example stream-json line).
	content := `{"model":"claude-opus-4-7","usage":{"input_tokens":6,"cache_creation_input_tokens":14083,"cache_read_input_tokens":12066,"output_tokens":8,"service_tier":"standard"}}`
	m := &AssistantMessage{Content: []byte(content)}
	u, model, err := m.ParseUsage()
	if err != nil {
		t.Fatalf("ParseUsage: %v", err)
	}
	if u == nil {
		t.Fatal("ParseUsage returned nil Usage")
	}
	if model != "claude-opus-4-7" {
		t.Errorf("model = %q, want claude-opus-4-7", model)
	}
	if u.InputTokens != 6 || u.OutputTokens != 8 ||
		u.CacheReadInputTokens != 12066 || u.CacheCreationInputTokens != 14083 {
		t.Errorf("usage = %+v, want input=6 output=8 read=12066 creation=14083", u)
	}
}

func TestAssistantMessage_ParseUsage_EmptyContent(t *testing.T) {
	m := &AssistantMessage{}
	u, model, err := m.ParseUsage()
	if err != nil {
		t.Errorf("expected nil error for empty content, got %v", err)
	}
	if u != nil {
		t.Errorf("expected nil Usage for empty content, got %+v", u)
	}
	if model != "" {
		t.Errorf("expected empty model for empty content, got %q", model)
	}
}

func TestAssistantMessage_ParseUsage_MalformedContent(t *testing.T) {
	m := &AssistantMessage{Content: []byte("not valid json {{{")}
	_, _, err := m.ParseUsage()
	if err == nil {
		t.Fatal("expected non-nil error for malformed Content")
	}
}

func TestUsage_UnmarshalJSON_Empty(t *testing.T) {
	raw := `{}`
	var u Usage
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if u.InputTokens != 0 {
		t.Errorf("InputTokens = %d, want 0", u.InputTokens)
	}
	if u.OutputTokens != 0 {
		t.Errorf("OutputTokens = %d, want 0", u.OutputTokens)
	}
}

// TestUserMessage_MarshalOmitsEmptyExtras guards zero-behavior-change: the
// existing SendUserMessage path must serialize byte-identically, so the new
// Priority/UUID/SessionID fields must be omitted when unset.
func TestUserMessage_MarshalOmitsEmptyExtras(t *testing.T) {
	msg := UserMessage{
		Type:            "user",
		Message:         MessageParam{Role: "user", Content: "hello"},
		ParentToolUseID: nil,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	s := string(data)
	for _, key := range []string{`"priority"`, `"uuid"`, `"session_id"`} {
		if strings.Contains(s, key) {
			t.Errorf("marshaled UserMessage should not contain %s when unset: %s", key, s)
		}
	}
	// parent_tool_use_id must still be present (non-omitempty) so the wire
	// shape of existing stdin writes is unchanged.
	if !strings.Contains(s, `"parent_tool_use_id":null`) {
		t.Errorf("marshaled UserMessage should retain parent_tool_use_id:null: %s", s)
	}
	// The core type/message keys must remain.
	for _, key := range []string{`"type":"user"`, `"message"`} {
		if !strings.Contains(s, key) {
			t.Errorf("marshaled UserMessage missing core key %s: %s", key, s)
		}
	}
}

func TestUserMessage_MarshalWithPriorityUUIDSession(t *testing.T) {
	msg := UserMessage{
		Type:      "user",
		Message:   MessageParam{Role: "user", Content: "hi"},
		Priority:  "next",
		UUID:      "u-1",
		SessionID: "s-1",
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if parsed["priority"] != "next" {
		t.Errorf("priority = %v, want next", parsed["priority"])
	}
	if parsed["uuid"] != "u-1" {
		t.Errorf("uuid = %v, want u-1", parsed["uuid"])
	}
	if parsed["session_id"] != "s-1" {
		t.Errorf("session_id = %v, want s-1", parsed["session_id"])
	}
}

// TestCancelAsyncMessageRequest_Marshal verifies the wire shape, in
// particular that the inner key is message_uuid (not uuid) per CLI 2.1.173.
func TestCancelAsyncMessageRequest_Marshal(t *testing.T) {
	req := CancelAsyncMessageRequest{
		Type:      "control_request",
		RequestID: "r-1",
		Request: CancelAsyncMessageRequestInner{
			Subtype:     "cancel_async_message",
			MessageUUID: "u-1",
		},
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	var parsed struct {
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
		Request   struct {
			Subtype     string `json:"subtype"`
			MessageUUID string `json:"message_uuid"`
			UUID        string `json:"uuid"`
		} `json:"request"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if parsed.Type != "control_request" {
		t.Errorf("type = %q, want control_request", parsed.Type)
	}
	if parsed.RequestID != "r-1" {
		t.Errorf("request_id = %q, want r-1", parsed.RequestID)
	}
	if parsed.Request.Subtype != "cancel_async_message" {
		t.Errorf("subtype = %q, want cancel_async_message", parsed.Request.Subtype)
	}
	if parsed.Request.MessageUUID != "u-1" {
		t.Errorf("message_uuid = %q, want u-1", parsed.Request.MessageUUID)
	}
	if parsed.Request.UUID != "" {
		t.Errorf("cancel request must not carry a uuid key, got %q", parsed.Request.UUID)
	}
}

func TestCancelAsyncMessageAck_Unmarshal(t *testing.T) {
	var ack CancelAsyncMessageAck
	if err := json.Unmarshal([]byte(`{"cancelled":true}`), &ack); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if !ack.Cancelled {
		t.Error("Cancelled = false, want true")
	}

	var empty CancelAsyncMessageAck
	if err := json.Unmarshal([]byte(`{}`), &empty); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if empty.Cancelled {
		t.Error("Cancelled = true for empty object, want false")
	}
}

func TestUserFrame_Unmarshal_IsReplay(t *testing.T) {
	var frame UserFrame
	raw := `{"type":"user","uuid":"u-1","session_id":"s-1","isReplay":true}`
	if err := json.Unmarshal([]byte(raw), &frame); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if frame.Type != "user" {
		t.Errorf("Type = %q, want user", frame.Type)
	}
	if frame.UUID != "u-1" {
		t.Errorf("UUID = %q, want u-1", frame.UUID)
	}
	if frame.SessionID != "s-1" {
		t.Errorf("SessionID = %q, want s-1", frame.SessionID)
	}
	if !frame.IsReplay {
		t.Error("IsReplay = false, want true")
	}

	var plain UserFrame
	if err := json.Unmarshal([]byte(`{"type":"user","uuid":"u-2"}`), &plain); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if plain.IsReplay {
		t.Error("IsReplay = true for frame without isReplay, want false")
	}
}

func TestSystemNotification_Unmarshal(t *testing.T) {
	raw := `{"type":"system","subtype":"notification","key":"k","text":"t","priority":"high","color":"red","timeout_ms":5000}`
	var n SystemNotification
	if err := json.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if n.Type != "system" || n.Subtype != "notification" {
		t.Errorf("Type/Subtype = %q/%q, want system/notification", n.Type, n.Subtype)
	}
	if n.Key != "k" || n.Text != "t" {
		t.Errorf("Key/Text = %q/%q, want k/t", n.Key, n.Text)
	}
	if n.Priority != "high" {
		t.Errorf("Priority = %q, want high", n.Priority)
	}
	if n.Color != "red" {
		t.Errorf("Color = %q, want red", n.Color)
	}
	if n.TimeoutMs != 5000 {
		t.Errorf("TimeoutMs = %d, want 5000", n.TimeoutMs)
	}

	var minimal SystemNotification
	if err := json.Unmarshal([]byte(`{"type":"system","subtype":"notification","key":"k","text":"t","priority":"low"}`), &minimal); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if minimal.Color != "" || minimal.TimeoutMs != 0 {
		t.Errorf("optional fields should be zero: color=%q timeout_ms=%d", minimal.Color, minimal.TimeoutMs)
	}
}
