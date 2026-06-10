// QUM-725: tests for the death-observability toast in the TUI. RED phase —
// AgentDiedMsg and BuildDeathToast do not exist yet.
package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestBuildDeathToast_Template_30sAgo(t *testing.T) {
	now := time.Date(2026, 6, 9, 12, 0, 30, 0, time.UTC)
	lastSeen := now.Add(-30 * time.Second)
	msg := AgentDiedMsg{
		Name:     "engineer-abc",
		Type:     "engineer",
		Parent:   "weave",
		LastSeen: lastSeen,
	}
	toast := BuildDeathToast(msg, now)
	want := "engineer-abc (engineer) died — last seen 30s ago. Parent weave notified."
	if toast.Text != want {
		t.Errorf("toast.Text =\n %q\nwant %q", toast.Text, want)
	}
}

func TestBuildDeathToast_ZeroLastSeen_JustNow(t *testing.T) {
	now := time.Date(2026, 6, 9, 12, 0, 30, 0, time.UTC)
	msg := AgentDiedMsg{
		Name:   "engineer-abc",
		Type:   "engineer",
		Parent: "weave",
		// LastSeen is the zero Time.
	}
	toast := BuildDeathToast(msg, now)
	want := "engineer-abc (engineer) died — last seen just now. Parent weave notified."
	if toast.Text != want {
		t.Errorf("toast.Text =\n %q\nwant %q", toast.Text, want)
	}
}

func TestBuildDeathToast_StyleAndDismissContract(t *testing.T) {
	msg := AgentDiedMsg{Name: "n", Type: "t", Parent: "p"}
	toast := BuildDeathToast(msg, time.Now())
	if toast.Style != ToastError {
		t.Errorf("toast.Style = %v, want ToastError", toast.Style)
	}
	if toast.DismissOn.Kind != DismissUserOnly {
		t.Errorf("toast.DismissOn.Kind = %v, want DismissUserOnly", toast.DismissOn.Kind)
	}
}

// TestApp_AgentDiedMsg_SpawnsErrorToast pins the App-level wiring: when
// AgentDiedMsg arrives, the toast (with the templated text + Error style +
// user-only dismissal) lands in m.toasts and renders into the view.
func TestApp_AgentDiedMsg_SpawnsErrorToast(t *testing.T) {
	m := newTestAppModel(t)
	app := applyResize(t, m)
	now := time.Now()
	app = sendMsg(t, app, AgentDiedMsg{
		Name:     "engineer-abc",
		Type:     "engineer",
		Parent:   "weave",
		LastSeen: now.Add(-30 * time.Second),
	})

	toasts := app.toasts.Toasts()
	if len(toasts) != 1 {
		t.Fatalf("expected 1 toast after AgentDiedMsg, got %d", len(toasts))
	}
	got := toasts[0]
	if got.Style != ToastError {
		t.Errorf("toast.Style = %v, want ToastError", got.Style)
	}
	if got.DismissOn.Kind != DismissUserOnly {
		t.Errorf("toast.DismissOn.Kind = %v, want DismissUserOnly", got.DismissOn.Kind)
	}
	if !strings.Contains(got.Text, "engineer-abc (engineer) died") {
		t.Errorf("toast.Text = %q, missing death prefix", got.Text)
	}
	if !strings.Contains(got.Text, "Parent weave notified.") {
		t.Errorf("toast.Text = %q, missing parent-notified suffix", got.Text)
	}

	stripped := ansi.Strip(app.View().Content)
	if !strings.Contains(stripped, "engineer-abc (engineer) died") {
		t.Errorf("view should render death toast text; got:\n%s", stripped)
	}
}

// TestApp_AgentDiedMsg_MultipleStacks pins QUM-697 UX: each AgentDiedMsg
// spawns a new toast — the stack does not collapse.
func TestApp_AgentDiedMsg_MultipleStacks(t *testing.T) {
	m := newTestAppModel(t)
	app := applyResize(t, m)
	now := time.Now()
	app = sendMsg(t, app, AgentDiedMsg{
		Name:     "alpha",
		Type:     "engineer",
		Parent:   "weave",
		LastSeen: now,
	})
	app = sendMsg(t, app, AgentDiedMsg{
		Name:     "beta",
		Type:     "engineer",
		Parent:   "weave",
		LastSeen: now,
	})

	toasts := app.toasts.Toasts()
	if len(toasts) != 2 {
		t.Fatalf("expected 2 death toasts to stack, got %d", len(toasts))
	}
	stripped := ansi.Strip(app.View().Content)
	if !strings.Contains(stripped, "alpha (engineer) died") {
		t.Errorf("view should contain alpha's death toast; got:\n%s", stripped)
	}
	if !strings.Contains(stripped, "beta (engineer) died") {
		t.Errorf("view should contain beta's death toast; got:\n%s", stripped)
	}
}
