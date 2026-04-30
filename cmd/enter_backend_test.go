package cmd

import (
	"io"
	"testing"

	"github.com/dmotles/sprawl/internal/host"
	"github.com/dmotles/sprawl/internal/sprawlmcp"
)

func TestBuildEnterSessionSpec_FreshPathUsesRootSessionData(t *testing.T) {
	called := false
	spec := buildEnterSessionSpec("/repo", freshPrepared(), io.Discard, func() { called = true })

	if spec.WorkDir != "/repo" {
		t.Errorf("WorkDir = %q, want /repo", spec.WorkDir)
	}
	if spec.Identity != "weave" {
		t.Errorf("Identity = %q, want weave", spec.Identity)
	}
	if spec.SprawlRoot != "/repo" {
		t.Errorf("SprawlRoot = %q, want /repo", spec.SprawlRoot)
	}
	if spec.SessionID != "fake-session-uuid" {
		t.Errorf("SessionID = %q, want fake-session-uuid", spec.SessionID)
	}
	if spec.Resume {
		t.Error("Resume must be false on fresh path")
	}
	if spec.PromptFile != "/fake/sprawl/.sprawl/agents/weave/SYSTEM.md" {
		t.Errorf("PromptFile = %q, want prepared prompt path", spec.PromptFile)
	}
	if spec.Model != freshPrepared().Model {
		t.Errorf("Model = %q, want %q", spec.Model, freshPrepared().Model)
	}
	if spec.PermissionMode != "bypassPermissions" {
		t.Errorf("PermissionMode = %q, want bypassPermissions", spec.PermissionMode)
	}
	wantAllowed := append(append([]string{}, freshPrepared().RootTools...), sprawlmcp.MCPToolNames()...)
	if len(spec.AllowedTools) != len(wantAllowed) {
		t.Fatalf("AllowedTools length = %d, want %d", len(spec.AllowedTools), len(wantAllowed))
	}
	for i, want := range wantAllowed {
		if spec.AllowedTools[i] != want {
			t.Errorf("AllowedTools[%d] = %q, want %q", i, spec.AllowedTools[i], want)
		}
	}
	if len(spec.DisallowedTools) == 0 {
		t.Fatal("DisallowedTools should be preserved")
	}
	if spec.OnResumeFailure == nil {
		t.Fatal("OnResumeFailure should be threaded through")
	}

	spec.OnResumeFailure()
	if !called {
		t.Fatal("OnResumeFailure callback did not run")
	}
}

func TestBuildEnterSessionSpec_ResumePathPreservesResumeState(t *testing.T) {
	called := false
	spec := buildEnterSessionSpec("/repo", resumePrepared(), io.Discard, func() { called = true })

	if !spec.Resume {
		t.Fatal("Resume must be true on resume path")
	}
	if spec.PromptFile != "/fake/sprawl/.sprawl/agents/weave/SYSTEM.md" {
		t.Errorf("PromptFile = %q, want prepared.PromptPath on resume path", spec.PromptFile)
	}
	if spec.SessionID != "prior-session-uuid" {
		t.Errorf("SessionID = %q, want prior-session-uuid", spec.SessionID)
	}
	if spec.PermissionMode != "bypassPermissions" {
		t.Errorf("PermissionMode = %q, want bypassPermissions", spec.PermissionMode)
	}
	wantAllowed := append(append([]string{}, resumePrepared().RootTools...), sprawlmcp.MCPToolNames()...)
	if len(spec.AllowedTools) != len(wantAllowed) {
		t.Fatalf("AllowedTools length = %d, want %d", len(spec.AllowedTools), len(wantAllowed))
	}
	for i, want := range wantAllowed {
		if spec.AllowedTools[i] != want {
			t.Errorf("AllowedTools[%d] = %q, want %q", i, spec.AllowedTools[i], want)
		}
	}
	if len(spec.DisallowedTools) != len(resumePrepared().Disallowed) {
		t.Fatalf("DisallowedTools length = %d, want %d", len(spec.DisallowedTools), len(resumePrepared().Disallowed))
	}
	for i, want := range resumePrepared().Disallowed {
		if spec.DisallowedTools[i] != want {
			t.Errorf("DisallowedTools[%d] = %q, want %q", i, spec.DisallowedTools[i], want)
		}
	}
	if spec.OnResumeFailure == nil {
		t.Fatal("OnResumeFailure should be preserved on resume path")
	}

	spec.OnResumeFailure()
	if !called {
		t.Fatal("OnResumeFailure callback did not run")
	}
}

func TestBuildEnterInitSpec_UsesSprawlOpsBridge(t *testing.T) {
	bridge := host.NewMCPBridge()
	initSpec := buildEnterInitSpec(bridge)

	if len(initSpec.MCPServerNames) != 1 || initSpec.MCPServerNames[0] != "sprawl" {
		t.Fatalf("MCPServerNames = %v, want [sprawl]", initSpec.MCPServerNames)
	}
	if initSpec.ToolBridge != bridge {
		t.Fatal("ToolBridge should be the provided MCP bridge")
	}
}
