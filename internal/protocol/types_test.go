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

// --- QUM-860: MessageParam string-or-content-blocks union ---

// TestMessageParam_Marshal_TextOnly_BareString guards the byte-identical text
// fast-path: a MessageParam with only Content set must serialize to a bare
// string content field, exactly as before the Blocks union was added.
func TestMessageParam_Marshal_TextOnly_BareString(t *testing.T) {
	m := MessageParam{Role: "user", Content: "hello"}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	if string(data) != `{"role":"user","content":"hello"}` {
		t.Errorf("marshaled = %s, want {\"role\":\"user\",\"content\":\"hello\"}", data)
	}
	// content must be a JSON string, not an array.
	var probe struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if len(probe.Content) == 0 || probe.Content[0] != '"' {
		t.Errorf("content should be a JSON string, got %s", probe.Content)
	}
}

// TestMessageParam_Marshal_EmptyContent_EmitsEmptyString guards against an
// omitempty regression: an empty text turn must still emit "content":"".
func TestMessageParam_Marshal_EmptyContent_EmitsEmptyString(t *testing.T) {
	m := MessageParam{Role: "user", Content: ""}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	if !strings.Contains(string(data), `"content":""`) {
		t.Errorf("marshaled = %s, want it to contain \"content\":\"\"", data)
	}
}

// TestMessageParam_Marshal_Blocks_ImageThenText proves the array form and that
// image-then-text ordering is preserved on the wire.
func TestMessageParam_Marshal_Blocks_ImageThenText(t *testing.T) {
	m := MessageParam{
		Role: "user",
		Blocks: []ContentBlock{
			{Type: "image", Source: &ImageSource{Type: "base64", MediaType: "image/png", Data: "AAAA"}},
			{Type: "text", Text: "what is this"},
		},
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	var parsed struct {
		Role    string            `json:"role"`
		Content []json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Unmarshal failed (content should be an array): %v; data=%s", err, data)
	}
	if parsed.Role != "user" {
		t.Errorf("role = %q, want user", parsed.Role)
	}
	if len(parsed.Content) != 2 {
		t.Fatalf("content len = %d, want 2; data=%s", len(parsed.Content), data)
	}
	var b0 struct {
		Type   string `json:"type"`
		Source struct {
			Type      string `json:"type"`
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
		} `json:"source"`
	}
	if err := json.Unmarshal(parsed.Content[0], &b0); err != nil {
		t.Fatalf("Unmarshal block 0: %v", err)
	}
	if b0.Type != "image" {
		t.Errorf("block[0].type = %q, want image (image-then-text ordering)", b0.Type)
	}
	if b0.Source.Type != "base64" || b0.Source.MediaType != "image/png" || b0.Source.Data != "AAAA" {
		t.Errorf("block[0].source = %+v, want base64/image/png/AAAA", b0.Source)
	}
	var b1 struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(parsed.Content[1], &b1); err != nil {
		t.Fatalf("Unmarshal block 1: %v", err)
	}
	if b1.Type != "text" || b1.Text != "what is this" {
		t.Errorf("block[1] = %+v, want text/what is this", b1)
	}
}

// TestMessageParam_Marshal_Blocks_OmitsEmptyFields guards the omitempty tags:
// a base64 image block must not emit url/file_id, and a text block must not
// emit a source.
func TestMessageParam_Marshal_Blocks_OmitsEmptyFields(t *testing.T) {
	m := MessageParam{
		Role: "user",
		Blocks: []ContentBlock{
			{Type: "image", Source: &ImageSource{Type: "base64", MediaType: "image/jpeg", Data: "ZZZZ"}},
			{Type: "text", Text: "label"},
		},
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	s := string(data)
	for _, key := range []string{`"url"`, `"file_id"`} {
		if strings.Contains(s, key) {
			t.Errorf("marshaled should not contain %s for a base64 image: %s", key, s)
		}
	}
	// The text block must not carry a source key.
	if strings.Count(s, `"source"`) != 1 {
		t.Errorf("expected exactly one source (on the image block), got: %s", s)
	}
	// The image block must not emit a spurious empty text field (forces
	// omitempty on ContentBlock.Text — an image block with "text":"" is a
	// malformed block the API can reject).
	var wrap struct {
		Content []json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(data, &wrap); err != nil {
		t.Fatalf("Unmarshal content array: %v", err)
	}
	blocks := wrap.Content
	if len(blocks) != 2 {
		t.Fatalf("content len = %d, want 2", len(blocks))
	}
	if strings.Contains(string(blocks[0]), `"text"`) {
		t.Errorf("image block must not emit a text field, got: %s", blocks[0])
	}
}

// TestMessageParam_Unmarshal_BareString_PopulatesContent is the invariant that
// keeps writer_test green: default unmarshal of the bare-string wire form must
// populate Content (no custom UnmarshalJSON is provided).
func TestMessageParam_Unmarshal_BareString_PopulatesContent(t *testing.T) {
	var m MessageParam
	if err := json.Unmarshal([]byte(`{"role":"user","content":"hi"}`), &m); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if m.Content != "hi" {
		t.Errorf("Content = %q, want hi", m.Content)
	}
	if m.Blocks != nil {
		t.Errorf("Blocks = %v, want nil", m.Blocks)
	}
}

// TestUserMessage_RoundTrip_TextContent mirrors writer_test at the MessageParam
// layer: a full UserMessage with a text turn round-trips through JSON with
// Content intact.
func TestUserMessage_RoundTrip_TextContent(t *testing.T) {
	msg := UserMessage{
		Type:            "user",
		Message:         MessageParam{Role: "user", Content: "hello"},
		ParentToolUseID: nil,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	var back UserMessage
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if back.Message.Content != "hello" {
		t.Errorf("round-tripped Content = %q, want hello", back.Message.Content)
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
