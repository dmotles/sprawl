package agentloop

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/dmotles/sprawl/internal/protocol"
)

// DefaultActivityCapacity is the default in-memory ring buffer size.
const DefaultActivityCapacity = 200

// maxSummaryLen caps the Summary field length (bytes).
const maxSummaryLen = 200

// ActivityEntry is a single recorded protocol-level event.
//
// See docs/designs/messaging-overhaul.md §4.4.
type ActivityEntry struct {
	TS      time.Time `json:"ts"`
	Kind    string    `json:"kind"`           // "assistant_text" | "tool_use" | "result" | "system" | "rate_limit"
	Summary string    `json:"summary"`        // ≤ maxSummaryLen, redacted
	Tool    string    `json:"tool,omitempty"` // populated for tool_use
}

// ActivityRing is a bounded, thread-safe ring buffer of ActivityEntry values.
//
// If a writer is provided, each Append additionally writes a single NDJSON
// line (entry as compact JSON + "\n") for cross-process consumers tailing
// the file.
type ActivityRing struct {
	mu       sync.Mutex
	entries  []ActivityEntry
	capacity int
	writer   io.Writer
}

// NewActivityRing returns a ring with the given capacity. If capacity ≤ 0,
// DefaultActivityCapacity is used. writer may be nil.
func NewActivityRing(capacity int, writer io.Writer) *ActivityRing {
	if capacity <= 0 {
		capacity = DefaultActivityCapacity
	}
	return &ActivityRing{
		entries:  make([]ActivityEntry, 0, capacity),
		capacity: capacity,
		writer:   writer,
	}
}

// Append records an entry. Oldest entry is evicted when capacity is reached.
func (r *ActivityRing) Append(e ActivityEntry) {
	r.mu.Lock()
	if len(r.entries) >= r.capacity {
		// Drop the oldest entry (index 0). For capacity=200 this copy is cheap.
		copy(r.entries, r.entries[1:])
		r.entries = r.entries[:len(r.entries)-1]
	}
	r.entries = append(r.entries, e)
	w := r.writer
	r.mu.Unlock()

	if w != nil {
		b, err := json.Marshal(e)
		if err == nil {
			b = append(b, '\n')
			_, _ = w.Write(b)
		}
	}
}

// Tail returns up to n most-recent entries (oldest-first).
func (r *ActivityRing) Tail(n int) []ActivityEntry {
	if n <= 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if n >= len(r.entries) {
		out := make([]ActivityEntry, len(r.entries))
		copy(out, r.entries)
		return out
	}
	out := make([]ActivityEntry, n)
	copy(out, r.entries[len(r.entries)-n:])
	return out
}

