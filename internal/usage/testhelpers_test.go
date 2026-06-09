package usage

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dmotles/sprawl/internal/protocol"
	"github.com/dmotles/sprawl/internal/runtime"
)

// assistantEvent builds a fake EventProtocolMessage carrying an assistant
// frame with the given session_id and an inline usage + model block. It is
// the minimum payload the Recorder must parse to accumulate tokens.
func assistantEvent(t *testing.T, sessionID string, u protocol.Usage, model string) runtime.RuntimeEvent {
	t.Helper()
	return assistantEventWithParent(t, sessionID, u, model, nil)
}

// assistantEventWithParent is like assistantEvent but lets the test inject a
// parent_tool_use_id (subagent fold case, QUM-368 ACs).
func assistantEventWithParent(t *testing.T, sessionID string, u protocol.Usage, model string, parentToolUseID *string) runtime.RuntimeEvent {
	t.Helper()
	// Inner content shape: {"model":"...","usage":{...}}
	inner := map[string]any{
		"model": model,
		"usage": u,
	}
	innerBytes, err := json.Marshal(inner)
	if err != nil {
		t.Fatalf("marshal inner content: %v", err)
	}
	// Build the assistant envelope; serialize it then deserialize into
	// protocol.Message so the Raw field is populated consistently with how
	// the real reader does it.
	envelope := map[string]any{
		"type":               "assistant",
		"session_id":         sessionID,
		"message":            json.RawMessage(innerBytes),
		"parent_tool_use_id": parentToolUseID,
	}
	envBytes, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	msg := &protocol.Message{
		Type:      "assistant",
		SessionID: sessionID,
		Raw:       envBytes,
	}
	return runtime.RuntimeEvent{
		Type:    runtime.EventProtocolMessage,
		Message: msg,
	}
}

// turnCompletedEvent builds an EventTurnCompleted with the given per-turn
// total_cost_usd from the Result frame.
func turnCompletedEvent(sessionID string, totalCostUsd float64) runtime.RuntimeEvent {
	return runtime.RuntimeEvent{
		Type: runtime.EventTurnCompleted,
		Result: &protocol.ResultMessage{
			Type:         "result",
			SessionID:    sessionID,
			TotalCostUsd: totalCostUsd,
		},
	}
}

// readNDJSONLines reads all non-empty lines from path, JSON-decoding each one
// into a Record. Fails the test if path does not exist.
func readNDJSONLines(t *testing.T, path string) []Record {
	t.Helper()
	f, err := os.Open(path) //nolint:gosec // test reads file path it constructed
	if err != nil {
		t.Fatalf("open %q: %v", path, err)
	}
	defer f.Close()
	var out []Record
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("unmarshal NDJSON line %q: %v", string(line), err)
		}
		out = append(out, rec)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan %q: %v", path, err)
	}
	return out
}

// usageLogPath returns .sprawl/logs/usage/<agent>/<session>.ndjson.
func usageLogPath(sprawlRoot, agent, sessionID string) string {
	return filepath.Join(sprawlRoot, ".sprawl", "logs", "usage", agent, sessionID+".ndjson")
}
