package tui

import (
	"encoding/json"
	"errors"
	"os"
	"strings"

	sprawlrt "github.com/dmotles/sprawl/internal/runtime"
	"github.com/dmotles/sprawl/internal/transcript"
)

// ReplayMaxMessages is the default cap on messages loaded from a prior
// session's transcript during resume replay.
const ReplayMaxMessages = 500

// compactContinuationPreamble is the literal prefix of the giant
// context-compaction continuation summary Claude emits as a synthetic user
// frame after an auto-compaction. QUM-904: the wire log carries no
// isCompactSummary flag (that was a Claude-JSONL-only field), so the
// continuation is detected by this content prefix (gated on isSynthetic).
// Verified against a captured wire log (weave/def3f46f… — isSynthetic:true,
// isReplay:false). Suppressed on replay because the first-party compaction
// banner replaces it.
const compactContinuationPreamble = "This session is being continued from a previous conversation that ran out of context."

// LoadTranscript reads a sprawl wire-log NDJSON file and converts it into a
// flat slice of MessageEntry values suitable for pre-populating the viewport
// on resume/resync.
//
// QUM-904: the source is sprawl's own seq'd wire log (via the internal/transcript
// reassembly parser), NOT Claude's JSONL. Because the persisted wire log
// bypasses both live render-drop seams (session.go per-turn send; eventbus
// trySendWithDeadline), content dropped on the live seam reappears correctly
// on rehydrate.
//
// If the file does not exist, or contains no replayable records, (nil, nil) is
// returned.
//
// QUM-676 / QUM-693: pre-S6 this function also prepended an "earlier messages
// truncated" marker on cap-truncation and appended a "Resumed from prior
// session" marker on success. Both were status entries the legacy viewport
// rendered inline; ChatList silently drops them (S5 contract violators). The
// resume/truncate signals are now surface-only — the caller routes them to the
// status-bar transient label after Preload. See cmd/enter.go's
// PreloadTranscript site.
func LoadTranscript(path string, maxMessages int) ([]MessageEntry, error) {
	entries, err := scanWireTranscript(path, sidechainVisible)
	if err != nil || len(entries) == 0 {
		return nil, err
	}
	if maxMessages > 0 && len(entries) > maxMessages {
		entries = entries[len(entries)-maxMessages:]
	}
	return entries, nil
}

