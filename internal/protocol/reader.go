package protocol

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Reader reads NDJSON messages from a stream.
type Reader struct {
	scanner *bufio.Scanner
}

// NewReader creates a new Reader that reads from r.
func NewReader(r io.Reader) *Reader {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 64*1024), 1024*1024) // 1MB max buffer
	return &Reader{scanner: s}
}

// Next returns the next Message from the stream.
// Returns nil, io.EOF when the stream is exhausted.
func (r *Reader) Next() (*Message, error) {
	for r.scanner.Scan() {
		line := strings.TrimSpace(r.scanner.Text())
		if line == "" {
			continue
		}

		var msg Message
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			return nil, fmt.Errorf("malformed JSON: %w", err)
		}
		msg.Raw = json.RawMessage(line)
		return &msg, nil
	}
	if err := r.scanner.Err(); err != nil {
		return nil, err
	}
	return nil, io.EOF
}

// ParseAs deserializes msg.Raw into target using JSON.
func ParseAs[T any](msg *Message, target *T) error {
	if msg.Raw == nil {
		return fmt.Errorf("message Raw field is nil")
	}
	return json.Unmarshal(msg.Raw, target)
}
