package tui

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// ReplayMaxMessages is the default cap on messages loaded from a prior
// session's transcript during resume replay.
const ReplayMaxMessages = 500

// LoadTranscript reads a Claude session JSONL log and converts it into a flat
// slice of MessageEntry values suitable for pre-populating the viewport.
//
// If the file does not exist, (nil, nil) is returned. If the file contains no
// replayable records, (nil, nil) is returned (no status markers are emitted).
// Otherwise, a trailing "Resumed from prior session" status marker is
// appended. When maxMessages > 0 and the entry count exceeds it, the oldest
// entries are dropped and a leading "earlier messages truncated" marker is
// prepended.
func LoadTranscript(path string, maxMessages int) ([]MessageEntry, error) {
	entries, err := scanTranscript(path, time.Time{})
	if err != nil || len(entries) == 0 {
		return nil, err
	}

	if maxMessages > 0 && len(entries) > maxMessages {
		entries = entries[len(entries)-maxMessages:]
		entries = append([]MessageEntry{{
			Type:     MessageStatus,
			Content:  "earlier messages truncated",
			Complete: true,
		}}, entries...)
	}

	entries = append(entries, MessageEntry{
		Type:     MessageStatus,
		Content:  "Resumed from prior session",
		Complete: true,
	})
	return entries, nil
}

// LoadChildTranscript reads a Claude session JSONL log and converts it into a
// flat slice of MessageEntry values for live observation of a child agent.
//
// Differs from LoadTranscript:
//   - Records whose top-level "timestamp" field is strictly before `since` are
//     filtered out (use zero time.Time to disable). Guards against
//     prior-incarnation pollution when an agent name is reused (QUM-331).
//   - No trailing "Resumed from prior session" status marker — the viewport is
//     a live tail, not a resumed session.
//
// Truncation behavior (leading "earlier messages truncated" status when capped)
// matches LoadTranscript. Missing file → (nil, nil) (no error).
func LoadChildTranscript(path string, since time.Time, maxMessages int) ([]MessageEntry, error) {
	entries, err := scanTranscript(path, since)
	if err != nil || len(entries) == 0 {
		return nil, err
	}
	if maxMessages > 0 && len(entries) > maxMessages {
		entries = entries[len(entries)-maxMessages:]
		entries = append([]MessageEntry{{
			Type:     MessageStatus,
			Content:  "earlier messages truncated",
			Complete: true,
		}}, entries...)
	}
	return entries, nil
}

// scanTranscript opens the JSONL log at path and parses it into MessageEntry
// values, skipping records whose top-level timestamp is before `since` when
// `since` is non-zero. Missing file returns (nil, nil).
func scanTranscript(path string, since time.Time) ([]MessageEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("open transcript %s: %w", path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var entries []MessageEntry
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		recType, _ := rec["type"].(string)
		if recType != "user" && recType != "assistant" {
			continue
		}
		if sc, ok := rec["isSidechain"].(bool); ok && sc {
			continue
		}
		if !since.IsZero() {
			tsStr, _ := rec["timestamp"].(string)
			if tsStr == "" {
				// Records without a timestamp predate the convention or come
				// from a different writer; conservatively skip when filtering.
				continue
			}
			ts, perr := time.Parse(time.RFC3339, tsStr)
			if perr != nil || ts.Before(since) {
				continue
			}
		}
		msg, ok := rec["message"].(map[string]any)
		if !ok {
			continue
		}

		switch recType {
		case "user":
			switch c := msg["content"].(type) {
			case string:
				if c == "" {
					continue
				}
				entries = append(entries, MessageEntry{
					Type:     MessageUser,
					Content:  c,
					Complete: true,
				})
			case []any:
				var parts []string
				for _, b := range c {
					bm, ok := b.(map[string]any)
					if !ok {
						continue
					}
					bt, _ := bm["type"].(string)
					if bt == "text" {
						if txt, ok := bm["text"].(string); ok && txt != "" {
							parts = append(parts, txt)
						}
					}
					// tool_result and others: skip
				}
				joined := strings.Join(parts, "\n")
				if joined != "" {
					entries = append(entries, MessageEntry{
						Type:     MessageUser,
						Content:  joined,
						Complete: true,
					})
				}
			}
		case "assistant":
			blocks, ok := msg["content"].([]any)
			if !ok {
				continue
			}
			for _, b := range blocks {
				bm, ok := b.(map[string]any)
				if !ok {
					continue
				}
				bt, _ := bm["type"].(string)
				switch bt {
				case "text":
					if txt, ok := bm["text"].(string); ok && txt != "" {
						entries = append(entries, MessageEntry{
							Type:     MessageAssistant,
							Content:  txt,
							Complete: true,
						})
					}
				case "tool_use":
					name, _ := bm["name"].(string)
					id, _ := bm["id"].(string)
					var inputRaw json.RawMessage
					if raw, err := json.Marshal(bm["input"]); err == nil {
						inputRaw = raw
					}
					entries = append(entries, MessageEntry{
						Type:          MessageToolCall,
						Content:       name,
						Complete:      true,
						Approved:      true,
						ToolInput:     summarizeToolInput(name, inputRaw),
						ToolInputFull: expandToolInput(name, inputRaw),
						ToolID:        id,
						// Replay-synthesized tool calls are not in flight —
						// the spinner ticker only animates Pending entries.
					})
					// thinking + other types: skip
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan transcript %s: %w", path, err)
	}
	return entries, nil
}
