package tui

import (
	"strings"
	"testing"
	"time"
)

// QUM-497: status-bar surface for in-flight MCP ops.

func TestStatusBar_SetActiveOps_RendersToolCallerAndElapsed(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(200)
	now := time.Date(2026, 5, 8, 12, 0, 30, 0, time.UTC)
	m.SetNowFn(func() time.Time { return now })
	m.SetActiveOps([]OpDescriptor{{
		CallID:  "c1",
		Tool:    "retire",
		Caller:  "weave",
		Step:    "merge.validate-started",
		Started: now.Add(-47 * time.Second),
	}})
	view := m.View()
	if !strings.Contains(view, "retire(weave)") {
		t.Errorf("expected 'retire(weave)' segment, got:\n%s", view)
	}
	if !strings.Contains(view, "merge.validate-started") {
		t.Errorf("expected step label in segment, got:\n%s", view)
	}
	if !strings.Contains(view, "T+0:47s") {
		t.Errorf("expected elapsed 'T+0:47s', got:\n%s", view)
	}
	if !strings.Contains(view, "⏳") {
		t.Errorf("expected hourglass prefix, got:\n%s", view)
	}
}

func TestStatusBar_SetActiveOps_EmptyHidesSegment(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(200)
	m.SetActiveOps([]OpDescriptor{{CallID: "c1", Tool: "retire", Caller: "weave", Started: time.Now()}})
	m.SetActiveOps(nil)
	view := m.View()
	if strings.Contains(view, "⏳") {
		t.Errorf("hourglass should be hidden after clear, got:\n%s", view)
	}
}

func TestStatusBar_SetActiveOps_TruncatesToTwoPlusCount(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(300)
	now := time.Now()
	m.SetNowFn(func() time.Time { return now })
	ops := []OpDescriptor{
		{CallID: "1", Tool: "retire", Caller: "weave", Started: now.Add(-5 * time.Second)},
		{CallID: "2", Tool: "merge", Caller: "weave", Started: now.Add(-3 * time.Second)},
		{CallID: "3", Tool: "spawn", Caller: "weave", Started: now.Add(-1 * time.Second)},
		{CallID: "4", Tool: "kill", Caller: "weave", Started: now},
	}
	m.SetActiveOps(ops)
	view := m.View()
	if !strings.Contains(view, "retire(weave)") || !strings.Contains(view, "merge(weave)") {
		t.Errorf("first two ops should render: %s", view)
	}
	if strings.Contains(view, "spawn(weave)") || strings.Contains(view, "kill(weave)") {
		t.Errorf("ops 3+ should be collapsed into '+N more', got: %s", view)
	}
	if !strings.Contains(view, "+2 more") {
		t.Errorf("expected '+2 more' overflow indicator, got: %s", view)
	}
}

func TestFormatElapsed(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0:00s"},
		{45 * time.Second, "0:45s"},
		{61 * time.Second, "1:01s"},
		{3*time.Minute + 7*time.Second, "3:07s"},
		{59*time.Minute + 59*time.Second, "59:59s"},
		{1*time.Hour + 2*time.Minute + 3*time.Second, "1:02:03"},
		{-5 * time.Second, "0:00s"},
	}
	for _, tc := range cases {
		if got := formatElapsed(tc.d); got != tc.want {
			t.Errorf("formatElapsed(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}
