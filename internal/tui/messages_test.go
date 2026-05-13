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

// --- QUM-557: stripSystemNotificationTag helper ---
//
// Contract:
//
//	stripSystemNotificationTag(s) -> (stripped, isInterrupt, ok)
//
// When the entire (whitespace-trimmed) string is wrapped in
// `<system-notification>...</system-notification>`, returns the inner body
// with the wrapping tags removed, isInterrupt=true iff the body starts with
// the literal `[interrupt]` marker (marker is preserved in stripped output),
// and ok=true. Otherwise returns (original, false, false).
func TestStripSystemNotificationTag_AsyncTag(t *testing.T) {
	stripped, isInterrupt, ok := stripSystemNotificationTag("<system-notification>foo</system-notification>")
	if !ok {
		t.Fatalf("ok = false, want true")
	}
	if isInterrupt {
		t.Errorf("isInterrupt = true, want false")
	}
	if stripped != "foo" {
		t.Errorf("stripped = %q, want %q", stripped, "foo")
	}
}

func TestStripSystemNotificationTag_InterruptTag(t *testing.T) {
	stripped, isInterrupt, ok := stripSystemNotificationTag("<system-notification>[interrupt] foo</system-notification>")
	if !ok {
		t.Fatalf("ok = false, want true")
	}
	if !isInterrupt {
		t.Errorf("isInterrupt = false, want true")
	}
	if stripped != "[interrupt] foo" {
		t.Errorf("stripped = %q, want %q (marker preserved)", stripped, "[interrupt] foo")
	}
}

func TestStripSystemNotificationTag_Multiline(t *testing.T) {
	in := "<system-notification>line1\nline2</system-notification>"
	stripped, isInterrupt, ok := stripSystemNotificationTag(in)
	if !ok {
		t.Fatalf("ok = false, want true")
	}
	if isInterrupt {
		t.Errorf("isInterrupt = true, want false")
	}
	if stripped != "line1\nline2" {
		t.Errorf("stripped = %q, want %q (internal newline preserved)", stripped, "line1\nline2")
	}
}

func TestStripSystemNotificationTag_NoTag(t *testing.T) {
	in := "hello world"
	stripped, isInterrupt, ok := stripSystemNotificationTag(in)
	if ok {
		t.Errorf("ok = true, want false (no tag present)")
	}
	if isInterrupt {
		t.Errorf("isInterrupt = true, want false")
	}
	if stripped != in {
		t.Errorf("stripped = %q, want original %q", stripped, in)
	}
}

func TestStripSystemNotificationTag_MalformedMissingClose(t *testing.T) {
	in := "<system-notification>oops"
	stripped, isInterrupt, ok := stripSystemNotificationTag(in)
	if ok {
		t.Errorf("ok = true, want false (no closing tag)")
	}
	if isInterrupt {
		t.Errorf("isInterrupt = true, want false")
	}
	if stripped != in {
		t.Errorf("stripped = %q, want original %q", stripped, in)
	}
}

func TestStripSystemNotificationTag_TagNotAtStart(t *testing.T) {
	in := "prefix<system-notification>x</system-notification>"
	stripped, isInterrupt, ok := stripSystemNotificationTag(in)
	if ok {
		t.Errorf("ok = true, want false (tag must wrap the whole string)")
	}
	if isInterrupt {
		t.Errorf("isInterrupt = true, want false")
	}
	if stripped != in {
		t.Errorf("stripped = %q, want original %q", stripped, in)
	}
}

func TestStripSystemNotificationTag_TableDriven(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantStr string
		wantInt bool
		wantOk  bool
	}{
		{"async simple", "<system-notification>foo</system-notification>", "foo", false, true},
		{"interrupt simple", "<system-notification>[interrupt] foo</system-notification>", "[interrupt] foo", true, true},
		{"multiline body", "<system-notification>a\nb</system-notification>", "a\nb", false, true},
		{"no tag", "hello", "hello", false, false},
		{"missing close", "<system-notification>oops", "<system-notification>oops", false, false},
		{"tag not at start", "x<system-notification>y</system-notification>", "x<system-notification>y</system-notification>", false, false},
		{"surrounding whitespace trimmed", "  <system-notification>body</system-notification>  ", "body", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotStr, gotInt, gotOk := stripSystemNotificationTag(tt.in)
			if gotOk != tt.wantOk {
				t.Errorf("ok = %v, want %v", gotOk, tt.wantOk)
			}
			if gotInt != tt.wantInt {
				t.Errorf("isInterrupt = %v, want %v", gotInt, tt.wantInt)
			}
			if gotStr != tt.wantStr {
				t.Errorf("stripped = %q, want %q", gotStr, tt.wantStr)
			}
		})
	}
}
