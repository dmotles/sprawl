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

// --- QUM-557 / QUM-562: stripSystemNotificationTag helper ---
//
// Contract (QUM-562):
//
//	stripSystemNotificationTag(s) -> (body, notifType, isInterrupt, ok)
//
// When the entire (whitespace-trimmed) string is wrapped in
// `<system-notification [attrs]>...</system-notification>`, returns the inner
// body with the wrapping tags removed, the parsed `type` attribute (defaults
// to "message" when absent or unrecognized), isInterrupt=true iff either the
// `interrupt="true"` attribute is set OR (back-compat) the body starts with
// the literal `[interrupt]` marker, and ok=true. Otherwise returns
// (original, "", false, false). The body is returned verbatim — any inner
// `[interrupt]` marker is preserved so the renderer can both color-code and
// display it.
func TestStripSystemNotificationTag_TypedMessage(t *testing.T) {
	body, notifType, isInterrupt, ok := stripSystemNotificationTag(`<system-notification type="message">foo</system-notification>`)
	if !ok {
		t.Fatalf("ok = false, want true")
	}
	if notifType != NotificationKindMessage {
		t.Errorf("notifType = %q, want %q", notifType, NotificationKindMessage)
	}
	if isInterrupt {
		t.Errorf("isInterrupt = true, want false")
	}
	if body != "foo" {
		t.Errorf("body = %q, want %q", body, "foo")
	}
}

func TestStripSystemNotificationTag_TypedMessageInterrupt(t *testing.T) {
	body, notifType, isInterrupt, ok := stripSystemNotificationTag(`<system-notification type="message" interrupt="true">[interrupt] foo</system-notification>`)
	if !ok {
		t.Fatalf("ok = false, want true")
	}
	if notifType != NotificationKindMessage {
		t.Errorf("notifType = %q, want %q", notifType, NotificationKindMessage)
	}
	if !isInterrupt {
		t.Errorf("isInterrupt = false, want true (interrupt=\"true\" attr)")
	}
	if body != "[interrupt] foo" {
		t.Errorf("body = %q, want %q (marker preserved)", body, "[interrupt] foo")
	}
}

func TestStripSystemNotificationTag_TypedStatusChange(t *testing.T) {
	body, notifType, isInterrupt, ok := stripSystemNotificationTag(`<system-notification type="status_change">finn changed status to working: doing X</system-notification>`)
	if !ok {
		t.Fatalf("ok = false, want true")
	}
	if notifType != NotificationKindStatusChange {
		t.Errorf("notifType = %q, want %q", notifType, NotificationKindStatusChange)
	}
	if isInterrupt {
		t.Errorf("isInterrupt = true, want false")
	}
	if body != "finn changed status to working: doing X" {
		t.Errorf("body = %q", body)
	}
}

// TestStripSystemNotificationTag_UntaggedLegacyAsync — back-compat: untyped
// `<system-notification>` wrappers (persisted before QUM-562 shipped) must
// parse as type="message" with isInterrupt=false.
func TestStripSystemNotificationTag_UntaggedLegacyAsync(t *testing.T) {
	body, notifType, isInterrupt, ok := stripSystemNotificationTag(`<system-notification>foo</system-notification>`)
	if !ok {
		t.Fatalf("ok = false, want true")
	}
	if notifType != NotificationKindMessage {
		t.Errorf("notifType = %q, want %q (legacy untyped defaults to message)", notifType, NotificationKindMessage)
	}
	if isInterrupt {
		t.Errorf("isInterrupt = true, want false")
	}
	if body != "foo" {
		t.Errorf("body = %q", body)
	}
}

// TestStripSystemNotificationTag_UntaggedLegacyInterrupt — back-compat: untyped
// wrapper with inner `[interrupt]` marker must yield isInterrupt=true.
func TestStripSystemNotificationTag_UntaggedLegacyInterrupt(t *testing.T) {
	body, notifType, isInterrupt, ok := stripSystemNotificationTag(`<system-notification>[interrupt] foo</system-notification>`)
	if !ok {
		t.Fatalf("ok = false, want true")
	}
	if notifType != NotificationKindMessage {
		t.Errorf("notifType = %q, want %q", notifType, NotificationKindMessage)
	}
	if !isInterrupt {
		t.Errorf("isInterrupt = false, want true (inner [interrupt] marker)")
	}
	if body != "[interrupt] foo" {
		t.Errorf("body = %q (marker preserved)", body)
	}
}

