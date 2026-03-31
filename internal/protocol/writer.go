package protocol

import (
	"encoding/json"
	"io"
)

// Writer writes NDJSON messages to a stream.
// Writer is not safe for concurrent use; callers must synchronize access.
type Writer struct {
	w io.Writer
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
func (w *Writer) ApproveToolUse(requestID string) error {
	return w.SendControlResponse(requestID, "success", "")
}

// Close closes the underlying writer if it implements io.Closer.
func (w *Writer) Close() error {
	if c, ok := w.w.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

func (w *Writer) writeJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = w.w.Write(data)
	return err
}