// RecordMessage derives ActivityEntry values from a protocol.Message and
// appends them. Unknown/noisy types (user echo, stream_event, control_request)
// are ignored.
func (r *ActivityRing) RecordMessage(msg *protocol.Message, now func() time.Time) {
	if msg == nil {
		return
	}
	if now == nil {
		now = time.Now
	}
	ts := now()

	switch msg.Type {
	case "assistant":
		var outer struct {
			Message struct {
				Content []struct {
					Type  string          `json:"type"`
					Text  string          `json:"text"`
					Name  string          `json:"name,omitempty"`
					Input json.RawMessage `json:"input,omitempty"`
				} `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal(msg.Raw, &outer); err != nil {
			return
		}
		for _, block := range outer.Message.Content {
			switch block.Type {
			case "text":
				if block.Text == "" {
					continue
				}
				r.Append(ActivityEntry{
					TS:      ts,
					Kind:    "assistant_text",
					Summary: truncate(RedactSummary(block.Text)),
				})
			case "tool_use":
				if block.Name == "" {
					continue
				}
				inputStr := ""
				if len(block.Input) > 0 {
					inputStr = string(block.Input)
				}
				summary := fmt.Sprintf("%s %s", block.Name, inputStr)
				r.Append(ActivityEntry{
					TS:      ts,
					Kind:    "tool_use",
					Tool:    block.Name,
					Summary: truncate(RedactSummary(summary)),
				})
			}
		}

	case "result":
		var res protocol.ResultMessage
		if err := json.Unmarshal(msg.Raw, &res); err != nil {
			return
		}
		status := "success"
		if res.IsError {
			status = "error"
		}
		summary := fmt.Sprintf("%s stop=%s turns=%d", status, res.StopReason, res.NumTurns)
		r.Append(ActivityEntry{
			TS:      ts,
			Kind:    "result",
			Summary: truncate(summary),
		})

	case "system":
		if msg.Subtype == "" {
			return
		}
		summary := msg.Subtype
		if msg.Subtype == "session_state_changed" {
			var ssc protocol.SessionStateChanged
			if err := json.Unmarshal(msg.Raw, &ssc); err == nil && ssc.State != "" {
				summary = fmt.Sprintf("%s: %s", msg.Subtype, ssc.State)
			}
		}
		r.Append(ActivityEntry{
			TS:      ts,
			Kind:    "system",
			Summary: truncate(summary),
		})

	case "rate_limit_event":
		var evt protocol.RateLimitEvent
		if err := json.Unmarshal(msg.Raw, &evt); err != nil {
			return
		}
		if evt.RateLimitInfo == nil {
			return
		}
		summary := fmt.Sprintf("status=%s type=%s", evt.RateLimitInfo.Status, evt.RateLimitInfo.RateLimitType)
		r.Append(ActivityEntry{
			TS:      ts,
			Kind:    "rate_limit",
			Summary: truncate(summary),
		})

	default:
		// "user" echo, "stream_event", "control_request", etc. are intentionally
		// ignored — they have no standalone observability value for this surface.
	}
}

// ActivityPath returns the canonical append-only activity file for an agent.
func ActivityPath(sprawlRoot, agentName string) string {
	return filepath.Join(sprawlRoot, ".sprawl", "agents", agentName, "activity.ndjson")
}

// ReadActivityFile returns the last `tail` entries from an NDJSON activity
// file. A missing file yields (nil, nil); malformed lines are skipped.
func ReadActivityFile(path string, tail int) ([]ActivityEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("open activity file: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// Activity entries can be up to a few KB if the ring records larger tool
	// args in the future — give the scanner generous headroom.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var all []ActivityEntry
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e ActivityEntry
		if err := json.Unmarshal(line, &e); err != nil {
			continue // skip malformed
		}
		all = append(all, e)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading activity file: %w", err)
	}
	if tail <= 0 || tail >= len(all) {
		return all, nil
	}
	return all[len(all)-tail:], nil
}

// --- Redaction ---

// secretKeyPattern matches JSON object keys whose names suggest secrets.
// Matches the full key="value" pair (single or double-quoted is NDJSON-unusual
// but handled for defensive completeness) so we can replace the value wholesale.
//
// The key set is a denylist (authorization, api_key, anthropic_api_key, bearer,
// secret, token, password), case-insensitive, with optional dashes/underscores
// and common prefixes (x-, http_).
var secretKeyPattern = regexp.MustCompile(
	`(?i)"((?:x[-_])?(?:http[-_])?(?:(?:anthropic[-_])?api[-_]?key|authorization|bearer|secret|token|password))"\s*:\s*"[^"]*"`,
)

// RedactSummary replaces values of known-sensitive JSON keys with "[REDACTED]".
// Non-JSON input is returned unchanged. The denylist is intentionally narrow:
// this is defense-in-depth, not a complete DLP solution.
func RedactSummary(s string) string {
	return secretKeyPattern.ReplaceAllStringFunc(s, func(match string) string {
		// Preserve the key, replace the value.
		colonIdx := strings.Index(match, ":")
		if colonIdx < 0 {
			return match
		}
		return match[:colonIdx+1] + `"[REDACTED]"`
	})
}

// truncate returns s truncated to maxSummaryLen bytes, suffixing "..." when cut.
func truncate(s string) string {
	if len(s) <= maxSummaryLen {
		return s
	}
	return s[:maxSummaryLen-3] + "..."
}