// LoadChildTranscript reads a sprawl wire-log NDJSON file and converts it into
// a flat slice of MessageEntry values for live observation of a child agent.
//
// QUM-928: no longer differs from LoadTranscript. Sidechain internals are
// suppressed in child panes too — the issue applies to all agent windows — so
// both legs pass the same sidechainVisible gate as the live mapping and cannot
// drift. Retained as the named child-observe entry point (readChildTranscript).
//
// QUM-904: the QUM-331 `since` timestamp filter is gone. Wire logs are keyed by
// a unique sessionID per incarnation, so a reused agent name gets a brand-new
// .ndjson file — the stale-prior-incarnation pollution the filter guarded
// against is structurally impossible. (The wire also carries no top-level
// timestamp on the vast majority of assistant frames, so a timestamp filter
// would silently drop them.)
//
// Missing file → (nil, nil) (no error).
func LoadChildTranscript(path string, maxMessages int) ([]MessageEntry, error) {
	entries, err := scanWireTranscript(path, sidechainVisible)
	if err != nil || len(entries) == 0 {
		return nil, err
	}
	// QUM-676: truncation marker dropped — see LoadTranscript for rationale.
	if maxMessages > 0 && len(entries) > maxMessages {
		entries = entries[len(entries)-maxMessages:]
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

// scanWireTranscript reads the wire log at path via the shared reassembly
// parser and maps its reconstructed stdout ("out") frames into MessageEntry
// values. Missing file returns (nil, nil).
//
// Only the "out" direction is rendered: it carries Claude's stdout conversation
// stream (real prompts echoed with isReplay, synthetic tool_result user frames,
// and assistant frames), which mirrors the retired Claude-JSONL record shape.
// The "in" direction is the raw stdin we wrote (duplicated on "out" as replay
// echoes) plus control frames — never rendered.
//
// INVARIANT: user prompts rehydrate SOLELY from their "out" isReplay echoes,
// which exist only because the session is launched with ReplayUserMessages=true
// (QUM-817 sets this at both session set-sites). If that flag were ever turned
// off, typed prompts would vanish from rehydration.
//
// The transcript parser preserves within-direction order and reassembles frames
// split across records, so iterating "out" frames in order and emitting a
// MessageEntry per content block reproduces the same sequence the old
// per-line JSONL scan produced.
func scanWireTranscript(path string, includeSidechain bool) ([]MessageEntry, error) {
	t, err := transcript.ParseFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var entries []MessageEntry
	// QUM-379: track in-flight Agent tool calls to assign nesting depth.
	var agentStack []string
	for i := range t.Frames {
		f := &t.Frames[i]
		if f.Dir != "out" || f.Msg == nil {
			continue
		}
		var rec map[string]any
		if json.Unmarshal(f.Raw, &rec) != nil {
			continue
		}
		entries, agentStack = appendRecordEntries(entries, agentStack, rec, includeSidechain)
	}
	return entries, nil
}

// appendRecordEntries maps one decoded conversation record (a "user" or
// "assistant" stdout frame) to zero or more MessageEntry values, applying every
// suppression/mapping rule. It is pure over its inputs and returns the updated
// entries and agentStack slices.
//
// Sidechain identity is wire-native: the wire has no top-level isSidechain
// field, so a frame belongs to a sidechain iff it carries a non-empty
// parent_tool_use_id (QUM-904). That same id supplies the wire-level parent
// tool id for nesting (QUM-577).
//
// QUM-914 Breakage B (DEFERRED to QUM-904): the async Agent tool relocated
// sidechain inner frames OUT of the main Claude transcript into per-subagent
// files, so on replay/resync from that transcript sidechain work is MISSING
// entirely (parent_tool_use_id is absent there). Restoring replay fidelity
// requires rehydrating from the sprawl wire log (which DOES retain the
// interleaved sidechain frames + parent_tool_use_id) — the direction QUM-904
// is already headed. This function's sidechain logic is intentionally left
// unchanged here; QUM-914 only fixes the live TUI path.
func appendRecordEntries(entries []MessageEntry, agentStack []string, rec map[string]any, includeSidechain bool) ([]MessageEntry, []string) {
	recType, _ := rec["type"].(string)
	if recType != "user" && recType != "assistant" {
		return entries, agentStack
	}
	// QUM-904: sidechain-ness derives from parent_tool_use_id (null/absent →
	// main conversation). JSON null unmarshals to nil, so the assertion fails
	// to "" for main-convo frames.
	wireParentToolID, _ := rec["parent_tool_use_id"].(string)
	isSidechain := wireParentToolID != ""
	if isSidechain && !includeSidechain {
		return entries, agentStack
	}
	msg, ok := rec["message"].(map[string]any)
	if !ok {
		return entries, agentStack
	}

	switch recType {
	case "user":
		switch c := msg["content"].(type) {
		case string:
			if c == "" {
				return entries, agentStack
			}
			// QUM-865 / QUM-904: the giant context-compaction continuation
			// summary is a synthetic user frame whose content begins with the
			// preamble. Suppress it on replay — the first-party compaction
			// banner replaces it. Gated on isSynthetic so a user who literally
			// types the preamble is never suppressed. The continuation is
			// always string-form content on the wire (never a []any block
			// array), so this check lives only in the string arm; the retired
			// JSONL path keyed on an isCompactSummary flag the wire lacks.
			if isSyn, _ := rec["isSynthetic"].(bool); isSyn && strings.HasPrefix(strings.TrimSpace(c), compactContinuationPreamble) {
				return entries, agentStack
			}
			// QUM-634: the harness autonomous-turn trigger recorded as a
			// `<task-notification>…</task-notification>` user wrapper renders
			// as a MessageAutoTrigger marker (NOT a raw user bubble leaking the
			// XML), mirroring the live path. A wrapper with no parseable
			// <summary> is suppressed entirely.
			if strings.Contains(c, taskNotificationOpenTag) {
				if summary, ok := parseTaskNotificationSummary(c); ok {
					entries = append(entries, MessageEntry{
						Type:     MessageAutoTrigger,
						Content:  summary,
						Complete: true,
					})
				}
				return entries, agentStack
			}
			// QUM-557 / QUM-562 / QUM-574 / QUM-833: supervisor-injected
			// `<system-notification>` wrapper(s) route through the SAME shared
			// classifier the live pending-zone path uses, so live and replay
			// cannot drift.
			if notifEntries, ok := peelNotificationEntries(c); ok {
				entries = append(entries, notifEntries...)
				return entries, agentStack
			}
			// QUM-924: the auto-continue continuation nudge is injected on the
			// wire as a BARE user string (the shared runtime.AutoContinuePrefix
			// sentinel, no wrapper and no isSynthetic flag). Live rendering draws
			// the "↻ auto-continued" marker from a SEPARATE task_notification
			// system frame, which the reload path drops (:166) — so on replay this
			// user echo is the only surviving trace and must reconstruct the
			// marker as a MessageAutoTrigger (Content is ignored by the renderer,
			// which emits a fixed cue). Prefix-matched, not isSynthetic-gated
			// (the bare frame carries no flag) — a user who literally opens a
			// message with the sentinel is mis-classified on replay only; that is
			// an accepted, cosmetic false-positive window kept narrow via HasPrefix
			// (not Contains).
			if strings.HasPrefix(c, sprawlrt.AutoContinuePrefix) {
				entries = append(entries, MessageEntry{
					Type:     MessageAutoTrigger,
					Content:  "",
					Complete: true,
				})
				return entries, agentStack
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
				// QUM-557 / QUM-562 / QUM-574 / QUM-833: peel
				// `<system-notification>` wrapper(s) via the SAME shared
				// classifier as the string-content branch and the live path.
				if notifEntries, ok := peelNotificationEntries(joined); ok {
					entries = append(entries, notifEntries...)
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
			return entries, agentStack
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
				// QUM-481: nested entries also carry ParentToolID so pure-replay
				// viewport reseeds preserve the parent→child linkage the
				// Agent-container renderer needs.
				var parentID string
				if depth > 0 {
					parentID = agentStack[len(agentStack)-1]
				}
				// QUM-577 / QUM-904: sidechain tool_use frames carry an explicit
				// wire-level parent_tool_use_id pointing at the outer Agent
				// call. Use it directly — the agentStack heuristic would
				// misattribute parallel sidechains.
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
				// QUM-379: push Agent IDs onto the nesting stack. Sidechain
				// frames are inner activity — do not push their tool_use IDs
				// onto the outer agentStack.
				if !isSidechain && name == "Agent" && id != "" {
					agentStack = append(agentStack, id)
				}
				// thinking + other types: skip
			}
		}
	}
	return entries, agentStack
}
