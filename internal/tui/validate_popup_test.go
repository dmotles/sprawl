package tui

import (
	"strings"
	"testing"
	"time"
)

func newPopupForTest() *ValidatePopupModel {
	m := NewValidatePopupModel(nil, 50*time.Millisecond)
	m.SetSize(80, 24)
	// Anchor clock so elapsed-time renders are deterministic.
	base := time.Date(2026, 5, 18, 14, 0, 0, 0, time.UTC)
	m.SetNow(func() time.Time { return base.Add(3 * time.Second) })
	return &m
}

func mkEvt(step string, kv map[string]string) ValidateEventMsg {
	return ValidateEventMsg{CallID: "call-1", Step: step, KV: kv}
}

func TestPopup_HiddenOnInit(t *testing.T) {
	m := newPopupForTest()
	if m.Visible() {
		t.Error("popup should not be visible on init")
	}
	if v := m.View(); v != "" {
		t.Errorf("View() on hidden = %q, want empty", v)
	}
	if m.State() != PopupHidden {
		t.Errorf("State() = %d, want PopupHidden", m.State())
	}
}

func TestPopup_QueuedTransitionAndBanner(t *testing.T) {
	m := newPopupForTest()
	cmds := m.Handle(mkEvt("merge.queued", map[string]string{"behind": "agent-a"}))
	if m.State() != PopupQueued {
		t.Fatalf("State() = %d, want PopupQueued", m.State())
	}
	if !m.Visible() {
		t.Error("queued popup must be visible")
	}
	if m.Behind() != "agent-a" {
		t.Errorf("Behind() = %q, want agent-a", m.Behind())
	}
	if v := m.View(); !strings.Contains(v, "queued behind merge of agent-a") {
		t.Errorf("View() missing banner; got:\n%s", v)
	}
	if len(cmds) == 0 {
		t.Error("expected at least a tick cmd")
	}
}

func TestPopup_AutoOpenAfterTimer(t *testing.T) {
	m := newPopupForTest()
	m.Handle(mkEvt("merge.validate-started", map[string]string{"cmd": "make validate", "log_path": "/tmp/v.log"}))
	if m.State() != PopupRunningHidden {
		t.Fatalf("State() = %d, want PopupRunningHidden after validate-started", m.State())
	}
	if m.Visible() {
		t.Error("runningHidden must NOT be visible until timer fires")
	}
	m.HandleTimer(validatePopupTimerMsg{at: time.Now()})
	if m.State() != PopupRunningVisible {
		t.Fatalf("State() = %d, want PopupRunningVisible after timer", m.State())
	}
	if !m.Visible() {
		t.Error("runningVisible must be visible")
	}
	if m.Cmd() != "make validate" {
		t.Errorf("Cmd() = %q, want %q", m.Cmd(), "make validate")
	}
	if m.LogPath() != "/tmp/v.log" {
		t.Errorf("LogPath() = %q, want %q", m.LogPath(), "/tmp/v.log")
	}
	v := m.View()
	if !strings.Contains(v, "make validate") {
		t.Errorf("View() missing cmd; got:\n%s", v)
	}
	if !strings.Contains(v, "/tmp/v.log") {
		t.Errorf("View() missing log path; got:\n%s", v)
	}
}

func TestPopup_LineBufferAppendAndCap(t *testing.T) {
	m := newPopupForTest()
	m.Handle(mkEvt("merge.validate-started", map[string]string{"cmd": "x", "log_path": ""}))
	for i := 0; i < validatePopupTailCap+50; i++ {
		m.Handle(mkEvt("merge.validate-line", map[string]string{"line": "line-" + itoa(i)}))
	}
	tail := m.Tail()
	if len(tail) != validatePopupTailCap {
		t.Fatalf("len(tail) = %d, want %d", len(tail), validatePopupTailCap)
	}
	// Oldest retained should be line-50; newest line-249.
	if tail[0] != "line-50" {
		t.Errorf("tail[0] = %q, want line-50", tail[0])
	}
	if tail[len(tail)-1] != "line-249" {
		t.Errorf("tail[-1] = %q, want line-249", tail[len(tail)-1])
	}
}

