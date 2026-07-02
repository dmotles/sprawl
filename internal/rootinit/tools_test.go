package rootinit

import (
	"sort"
	"strings"
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
		"AskUserQuestion",
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

// TestAskUserQuestion_DeprecatedFromRootTools pins QUM-528: the harness-tied
// AskUserQuestion tool is broken under `--print --output-format stream-json`
// (the only mode sprawl uses) and has been superseded by the
// `mcp__sprawl__ask_user_question` MCP tool (QUM-527). It must be absent from
// RootTools and present in BOTH disallow lists for belt-and-suspenders
// enforcement via `--disallowedTools`.
func TestAskUserQuestion_DeprecatedFromRootTools(t *testing.T) {
	for _, tool := range RootTools {
		if tool == "AskUserQuestion" {
			t.Errorf("RootTools must not include AskUserQuestion (deprecated by QUM-528); use mcp__sprawl__ask_user_question instead")
		}
	}
	if !contains(DisallowedTools, "AskUserQuestion") {
		t.Errorf("DisallowedTools must include AskUserQuestion (QUM-528): got %v", DisallowedTools)
	}
	if !contains(ChildDisallowedTools, "AskUserQuestion") {
		t.Errorf("ChildDisallowedTools must include AskUserQuestion (QUM-528): got %v", ChildDisallowedTools)
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func TestModelForAgentType(t *testing.T) {
	tests := []struct {
		agentType string
		want      string
	}{
		{"root", DefaultRootModel},
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

// TestValidSpawnModels_PinnedSet pins QUM-851: the strict enum of models the
// spawn MCP tool accepts. Additions/removals require an explicit update here so
// the schema enum and resolver stay in lockstep.
func TestValidSpawnModels_PinnedSet(t *testing.T) {
	want := []string{"haiku", "sonnet", "opus", "fable", "opus[1m]", "sonnet[1m]"}
	if len(ValidSpawnModels) != len(want) {
		t.Fatalf("ValidSpawnModels = %v, want %v", ValidSpawnModels, want)
	}
	for i := range want {
		if ValidSpawnModels[i] != want[i] {
			t.Errorf("ValidSpawnModels[%d] = %q, want %q", i, ValidSpawnModels[i], want[i])
		}
	}
}

// TestResolveSpawnModel_AcceptsEnum pins QUM-851: every enum value resolves to
// a valid claude --model string (identity today) with no error.
func TestResolveSpawnModel_AcceptsEnum(t *testing.T) {
	for _, m := range []string{"haiku", "sonnet", "opus", "fable", "opus[1m]", "sonnet[1m]"} {
		got, err := ResolveSpawnModel(m)
		if err != nil {
			t.Errorf("ResolveSpawnModel(%q) error: %v", m, err)
			continue
		}
		if got != m {
			t.Errorf("ResolveSpawnModel(%q) = %q, want %q", m, got, m)
		}
	}
}

// TestResolveSpawnModel_RejectsNonEnum pins QUM-851: non-enum values (including
// empty) are rejected with a clear, actionable error naming the bad value.
func TestResolveSpawnModel_RejectsNonEnum(t *testing.T) {
	for _, bad := range []string{"", "gpt-4", "opus-4", "OPUS", "claude-opus-4-8"} {
		got, err := ResolveSpawnModel(bad)
		if err == nil {
			t.Errorf("ResolveSpawnModel(%q) = %q, want error", bad, got)
			continue
		}
		if bad != "" && !strings.Contains(err.Error(), bad) {
			t.Errorf("ResolveSpawnModel(%q) error %q should name the invalid value", bad, err)
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
