package protocol

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

func TestWriterSendUserMessage(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	if err := w.SendUserMessage("hello"); err != nil {
		t.Fatalf("SendUserMessage error: %v", err)
	}

	output := buf.String()
	// Must end with newline (NDJSON)
	if !strings.HasSuffix(output, "\n") {
		t.Error("output does not end with newline")
	}

	var msg UserMessage
	if err := json.Unmarshal(buf.Bytes(), &msg); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}
	if msg.Type != "user" {
		t.Errorf("Type = %q, want %q", msg.Type, "user")
	}
	if msg.Message.Role != "user" {
		t.Errorf("Message.Role = %q, want %q", msg.Message.Role, "user")
	}
	if msg.Message.Content != "hello" {
		t.Errorf("Message.Content = %q, want %q", msg.Message.Content, "hello")
	}
	if msg.ParentToolUseID != nil {
		t.Errorf("ParentToolUseID = %v, want nil", msg.ParentToolUseID)
	}
}

func TestWriterSendControlResponseSuccess(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	if err := w.SendControlResponse("req-1", "success", ""); err != nil {
		t.Fatalf("SendControlResponse error: %v", err)
	}

	output := buf.String()
	if !strings.HasSuffix(output, "\n") {
		t.Error("output does not end with newline")
	}

	var cr ControlResponse
	if err := json.Unmarshal(buf.Bytes(), &cr); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}
	if cr.Type != "control_response" {
		t.Errorf("Type = %q, want %q", cr.Type, "control_response")
	}
	if cr.Response.RequestID != "req-1" {
		t.Errorf("RequestID = %q, want %q", cr.Response.RequestID, "req-1")
	}
	if cr.Response.Subtype != "success" {
		t.Errorf("Subtype = %q, want %q", cr.Response.Subtype, "success")
	}
	if cr.Response.Error != "" {
		t.Errorf("Error = %q, want empty", cr.Response.Error)
	}

	// The "error" key should be omitted entirely (omitempty)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(buf.Bytes(), &raw); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}
	var resp map[string]json.RawMessage
	if err := json.Unmarshal(raw["response"], &resp); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	if _, ok := resp["error"]; ok {
		t.Error("error field should be omitted when empty")
	}
}

func TestWriterSendControlResponseError(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	if err := w.SendControlResponse("req-2", "error", "denied"); err != nil {
		t.Fatalf("SendControlResponse error: %v", err)
	}

	var cr ControlResponse
	if err := json.Unmarshal(buf.Bytes(), &cr); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}
	if cr.Response.RequestID != "req-2" {
		t.Errorf("RequestID = %q, want %q", cr.Response.RequestID, "req-2")
	}
	if cr.Response.Subtype != "error" {
		t.Errorf("Subtype = %q, want %q", cr.Response.Subtype, "error")
	}
	if cr.Response.Error != "denied" {
		t.Errorf("Error = %q, want %q", cr.Response.Error, "denied")
	}
}

func TestWriterApproveToolUse(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	if err := w.ApproveToolUse("req-3"); err != nil {
		t.Fatalf("ApproveToolUse error: %v", err)
	}

	output := buf.String()
	if !strings.HasSuffix(output, "\n") {
		t.Error("output does not end with newline")
	}

	var cr ControlResponse
	if err := json.Unmarshal(buf.Bytes(), &cr); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}
	if cr.Type != "control_response" {
		t.Errorf("Type = %q, want %q", cr.Type, "control_response")
	}
	if cr.Response.RequestID != "req-3" {
		t.Errorf("RequestID = %q, want %q", cr.Response.RequestID, "req-3")
	}
	if cr.Response.Subtype != "success" {
		t.Errorf("Subtype = %q, want %q", cr.Response.Subtype, "success")
	}
	if cr.Response.Error != "" {
		t.Errorf("Error = %q, want empty", cr.Response.Error)
	}
}

type mockCloser struct {
	bytes.Buffer
	closed bool
}

func (m *mockCloser) Close() error {
	m.closed = true
	return nil
}

