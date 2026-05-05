package rootinit

import (
	"sort"
	"testing"
)

// TestChildDisallowedTools_PinnedList pins the exact contents of
// ChildDisallowedTools (QUM-470). The harness-only tools below require an
// outer harness session (CronCreate, ScheduleWakeup, etc.) and must NEVER be
// surfaced into the child claude allowlist — they have no meaningful effect
// inside child sessions and pollute the tool list. Use a sorted set
// comparison so future re-orders are tolerated; additions/removals require
// an explicit update of this golden list.
func TestChildDisallowedTools_PinnedList(t *testing.T) {
	want := []string{
		"ScheduleWakeup",
		"Monitor",
		"PushNotification",
		"RemoteTrigger",
		"CronCreate",
		"CronDelete",
		"CronList",
		"EnterWorktree",
		"ExitWorktree",
		"TaskStop",
	}
	got := append([]string(nil), ChildDisallowedTools...)

	sort.Strings(want)
	sort.Strings(got)

	if len(got) != len(want) {
		t.Fatalf("ChildDisallowedTools length = %d, want %d\ngot:  %v\nwant: %v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ChildDisallowedTools[%d] = %q, want %q\nfull got:  %v\nfull want: %v", i, got[i], want[i], got, want)
		}
	}
}

func TestModelForAgentType(t *testing.T) {
	tests := []struct {
		agentType string
		want      string
	}{
		{"manager", DefaultManagerModel},
		{"engineer", DefaultAgentModel},
		{"researcher", DefaultAgentModel},
		{"", DefaultAgentModel},
		{"something-new", DefaultAgentModel},
	}
	for _, tt := range tests {
		if got := ModelForAgentType(tt.agentType); got != tt.want {
			t.Errorf("ModelForAgentType(%q) = %q, want %q", tt.agentType, got, tt.want)
		}
	}
}

func TestModelConstants(t *testing.T) {
	if DefaultRootModel != "opus[1m]" {
		t.Errorf("DefaultRootModel = %q, want %q", DefaultRootModel, "opus[1m]")
	}
	if DefaultManagerModel != "opus[1m]" {
		t.Errorf("DefaultManagerModel = %q, want %q", DefaultManagerModel, "opus[1m]")
	}
	if DefaultAgentModel != "opus" {
		t.Errorf("DefaultAgentModel = %q, want %q", DefaultAgentModel, "opus")
	}
}
