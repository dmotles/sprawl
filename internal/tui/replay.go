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
	entries, err := scanTranscriptWithSidechain(path, since, true)
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

// extractToolResultContent decodes the polymorphic `content` field of a
// tool_result block from a generic JSON unmarshal (map[string]any). The
// Anthropic protocol allows it to be a plain string or an array of
// {type:"text", text:"..."} blocks.
func extractToolResultContent(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, elem := range c {
			m, ok := elem.(map[string]any)
			if !ok {
				continue
			}
			if t, _ := m["type"].(string); t == "text" {
				if txt, ok := m["text"].(string); ok && txt != "" {
					parts = append(parts, txt)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

// scanTranscript opens the JSONL log at path and parses it into MessageEntry
// values, skipping records whose top-level timestamp is before `since` when
// `since` is non-zero. Missing file returns (nil, nil).
//
//nolint:unparam // since parameter retained for test call sites and symmetry with scanTranscriptWithSidechain.
func scanTranscript(path string, since time.Time) ([]MessageEntry, error) {
	return scanTranscriptWithSidechain(path, since, false)
}

func scanTranscriptWithSidechain(path string, since time.Time, includeSidechain bool) ([]MessageEntry, error) {
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
	// QUM-379: track in-flight Agent tool calls to assign nesting depth.
	var agentStack []string
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
		isSidechain, _ := rec["isSidechain"].(bool)
		if isSidechain && !includeSidechain {
			continue
		}
		// QUM-577: for sidechain records, read parent_tool_use_id from the
		// top-level JSONL record (wire-level), not from the agentStack
		// heuristic. Parallel sub-agents would otherwise be misattributed.
		wireParentToolID, _ := rec["parent_tool_use_id"].(string)
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
				// QUM-557 / QUM-562 / QUM-574: detect supervisor-injected
				// `<system-notification>` wrapper(s) so resumed/replayed
				// transcripts render identically to the live-drain path
				// (same color, same glyph, no tags). The parser peels one
				// envelope per call and returns the typed `type` attribute
				// so status_change vs message-class branches correctly at
				// render time. The peel-loop handles back-to-back envelopes
				// (status_change + message in the same user-message body)
				// so each renders as a distinct entry.
				if stripped, notifType, isInterrupt, remaining, ok := stripSystemNotificationTag(c); ok {
					entries = append(entries, MessageEntry{
						Type:             MessageSystemNotification,
						Content:          stripped,
						Complete:         true,
						Interrupt:        isInterrupt,
						NotificationType: notifType,
					})
					for {
						s2, t2, i2, rem2, ok2 := stripSystemNotificationTag(remaining)
						if !ok2 {
							break
						}
						entries = append(entries, MessageEntry{
							Type:             MessageSystemNotification,
							Content:          s2,
							Complete:         true,
							Interrupt:        i2,
							NotificationType: t2,
						})
						remaining = rem2
					}
					if strings.TrimSpace(remaining) != "" {
						entries = append(entries, MessageEntry{
							Type:     MessageUser,
							Content:  remaining,
							Complete: true,
						})
					}
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
					// QUM-379: tool_result blocks pop Agent IDs from the nesting stack.
					// QUM-388: also patch result content onto the matching tool call entry.
					if bt == "tool_result" {
						tid, _ := bm["tool_use_id"].(string)
						for j := len(agentStack) - 1; j >= 0; j-- {
							if agentStack[j] == tid {
								agentStack = append(agentStack[:j], agentStack[j+1:]...)
								break
							}
						}
						content := extractToolResultContent(bm["content"])
						isError, _ := bm["is_error"].(bool)
						for k := len(entries) - 1; k >= 0; k-- {
							if entries[k].Type == MessageToolCall && entries[k].ToolID == tid {
								entries[k].Result = content
								entries[k].Failed = isError
								break
							}
						}
					}
				}
				joined := strings.Join(parts, "\n")
				if joined != "" {
					// QUM-557 / QUM-562 / QUM-574: detect
					// `<system-notification>` wrapper(s) on the joined
					// text-block body so array-form replay matches the
					// live-drain rendering on restart. MUST stay symmetric
					// with the string-content branch above (QUM-557
					// lesson: silent replay divergence). The peel-loop
					// handles back-to-back envelopes.
					if stripped, notifType, isInterrupt, remaining, ok := stripSystemNotificationTag(joined); ok {
						entries = append(entries, MessageEntry{
							Type:             MessageSystemNotification,
							Content:          stripped,
							Complete:         true,
							Interrupt:        isInterrupt,
							NotificationType: notifType,
						})
						for {
							s2, t2, i2, rem2, ok2 := stripSystemNotificationTag(remaining)
							if !ok2 {
								break
							}
							entries = append(entries, MessageEntry{
								Type:             MessageSystemNotification,
								Content:          s2,
								Complete:         true,
								Interrupt:        i2,
								NotificationType: t2,
							})
							remaining = rem2
						}
						if strings.TrimSpace(remaining) != "" {
							entries = append(entries, MessageEntry{
								Type:     MessageUser,
								Content:  remaining,
								Complete: true,
							})
						}
					} else {
						entries = append(entries, MessageEntry{
							Type:     MessageUser,
							Content:  joined,
							Complete: true,
						})
					}
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
					depth := len(agentStack)
					// QUM-481: nested entries also carry ParentToolID so
					// pure-replay viewport reseeds (no prior live render)
					// preserve the parent→child linkage needed by the
					// Agent-container renderer.
					var parentID string
					if depth > 0 {
						parentID = agentStack[len(agentStack)-1]
					}
					// QUM-577: sidechain tool_use records carry an explicit
					// wire-level parent_tool_use_id pointing at the outer
					// Agent call. Use it directly — the agentStack heuristic
					// would misattribute parallel sub-agents (see
					// TestLoadChildTranscript_SidechainParallelAgents_ParentToolIDFromWire).
					if isSidechain && wireParentToolID != "" {
						parentID = wireParentToolID
						if depth < 1 {
							depth = 1
						}
					}
					headerArg, headerParams := FormatToolHeader(name, inputRaw)
					entries = append(entries, MessageEntry{
						Type:          MessageToolCall,
						Content:       name,
						Complete:      true,
						Approved:      true,
						ToolInput:     summarizeToolInput(name, inputRaw),
						ToolInputFull: expandToolInput(name, inputRaw),
						ToolID:        id,
						Depth:         depth,
						ParentToolID:  parentID,
						HeaderArg:     headerArg,
						HeaderParams:  headerParams,
						// Replay-synthesized tool calls are not in flight —
						// the spinner ticker only animates Pending entries.
					})
					// QUM-379: push Agent IDs onto the nesting stack.
					// Sidechain records are inner sub-agent activity — do
					// not push their tool_use IDs onto the outer agentStack.
					if !isSidechain && name == "Agent" && id != "" {
						agentStack = append(agentStack, id)
					}
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
