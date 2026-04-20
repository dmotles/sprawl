package cmd

import (
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/rootinit"
)

// freshPrepared is a test fixture representing a fresh-path PreparedSession.
func freshPrepared() *rootinit.PreparedSession {
	return &rootinit.PreparedSession{
		Resume:     false,
		PromptPath: "/fake/sprawl/.sprawl/agents/weave/SYSTEM.md",
		SessionID:  "fake-session-uuid",
		Model:      rootinit.DefaultModel,
		RootTools:  rootinit.RootTools,
		Disallowed: rootinit.DisallowedTools,
	}
}

// resumePrepared is a test fixture representing a resume-path PreparedSession.
func resumePrepared() *rootinit.PreparedSession {
	return &rootinit.PreparedSession{
		Resume:     true,
		PromptPath: "", // empty on resume
		SessionID:  "prior-session-uuid",
		Model:      rootinit.DefaultModel,
		RootTools:  rootinit.RootTools,
		Disallowed: rootinit.DisallowedTools,
	}
}

func TestBuildEnterLaunchOpts_FreshIncludesSystemPromptFile(t *testing.T) {
	opts := buildEnterLaunchOpts(freshPrepared())
	if opts.SystemPromptFile != "/fake/sprawl/.sprawl/agents/weave/SYSTEM.md" {
		t.Errorf("SystemPromptFile = %q, want prepared.PromptPath", opts.SystemPromptFile)
	}
	if opts.Resume {
		t.Error("Resume must be false on fresh path")
	}
	args := opts.BuildArgs()
	if !argsContainPair(args, "--system-prompt-file", "/fake/sprawl/.sprawl/agents/weave/SYSTEM.md") {
		t.Errorf("args missing --system-prompt-file; got %v", args)
	}
	if argsContain(args, "--resume") {
		t.Errorf("fresh args must not contain --resume; got %v", args)
	}
	if !argsContainPair(args, "--session-id", "fake-session-uuid") {
		t.Errorf("fresh args missing --session-id; got %v", args)
	}
}

func TestBuildEnterLaunchOpts_FreshIncludesModelAndSessionID(t *testing.T) {
	opts := buildEnterLaunchOpts(freshPrepared())
	if opts.Model != rootinit.DefaultModel {
		t.Errorf("Model = %q, want %q", opts.Model, rootinit.DefaultModel)
	}
	if opts.SessionID != "fake-session-uuid" {
		t.Errorf("SessionID = %q, want fake-session-uuid", opts.SessionID)
	}
	args := opts.BuildArgs()
	if !argsContainPair(args, "--model", rootinit.DefaultModel) {
		t.Errorf("args missing --model %s; got %v", rootinit.DefaultModel, args)
	}
}

func TestBuildEnterLaunchOpts_ResumeOmitsSystemPromptFileAndSessionIDFlag(t *testing.T) {
	opts := buildEnterLaunchOpts(resumePrepared())
	if !opts.Resume {
		t.Error("Resume must be true on resume path")
	}
	if opts.SystemPromptFile != "" {
		t.Errorf("SystemPromptFile must be empty on resume; got %q", opts.SystemPromptFile)
	}
	args := opts.BuildArgs()
	if argsContain(args, "--system-prompt-file") {
		t.Errorf("resume args must not contain --system-prompt-file; got %v", args)
	}
	if !argsContainPair(args, "--resume", "prior-session-uuid") {
		t.Errorf("resume args missing --resume prior-session-uuid; got %v", args)
	}
	// Mutual exclusion: BuildArgs omits --session-id when Resume=true.
	if argsContain(args, "--session-id") {
		t.Errorf("resume args must not contain --session-id flag; got %v", args)
	}
}

func TestBuildEnterLaunchOpts_IncludesRootToolsAndMCPTools(t *testing.T) {
	opts := buildEnterLaunchOpts(freshPrepared())
	args := opts.BuildArgs()
	for _, tool := range rootinit.RootTools {
		if !argsContainPair(args, "--allowed-tools", tool) {
			t.Errorf("expected --allowed-tools %s in args; got %v", tool, args)
		}
	}
	for _, tool := range sprawlOpsMCPTools() {
		if !argsContainPair(args, "--allowed-tools", tool) {
			t.Errorf("expected --allowed-tools %s in args (mcp); got %v", tool, args)
		}
	}
}

func TestBuildEnterLaunchOpts_IncludesDisallowed(t *testing.T) {
	opts := buildEnterLaunchOpts(freshPrepared())
	args := opts.BuildArgs()
	for _, tool := range []string{"Edit", "Write", "NotebookEdit"} {
		if !argsContainPair(args, "--disallowed-tools", tool) {
			t.Errorf("expected --disallowed-tools %s in args; got %v", tool, args)
		}
	}
}

func TestBuildEnterLaunchOpts_StreamJSONFlags(t *testing.T) {
	opts := buildEnterLaunchOpts(freshPrepared())
	args := opts.BuildArgs()
	want := map[string]string{
		"--input-format":    "stream-json",
		"--output-format":   "stream-json",
		"--permission-mode": "bypassPermissions",
	}
	for flag, val := range want {
		if !argsContainPair(args, flag, val) {
			t.Errorf("expected %s %s in args; got %v", flag, val, args)
		}
	}
	if !argsContain(args, "-p") {
		t.Errorf("expected -p in args; got %v", args)
	}
	if !argsContain(args, "--verbose") {
		t.Errorf("expected --verbose in args; got %v", args)
	}
}

func TestSprawlOpsMCPTools_AllPrefixed(t *testing.T) {
	tools := sprawlOpsMCPTools()
	if len(tools) == 0 {
		t.Fatal("sprawlOpsMCPTools returned empty slice")
	}
	for _, tool := range tools {
		if !strings.HasPrefix(tool, "mcp__sprawl-ops__") {
			t.Errorf("tool %q lacks mcp__sprawl-ops__ prefix", tool)
		}
	}
}

func TestSprawlOpsMCPTools_CoversAllServerTools(t *testing.T) {
	// Pin the expected set — if sprawlmcp adds a tool, update both here and
	// sprawlOpsMCPTools(). This guards against silent drift.
	want := []string{
		"mcp__sprawl-ops__sprawl_spawn",
		"mcp__sprawl-ops__sprawl_status",
		"mcp__sprawl-ops__sprawl_delegate",
		"mcp__sprawl-ops__sprawl_message",
		"mcp__sprawl-ops__sprawl_merge",
		"mcp__sprawl-ops__sprawl_retire",
		"mcp__sprawl-ops__sprawl_kill",
	}
	got := sprawlOpsMCPTools()
	if len(got) != len(want) {
		t.Fatalf("sprawlOpsMCPTools length = %d, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("sprawlOpsMCPTools[%d] = %q, want %q", i, got[i], w)
		}
	}
}

func argsContain(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func argsContainPair(args []string, flag, value string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}
