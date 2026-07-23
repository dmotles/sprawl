package sprawlmcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/dmotles/sprawl/internal/tui"
)

// QUM-898: the `toast` MCP tool lets any agent surface a short toast in
// weave's TUI. It validates/normalizes text, maps severity → ToastStyle, and
// emits a tui.ToastSpawnMsg via the same MsgSender path used by the MCP
// call-indicator notifications.

// captureToast installs a recording MsgSender on srv and returns the single
// ToastSpawnMsg it captured (failing the test if the count is not exactly 1).
func captureToast(t *testing.T, srv *Server, args string) tui.ToastSpawnMsg {
	t.Helper()
	rec := &recordingSender{}
	srv.SetMsgSender(rec.push)
	if _, err := srv.toolToast(context.Background(), json.RawMessage(args)); err != nil {
		t.Fatalf("toolToast(%s): unexpected error: %v", args, err)
	}
	all := rec.snap()
	// Called directly, toolToast must emit EXACTLY one message and nothing
	// else (MCPCallStarted/Ended only wrap the full HandleMessage path).
	if len(all) != 1 {
		t.Fatalf("expected exactly 1 emitted message, got %d: %+v", len(all), all)
	}
	ts, ok := all[0].(tui.ToastSpawnMsg)
	if !ok {
		t.Fatalf("emitted message is %T, want tui.ToastSpawnMsg", all[0])
	}
	return ts
}

func TestToolToast_SeverityMapping(t *testing.T) {
	cases := []struct {
		severity string
		want     tui.ToastStyle
	}{
		{"info", tui.ToastInfo},
		{"warning", tui.ToastWarning},
		{"error", tui.ToastError},
	}
	for _, tc := range cases {
		t.Run(tc.severity, func(t *testing.T) {
			srv := New(&mockSupervisor{})
			args := `{"text":"hello","severity":"` + tc.severity + `"}`
			msg := captureToast(t, srv, args)
			if msg.Toast.Style != tc.want {
				t.Errorf("severity %q → Style %d, want %d", tc.severity, msg.Toast.Style, tc.want)
			}
			if msg.Toast.Text != "hello" {
				t.Errorf("Text = %q, want %q", msg.Toast.Text, "hello")
			}
		})
	}
}

func TestToolToast_InvalidSeverityRejected(t *testing.T) {
	for _, sev := range []string{"critical", "", "INFO"} {
		t.Run(sev, func(t *testing.T) {
			srv := New(&mockSupervisor{})
			args := `{"text":"hi","severity":"` + sev + `"}`
			if _, err := srv.toolToast(context.Background(), json.RawMessage(args)); err == nil {
				t.Fatalf("expected error for severity %q, got nil", sev)
			}
		})
	}
}

func TestToolToast_MissingSeverityRejected(t *testing.T) {
	srv := New(&mockSupervisor{})
	if _, err := srv.toolToast(context.Background(), json.RawMessage(`{"text":"hi"}`)); err == nil {
		t.Fatal("expected error for missing severity, got nil")
	}
}

func TestToolToast_MissingTextRejected(t *testing.T) {
	srv := New(&mockSupervisor{})
	if _, err := srv.toolToast(context.Background(), json.RawMessage(`{"severity":"info"}`)); err == nil {
		t.Fatal("expected error for missing text, got nil")
	}
}

func TestToolToast_EmptyTextRejected(t *testing.T) {
	srv := New(&mockSupervisor{})
	if _, err := srv.toolToast(context.Background(), json.RawMessage(`{"text":"","severity":"info"}`)); err == nil {
		t.Fatal("expected error for empty text, got nil")
	}
}

func TestToolToast_WhitespaceOnlyTextRejected(t *testing.T) {
	srv := New(&mockSupervisor{})
	// Newlines + spaces collapse to empty after normalization → reject.
	if _, err := srv.toolToast(context.Background(), json.RawMessage(`{"text":"  \n\n  ","severity":"info"}`)); err == nil {
		t.Fatal("expected error for whitespace-only text, got nil")
	}
}

func TestToolToast_NewlinesStripped(t *testing.T) {
	srv := New(&mockSupervisor{})
	args := `{"text":"line1\nline2\r\nline3\rline4","severity":"info"}`
	msg := captureToast(t, srv, args)
	if strings.ContainsAny(msg.Toast.Text, "\r\n") {
		t.Errorf("Text still contains newline chars: %q", msg.Toast.Text)
	}
	if want := "line1 line2 line3 line4"; msg.Toast.Text != want {
		t.Errorf("Text = %q, want %q", msg.Toast.Text, want)
	}
}

func TestToolToast_TruncatesToCap(t *testing.T) {
	srv := New(&mockSupervisor{})
	long := strings.Repeat("a", 200)
	args := `{"text":"` + long + `","severity":"info"}`
	msg := captureToast(t, srv, args)
	// 200 runes in → truncated to exactly the 120-rune cap (pins both
	// over- and under-truncation).
	if n := utf8.RuneCountInString(msg.Toast.Text); n != 120 {
		t.Errorf("Text rune count = %d, want exactly 120", n)
	}
}

func TestToolToast_TruncatesMultibyteByRune(t *testing.T) {
	srv := New(&mockSupervisor{})
	// 200 multibyte runes — a byte-based cap would over/under-count.
	long := strings.Repeat("é", 200)
	args := `{"text":"` + long + `","severity":"warning"}`
	msg := captureToast(t, srv, args)
	if n := utf8.RuneCountInString(msg.Toast.Text); n != 120 {
		t.Errorf("Text rune count = %d, want exactly 120", n)
	}
	if !utf8.ValidString(msg.Toast.Text) {
		t.Errorf("Text is not valid UTF-8 after truncation: %q", msg.Toast.Text)
	}
}