func TestWriterClose(t *testing.T) {
	t.Run("non-closer", func(t *testing.T) {
		var buf bytes.Buffer
		w := NewWriter(&buf)
		if err := w.Close(); err != nil {
			t.Errorf("Close() on non-Closer returned error: %v", err)
		}
	})

	t.Run("closer", func(t *testing.T) {
		mc := &mockCloser{}
		w := NewWriter(mc)
		if err := w.Close(); err != nil {
			t.Errorf("Close() returned error: %v", err)
		}
		if !mc.closed {
			t.Error("Close() did not call underlying Close()")
		}
	})
}

func TestWriterSendInterrupt(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	if err := w.SendInterrupt("req-int-1"); err != nil {
		t.Fatalf("SendInterrupt error: %v", err)
	}

	output := buf.String()
	// Must end with newline (NDJSON)
	if !strings.HasSuffix(output, "\n") {
		t.Error("output does not end with newline")
	}

	var msg InterruptRequest
	if err := json.Unmarshal(buf.Bytes(), &msg); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}
	if msg.Type != "control_request" {
		t.Errorf("Type = %q, want %q", msg.Type, "control_request")
	}
	if msg.RequestID != "req-int-1" {
		t.Errorf("RequestID = %q, want %q", msg.RequestID, "req-int-1")
	}
	if msg.Request.Subtype != "interrupt" {
		t.Errorf("Request.Subtype = %q, want %q", msg.Request.Subtype, "interrupt")
	}
}

func TestWriterConcurrentWrites(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	const numGoroutines = 10
	var wg sync.WaitGroup
	wg.Add(numGoroutines * 2)

	for i := range numGoroutines {
		go func(n int) {
			defer wg.Done()
			_ = w.SendUserMessage("msg")
		}(i)
		go func(n int) {
			defer wg.Done()
			_ = w.SendInterrupt("req")
		}(i)
	}

	wg.Wait()

	// Verify we got the expected number of NDJSON lines
	output := strings.TrimRight(buf.String(), "\n")
	lines := strings.Split(output, "\n")
	if len(lines) != numGoroutines*2 {
		t.Errorf("got %d lines, want %d", len(lines), numGoroutines*2)
	}

	// Each line should be valid JSON
	for i, line := range lines {
		var raw json.RawMessage
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			t.Errorf("line %d is not valid JSON: %v", i, err)
		}
	}
}

func TestWriterWriteJSON(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	msg := map[string]any{
		"type":    "custom",
		"payload": "test-data",
	}
	if err := w.WriteJSON(msg); err != nil {
		t.Fatalf("WriteJSON() error: %v", err)
	}

	output := buf.String()
	if !strings.HasSuffix(output, "\n") {
		t.Error("WriteJSON() output does not end with newline")
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if got["type"] != "custom" {
		t.Errorf("type = %v, want %q", got["type"], "custom")
	}
	if got["payload"] != "test-data" {
		t.Errorf("payload = %v, want %q", got["payload"], "test-data")
	}
}

func TestWriterWriteJSONStruct(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	type testMsg struct {
		Type string `json:"type"`
		Val  int    `json:"val"`
	}
	msg := testMsg{Type: "test", Val: 42}
	if err := w.WriteJSON(msg); err != nil {
		t.Fatalf("WriteJSON() error: %v", err)
	}

	var got testMsg
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Type != "test" {
		t.Errorf("Type = %q, want %q", got.Type, "test")
	}
	if got.Val != 42 {
		t.Errorf("Val = %d, want 42", got.Val)
	}
}

func TestWriterMultipleMessages(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	if err := w.SendUserMessage("first"); err != nil {
		t.Fatalf("SendUserMessage(first) error: %v", err)
	}
	if err := w.SendUserMessage("second"); err != nil {
		t.Fatalf("SendUserMessage(second) error: %v", err)
	}

	output := buf.String()
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}

	for i, line := range lines {
		var msg UserMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Fatalf("line %d unmarshal error: %v", i, err)
		}
		if msg.Type != "user" {
			t.Errorf("line %d Type = %q, want %q", i, msg.Type, "user")
		}
	}

	// Verify content of each message
	var msg1, msg2 UserMessage
	json.Unmarshal([]byte(lines[0]), &msg1)
	json.Unmarshal([]byte(lines[1]), &msg2)
	if msg1.Message.Content != "first" {
		t.Errorf("first message Content = %q, want %q", msg1.Message.Content, "first")
	}
	if msg2.Message.Content != "second" {
		t.Errorf("second message Content = %q, want %q", msg2.Message.Content, "second")
	}
}
