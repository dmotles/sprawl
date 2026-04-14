package protocol

import (
	"encoding/json"
	"io"
	"sync"
)

// Writer writes NDJSON messages to a stream.
// Writer is safe for concurrent use.
type Writer struct {
	mu sync.Mutex
	w  io.Writer
}

// NewWriter creates a new Writer that writes to w.
func NewWriter(w io.Writer) *Writer {
	return &Writer{w: w}
}

// SendUserMessage sends a user message with the given prompt.
func (w *Writer) SendUserMessage(prompt string) error {
	msg := UserMessage{
		Type: "user",
		Message: MessageParam{
			Role:    "user",
			Content: prompt,
		},
		ParentToolUseID: nil,
	}
	return w.writeJSON(msg)
}

// SendControlResponse sends a control response for a given request.
func (w *Writer) SendControlResponse(requestID, subtype string, errMsg string) error {
	msg := ControlResponse{
		Type: "control_response",
		Response: ControlResponseInner{
			Subtype:   subtype,
			RequestID: requestID,
			Error:     errMsg,
		},
	}
	return w.writeJSON(msg)
}

// ApproveToolUse sends a success control response approving a tool use request.
// It includes the required behavior:"allow" payload that Claude Code expects.
func (w *Writer) ApproveToolUse(requestID string) error {
	msg := ControlResponse{
		Type: "control_response",
		Response: ControlResponseInner{
			Subtype:   "success",
			RequestID: requestID,
			Response: map[string]any{
				"behavior":  "allow",
				"toolUseID": "",
				"message":   "Allowed by host",
			},
		},
	}
	return w.writeJSON(msg)
}

// SendInterrupt sends an interrupt control_request to cancel the current turn.
func (w *Writer) SendInterrupt(requestID string) error {
	msg := InterruptRequest{
		Type:      "control_request",
		RequestID: requestID,
		Request:   InterruptRequestInner{Subtype: "interrupt"},
	}
	return w.writeJSON(msg)
}

// Close closes the underlying writer if it implements io.Closer.
func (w *Writer) Close() error {
	if c, ok := w.w.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// WriteJSON marshals v as JSON and writes it as an NDJSON line.
// This is the public equivalent of the private writeJSON method.
func (w *Writer) WriteJSON(v any) error {
	return w.writeJSON(v)
}

func (w *Writer) writeJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	w.mu.Lock()
	_, err = w.w.Write(data)
	w.mu.Unlock()
	return err
}