func TestPopup_MinimizeRestoreToggle(t *testing.T) {
	m := newPopupForTest()
	m.Handle(mkEvt("merge.validate-started", map[string]string{"cmd": "make validate"}))
	m.HandleTimer(validatePopupTimerMsg{})
	if !m.Visible() {
		t.Fatal("setup: popup should be running-visible")
	}

	if !m.ToggleMinimize() {
		t.Error("ToggleMinimize should consume key in runningVisible")
	}
	if m.State() != PopupMinimized {
		t.Errorf("State() = %d, want PopupMinimized", m.State())
	}
	if m.Visible() {
		t.Error("minimized popup must not be Visible()")
	}
	if !m.MinimizedActive() {
		t.Error("MinimizedActive() must be true in minimized state")
	}
	pill := m.Pill()
	if !strings.Contains(pill, "validate:") || !strings.Contains(pill, "running") {
		t.Errorf("Pill() = %q, want substring 'validate:' and 'running'", pill)
	}

	if !m.ToggleMinimize() {
		t.Error("ToggleMinimize should consume key in minimized")
	}
	if m.State() != PopupRunningVisible {
		t.Errorf("State() = %d, want PopupRunningVisible after restore", m.State())
	}
}

func TestPopup_AutoCloseOnSuccess(t *testing.T) {
	m := newPopupForTest()
	m.Handle(mkEvt("merge.validate-started", map[string]string{"cmd": "x"}))
	m.HandleTimer(validatePopupTimerMsg{})
	m.Handle(mkEvt("merge.validate-ended", map[string]string{"exit": "0", "log_path": "/tmp/v.log"}))
	if m.State() != PopupHidden {
		t.Errorf("State() = %d, want PopupHidden after exit=0", m.State())
	}
	if m.Visible() {
		t.Error("popup must be hidden after successful exit")
	}
}

func TestPopup_AutoRestoreOnFailure(t *testing.T) {
	m := newPopupForTest()
	m.Handle(mkEvt("merge.validate-started", map[string]string{"cmd": "x"}))
	m.HandleTimer(validatePopupTimerMsg{})
	// Minimize first to verify failure restores from minimized.
	m.ToggleMinimize()
	if m.State() != PopupMinimized {
		t.Fatal("setup: popup should be minimized")
	}
	m.Handle(mkEvt("merge.validate-ended", map[string]string{"exit": "nonzero", "log_path": "/tmp/v.log", "error": "tests failed"}))
	if m.State() != PopupFailed {
		t.Errorf("State() = %d, want PopupFailed", m.State())
	}
	if !m.Visible() {
		t.Error("popup must auto-restore (Visible) on failure")
	}
	v := m.View()
	if !strings.Contains(v, "FAILED") {
		t.Errorf("View() missing FAILED indicator; got:\n%s", v)
	}
	if !strings.Contains(v, "exit=nonzero") {
		t.Errorf("View() missing exit code; got:\n%s", v)
	}
	if !strings.Contains(v, "/tmp/v.log") {
		t.Errorf("View() missing log path footer; got:\n%s", v)
	}
}

func TestPopup_FailureStateIsSticky(t *testing.T) {
	m := newPopupForTest()
	m.Handle(mkEvt("merge.validate-started", map[string]string{"cmd": "x"}))
	m.HandleTimer(validatePopupTimerMsg{})
	m.Handle(mkEvt("merge.validate-ended", map[string]string{"exit": "nonzero", "error": "boom"}))
	if m.ToggleMinimize() {
		t.Error("ToggleMinimize must not consume key in failed state (sticky)")
	}
	if m.State() != PopupFailed {
		t.Errorf("State() = %d, want PopupFailed after blocked toggle", m.State())
	}
}