func TestToolToast_DefaultTimeoutWhenOmitted(t *testing.T) {
	srv := New(&mockSupervisor{})
	msg := captureToast(t, srv, `{"text":"hi","severity":"info"}`)
	if msg.Toast.DismissOn.Kind != tui.DismissTimer {
		t.Fatalf("DismissOn.Kind = %d, want DismissTimer", msg.Toast.DismissOn.Kind)
	}
	if msg.Toast.DismissOn.Timer != 5*time.Second {
		t.Errorf("Timer = %v, want 5s", msg.Toast.DismissOn.Timer)
	}
}

func TestToolToast_ExplicitTimeout(t *testing.T) {
	srv := New(&mockSupervisor{})
	msg := captureToast(t, srv, `{"text":"hi","severity":"info","timeout_secs":3}`)
	if msg.Toast.DismissOn.Timer != 3*time.Second {
		t.Errorf("Timer = %v, want 3s", msg.Toast.DismissOn.Timer)
	}
}

func TestToolToast_NonPositiveTimeoutClampedToDefault(t *testing.T) {
	for _, args := range []string{
		`{"text":"hi","severity":"info","timeout_secs":0}`,
		`{"text":"hi","severity":"info","timeout_secs":-4}`,
	} {
		srv := New(&mockSupervisor{})
		msg := captureToast(t, srv, args)
		if msg.Toast.DismissOn.Timer != 5*time.Second {
			t.Errorf("%s: Timer = %v, want 5s (default)", args, msg.Toast.DismissOn.Timer)
		}
	}
}

func TestToolToast_OverMaxTimeoutClamped(t *testing.T) {
	srv := New(&mockSupervisor{})
	msg := captureToast(t, srv, `{"text":"hi","severity":"info","timeout_secs":9999}`)
	if msg.Toast.DismissOn.Timer != 60*time.Second {
		t.Errorf("Timer = %v, want 60s (max clamp)", msg.Toast.DismissOn.Timer)
	}
}

func TestToolToast_NilSenderNoPanic(t *testing.T) {
	srv := New(&mockSupervisor{})
	// No SetMsgSender installed → emitMsg no-ops. Tool must still succeed.
	out, err := srv.toolToast(context.Background(), json.RawMessage(`{"text":"hi","severity":"info"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out == "" {
		t.Error("expected non-empty confirmation string")
	}
}

func TestToolToast_ReturnsConfirmation(t *testing.T) {
	srv := New(&mockSupervisor{})
	srv.SetMsgSender(func(any) {})
	out, err := srv.toolToast(context.Background(), json.RawMessage(`{"text":"deploy done","severity":"info"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "deploy done") {
		t.Errorf("confirmation %q does not mention the toast text", out)
	}
}

// TestToolToast_DispatchWiring proves the `case "toast"` wiring in
// dispatchTool routes to toolToast (and that a non-weave caller is not gated).
func TestToolToast_DispatchWiring(t *testing.T) {
	srv := New(&mockSupervisor{})
	rec := &recordingSender{}
	srv.SetMsgSender(rec.push)
	ctx := withTestCallerIdentity(context.Background(), "some-engineer")
	out, err := srv.dispatchTool(ctx, "toast", json.RawMessage(`{"text":"via dispatch","severity":"error"}`))
	if err != nil {
		t.Fatalf("dispatchTool(toast): %v", err)
	}
	if out == "" {
		t.Error("expected non-empty confirmation")
	}
	var found bool
	for _, m := range rec.snap() {
		if ts, ok := m.(tui.ToastSpawnMsg); ok {
			found = true
			if ts.Toast.Style != tui.ToastError {
				t.Errorf("Style = %d, want ToastError", ts.Toast.Style)
			}
		}
	}
	if !found {
		t.Error("no ToastSpawnMsg emitted via dispatchTool")
	}
}

// TestToolToast_Registered asserts the tool is exposed in the base tool
// definitions with the right required fields.
func TestToolToast_Registered(t *testing.T) {
	var def map[string]any
	for _, d := range baseToolDefinitions() {
		if d["name"] == "toast" {
			def = d
			break
		}
	}
	if def == nil {
		t.Fatal("toast not found in baseToolDefinitions()")
	}
	schema, ok := def["inputSchema"].(map[string]any)
	if !ok {
		t.Fatal("toast inputSchema missing or wrong type")
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("toast schema properties missing")
	}
	for _, key := range []string{"text", "severity", "timeout_secs"} {
		if _, ok := props[key]; !ok {
			t.Errorf("toast schema missing property %q", key)
		}
	}
	sevSchema, ok := props["severity"].(map[string]any)
	if !ok {
		t.Fatal("severity property wrong type")
	}
	if _, ok := sevSchema["enum"]; !ok {
		t.Error("severity property missing enum constraint")
	}
	required, ok := schema["required"].([]string)
	if !ok {
		t.Fatal("required missing or wrong type")
	}
	// Order-independent set check: text and severity required, timeout_secs not.
	reqSet := map[string]bool{}
	for _, r := range required {
		reqSet[r] = true
	}
	if !reqSet["text"] || !reqSet["severity"] {
		t.Errorf("required = %v, want to include text and severity", required)
	}
	if reqSet["timeout_secs"] {
		t.Errorf("timeout_secs must be optional, but it is in required: %v", required)
	}
}