func TestStripSystemNotificationTag_Multiline(t *testing.T) {
	in := `<system-notification type="message">line1` + "\n" + `line2</system-notification>`
	body, notifType, isInterrupt, ok := stripSystemNotificationTag(in)
	if !ok {
		t.Fatalf("ok = false, want true")
	}
	if notifType != NotificationKindMessage {
		t.Errorf("notifType = %q", notifType)
	}
	if isInterrupt {
		t.Errorf("isInterrupt = true, want false")
	}
	if body != "line1\nline2" {
		t.Errorf("body = %q", body)
	}
}

func TestStripSystemNotificationTag_NoTag(t *testing.T) {
	in := "hello world"
	body, notifType, isInterrupt, ok := stripSystemNotificationTag(in)
	if ok {
		t.Errorf("ok = true, want false (no tag present)")
	}
	if notifType != "" {
		t.Errorf("notifType = %q, want empty", notifType)
	}
	if isInterrupt {
		t.Errorf("isInterrupt = true, want false")
	}
	if body != in {
		t.Errorf("body = %q, want original", body)
	}
}

func TestStripSystemNotificationTag_MalformedMissingClose(t *testing.T) {
	in := "<system-notification>oops"
	body, _, _, ok := stripSystemNotificationTag(in)
	if ok {
		t.Errorf("ok = true, want false (no closing tag)")
	}
	if body != in {
		t.Errorf("body = %q, want original", body)
	}
}

func TestStripSystemNotificationTag_TagNotAtStart(t *testing.T) {
	in := "prefix<system-notification>x</system-notification>"
	body, _, _, ok := stripSystemNotificationTag(in)
	if ok {
		t.Errorf("ok = true, want false (tag must wrap the whole string)")
	}
	if body != in {
		t.Errorf("body = %q, want original", body)
	}
}

// TestStripSystemNotificationTag_UnknownTypeFallsBackToMessage — YAGNI guard
// (per QUM-562 design decision #5): unrecognized `type` values must not crash
// the parser; they fall back to type="message" so an updated emitter can ship
// without breaking older TUI binaries mid-rollout.
func TestStripSystemNotificationTag_UnknownTypeFallsBackToMessage(t *testing.T) {
	body, notifType, _, ok := stripSystemNotificationTag(`<system-notification type="something_new">hello</system-notification>`)
	if !ok {
		t.Fatalf("ok = false, want true")
	}
	if notifType != NotificationKindMessage {
		t.Errorf("notifType = %q, want %q (unknown type falls back)", notifType, NotificationKindMessage)
	}
	if body != "hello" {
		t.Errorf("body = %q", body)
	}
}

// TestStripSystemNotificationTag_AttributeRobustness — the parser should be
// permissive about attribute formatting (single quotes, surrounding
// whitespace) even though the canonical emitter always produces double-quoted
// canonical form. Malformed attribute syntax falls back to defaults but does
// not break the parse.
func TestStripSystemNotificationTag_AttributeRobustness(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		wantType string
		wantBody string
		wantInt  bool
		wantOk   bool
	}{
		{
			name:     "single-quoted type",
			in:       `<system-notification type='status_change'>x</system-notification>`,
			wantType: NotificationKindStatusChange,
			wantBody: "x",
			wantInt:  false,
			wantOk:   true,
		},
		{
			name:     "extra whitespace in attrs",
			in:       `<system-notification   type="message"   interrupt="true"  >[interrupt] x</system-notification>`,
			wantType: NotificationKindMessage,
			wantBody: "[interrupt] x",
			wantInt:  true,
			wantOk:   true,
		},
		{
			name:     "interrupt attr without explicit type",
			in:       `<system-notification interrupt="true">[interrupt] x</system-notification>`,
			wantType: NotificationKindMessage,
			wantBody: "[interrupt] x",
			wantInt:  true,
			wantOk:   true,
		},
		{
			name:     "surrounding whitespace trimmed",
			in:       `  <system-notification type="message">body</system-notification>  `,
			wantType: NotificationKindMessage,
			wantBody: "body",
			wantInt:  false,
			wantOk:   true,
		},
		{
			name:     "empty body",
			in:       `<system-notification type="message"></system-notification>`,
			wantType: NotificationKindMessage,
			wantBody: "",
			wantInt:  false,
			wantOk:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, notifType, isInterrupt, ok := stripSystemNotificationTag(tt.in)
			if ok != tt.wantOk {
				t.Errorf("ok = %v, want %v", ok, tt.wantOk)
			}
			if notifType != tt.wantType {
				t.Errorf("notifType = %q, want %q", notifType, tt.wantType)
			}
			if isInterrupt != tt.wantInt {
				t.Errorf("isInterrupt = %v, want %v", isInterrupt, tt.wantInt)
			}
			if body != tt.wantBody {
				t.Errorf("body = %q, want %q", body, tt.wantBody)
			}
		})
	}
}