func TestPopup_QueuedThenStartTransitionsAreClean(t *testing.T) {
	m := newPopupForTest()
	m.Handle(mkEvt("merge.queued", map[string]string{"behind": "alpha"}))
	if m.State() != PopupQueued {
		t.Fatalf("after queued: state=%d", m.State())
	}
	m.Handle(mkEvt("merge.starting", map[string]string{"behind": "alpha", "waited": "42ms"}))
	// Starting is a no-op for state (validate-started follows).
	if m.State() != PopupQueued {
		t.Errorf("after starting: state=%d, want PopupQueued", m.State())
	}
	m.Handle(mkEvt("merge.validate-started", map[string]string{"cmd": "make validate"}))
	if m.State() != PopupRunningHidden {
		t.Errorf("after validate-started: state=%d, want PopupRunningHidden", m.State())
	}
	// behind should be preserved (informational).
	if m.Behind() != "alpha" {
		t.Errorf("Behind() = %q, want alpha (preserved)", m.Behind())
	}
}

func TestPopup_DismissFromFailedReturnsHidden(t *testing.T) {
	m := newPopupForTest()
	m.Handle(mkEvt("merge.validate-started", map[string]string{"cmd": "x"}))
	m.HandleTimer(validatePopupTimerMsg{})
	m.Handle(mkEvt("merge.validate-ended", map[string]string{"exit": "1", "error": "boom"}))
	if m.State() != PopupFailed {
		t.Fatalf("setup: state=%d, want PopupFailed", m.State())
	}
	if !m.Dismiss() {
		t.Fatal("Dismiss() should return true in PopupFailed")
	}
	if m.State() != PopupHidden {
		t.Errorf("State() = %d, want PopupHidden after Dismiss", m.State())
	}
	if m.Visible() {
		t.Error("Visible() must be false after Dismiss")
	}
}

func TestPopup_DismissNoopWhenNotFailed(t *testing.T) {
	t.Run("hidden", func(t *testing.T) {
		m := newPopupForTest()
		if m.Dismiss() {
			t.Error("Dismiss() should return false when PopupHidden")
		}
		if m.State() != PopupHidden {
			t.Errorf("state changed: %d", m.State())
		}
	})
	t.Run("queued", func(t *testing.T) {
		m := newPopupForTest()
		m.Handle(mkEvt("merge.queued", map[string]string{"behind": "a"}))
		if m.Dismiss() {
			t.Error("Dismiss() should return false when PopupQueued")
		}
		if m.State() != PopupQueued {
			t.Errorf("state changed: %d", m.State())
		}
	})
	t.Run("runningVisible", func(t *testing.T) {
		m := newPopupForTest()
		m.Handle(mkEvt("merge.validate-started", map[string]string{"cmd": "x"}))
		m.HandleTimer(validatePopupTimerMsg{})
		if m.Dismiss() {
			t.Error("Dismiss() should return false when PopupRunningVisible")
		}
		if m.State() != PopupRunningVisible {
			t.Errorf("state changed: %d", m.State())
		}
	})
	t.Run("minimized", func(t *testing.T) {
		m := newPopupForTest()
		m.Handle(mkEvt("merge.validate-started", map[string]string{"cmd": "x"}))
		m.HandleTimer(validatePopupTimerMsg{})
		m.ToggleMinimize()
		if m.Dismiss() {
			t.Error("Dismiss() should return false when PopupMinimized")
		}
		if m.State() != PopupMinimized {
			t.Errorf("state changed: %d", m.State())
		}
	})
}

func TestPopup_FailedFooterContainsDismissHint(t *testing.T) {
	m := newPopupForTest()
	m.Handle(mkEvt("merge.validate-started", map[string]string{"cmd": "x"}))
	m.HandleTimer(validatePopupTimerMsg{})
	m.Handle(mkEvt("merge.validate-ended", map[string]string{"exit": "1", "log_path": "/tmp/v.log", "error": "boom"}))
	v := m.View()
	if !strings.Contains(v, "Esc to dismiss") {
		t.Errorf("View() in PopupFailed missing 'Esc to dismiss' hint; got:\n%s", v)
	}
}

// itoa avoids strconv import bloat in test data builders.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := false
	if i < 0 {
		neg = true
		i = -i
	}
	buf := make([]byte, 0, 12)
	for i > 0 {
		buf = append(buf, byte('0'+i%10))
		i /= 10
	}
	for l, r := 0, len(buf)-1; l < r; l, r = l+1, r-1 {
		buf[l], buf[r] = buf[r], buf[l]
	}
	if neg {
		return "-" + string(buf)
	}
	return string(buf)
}
