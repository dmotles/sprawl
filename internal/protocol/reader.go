package protocol

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// DefaultMaxLineSize is the default per-line ceiling. Tool results containing
// base64-encoded screenshots can exceed 1MB, so the default is intentionally
// generous while still providing a hard ceiling against a runaway producer.
const DefaultMaxLineSize = 100 * 1024 * 1024 // 100MB

// ErrLineTooLong is returned when a single line exceeds the configured ceiling.
var ErrLineTooLong = errors.New("protocol: line exceeds max size")

// Reader reads NDJSON messages from a stream.
type Reader struct {
	br      *bufio.Reader
	maxLine int
}

// NewReader creates a new Reader that reads from r using DefaultMaxLineSize.
func NewReader(r io.Reader) *Reader {
	return NewReaderWithMaxLine(r, DefaultMaxLineSize)
}

// NewReaderWithMaxLine creates a Reader with a custom per-line ceiling.
func NewReaderWithMaxLine(r io.Reader, maxLine int) *Reader {
	return &Reader{
		br:      bufio.NewReader(r),
		maxLine: maxLine,
	}
}

// readLine reads one line terminated by '\n' (or EOF). The returned slice does
// not include the delimiter. Returns io.EOF only when no bytes were read.
func (r *Reader) readLine() ([]byte, error) {
	var buf []byte
	for {
		chunk, err := r.br.ReadSlice('\n')
		if len(buf)+len(chunk) > r.maxLine {
			// Drain the rest of this line so subsequent Next() calls can
			// resume on the next line cleanly — but cap the drain so a
			// truly runaway stream can't exhaust memory. We just discard
			// until the next newline or EOF.
			if !errors.Is(err, bufio.ErrBufferFull) && !errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("%w: %d bytes", ErrLineTooLong, len(buf)+len(chunk))
			}
			for errors.Is(err, bufio.ErrBufferFull) {
				_, err = r.br.ReadSlice('\n')
			}
			return nil, fmt.Errorf("%w", ErrLineTooLong)
		}
		buf = append(buf, chunk...)
		if err == nil {
			// Full line read, delimiter included.
			return bytes.TrimRight(buf, "\n"), nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			// Line longer than bufio buffer; keep accumulating.
			continue
		}
		if errors.Is(err, io.EOF) {
			if len(buf) == 0 {
				return nil, io.EOF
			}
			// Final line lacking trailing newline.
			return bytes.TrimRight(buf, "\n"), nil
		}
		return nil, err
	}
}

// Next returns the next Message from the stream.
// Returns nil, io.EOF when the stream is exhausted.
func (r *Reader) Next() (*Message, error) {
	for {
		line, err := r.readLine()
		if err != nil {
			return nil, err
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}

		var msg Message
		if err := json.Unmarshal(line, &msg); err != nil {
			return nil, fmt.Errorf("malformed JSON: %w", err)
		}
		// Copy so the slice is not tied to bufio's internal buffer.
		raw := make([]byte, len(line))
		copy(raw, line)
		msg.Raw = json.RawMessage(raw)
		return &msg, nil
	}
}

// ParseAs deserializes msg.Raw into target using JSON.
func ParseAs[T any](msg *Message, target *T) error {
	if msg.Raw == nil {
		return fmt.Errorf("message Raw field is nil")
	}
	return json.Unmarshal(msg.Raw, target)
}
