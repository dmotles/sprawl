package sprawlmcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/supervisor"
	"github.com/dmotles/sprawl/internal/supervisor/supervisortest"
)

// QUM-588 Part 1: toolMerge must surface queue-wait information when the
// supervisor reports the merge was queued behind another merge. The
// outcome carries QueuedBehind (agent name) and QueueWait (duration);
// toolMerge prepends a "Queued behind merge of <name> (waited <dur>); "
// prefix to its success text.

// queuedMergeSupervisor returns a MergeOutcome populated with the given
// queue-related fields. Embeds NoopSupervisor so the Supervisor interface
// surface remains satisfied as it grows.
type queuedMergeSupervisor struct {
	supervisortest.NoopSupervisor
	outcome *supervisor.MergeOutcome
}

func (q *queuedMergeSupervisor) Merge(_ context.Context, _, _, _ string, _ bool) (*supervisor.MergeOutcome, error) {
	return q.outcome, nil
}

// Test_ToolMerge_FormatsQueuedBehindPrefix — when the merge outcome reports
// QueuedBehind == "alpha" with a positive QueueWait, toolMerge's result text
// must contain both the "Queued behind merge of alpha" prefix substring and
// the "Merged agent ..." success substring.
func Test_ToolMerge_FormatsQueuedBehindPrefix(t *testing.T) {
	mock := &queuedMergeSupervisor{
		outcome: &supervisor.MergeOutcome{
			NoOp:         false,
			QueuedBehind: "alpha",
			QueueWait:    3 * time.Second,
		},
	}
	srv := New(mock)
	msg := makeJSONRPCRequest(900, "tools/call", map[string]any{
		"name": "merge",
		"arguments": map[string]any{
			"agent": "beta",
		},
	})
	resp, err := srv.HandleMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	text := extractToolText(t, resp)
	if !strings.Contains(text, "Queued behind merge of alpha") {
		t.Errorf("text = %q, want to contain \"Queued behind merge of alpha\" (QUM-588 queue surface)", text)
	}
	if !strings.Contains(text, "Merged agent beta") {
		t.Errorf("text = %q, want to also contain \"Merged agent beta\" (success substring)", text)
	}
}

// Test_ToolMerge_NoQueuedPrefixWhenUncontended — when QueuedBehind is empty,
// toolMerge's result text must NOT contain the "Queued behind" prefix.
func Test_ToolMerge_NoQueuedPrefixWhenUncontended(t *testing.T) {
	mock := &queuedMergeSupervisor{
		outcome: &supervisor.MergeOutcome{}, // QueuedBehind = ""
	}
	srv := New(mock)
	msg := makeJSONRPCRequest(901, "tools/call", map[string]any{
		"name": "merge",
		"arguments": map[string]any{
			"agent": "beta",
		},
	})
	resp, err := srv.HandleMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	text := extractToolText(t, resp)
	if strings.Contains(text, "Queued behind") {
		t.Errorf("text = %q, must NOT contain \"Queued behind\" when uncontended", text)
	}
	if !strings.Contains(text, "Merged agent beta") {
		t.Errorf("text = %q, want to contain \"Merged agent beta\"", text)
	}
}
