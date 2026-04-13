package tui

import (
	"errors"
	"testing"
)

func TestTurnState_String(t *testing.T) {
	tests := []struct {
		state TurnState
		want  string
	}{
		{TurnIdle, "idle"},
		{TurnThinking, "thinking"},
		{TurnStreaming, "streaming"},
		{TurnComplete, "complete"},
		{TurnState(99), "unknown"},
	}
	for _, tt := range tests {
		got := tt.state.String()
		if got != tt.want {
			t.Errorf("TurnState(%d).String() = %q, want %q", tt.state, got, tt.want)
		}
	}
}

func TestSessionErrorMsg_Error(t *testing.T) {
	msg := SessionErrorMsg{Err: errors.New("process died")}
	if msg.Error() != "process died" {
		t.Errorf("SessionErrorMsg.Error() = %q, want %q", msg.Error(), "process died")
	}
}

func TestAssistantTextMsg_FieldAccess(t *testing.T) {
	msg := AssistantTextMsg{Text: "Hello world"}
	if msg.Text != "Hello world" {
		t.Errorf("Text = %q, want %q", msg.Text, "Hello world")
	}
}

func TestToolCallMsg_FieldAccess(t *testing.T) {
	msg := ToolCallMsg{ToolName: "Bash", ToolID: "tool-1", Approved: true}
	if msg.ToolName != "Bash" {
		t.Errorf("ToolName = %q, want %q", msg.ToolName, "Bash")
	}
	if msg.ToolID != "tool-1" {
		t.Errorf("ToolID = %q, want %q", msg.ToolID, "tool-1")
	}
	if !msg.Approved {
		t.Error("Approved = false, want true")
	}
}

func TestSessionResultMsg_FieldAccess(t *testing.T) {
	msg := SessionResultMsg{
		Result:       "done",
		IsError:      false,
		DurationMs:   200,
		NumTurns:     1,
		TotalCostUsd: 0.05,
	}
	if msg.Result != "done" {
		t.Errorf("Result = %q, want %q", msg.Result, "done")
	}
	if msg.IsError {
		t.Error("IsError = true, want false")
	}
	if msg.DurationMs != 200 {
		t.Errorf("DurationMs = %d, want 200", msg.DurationMs)
	}
	if msg.NumTurns != 1 {
		t.Errorf("NumTurns = %d, want 1", msg.NumTurns)
	}
	if msg.TotalCostUsd != 0.05 {
		t.Errorf("TotalCostUsd = %f, want 0.05", msg.TotalCostUsd)
	}
}

func TestTurnStateMsg_FieldAccess(t *testing.T) {
	msg := TurnStateMsg{State: TurnStreaming}
	if msg.State != TurnStreaming {
		t.Errorf("State = %v, want %v", msg.State, TurnStreaming)
	}
}
