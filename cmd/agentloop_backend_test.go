package cmd

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/agentloop"
	backend "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/protocol"
	"github.com/dmotles/sprawl/internal/rootinit"
	"github.com/dmotles/sprawl/internal/state"
)

func TestBuildAgentSessionSpec_FromAgentState(t *testing.T) {
	agentState := &state.AgentState{
		Name:      "finn",
		Worktree:  "/repo/.sprawl/worktrees/finn",
		SessionID: "sess-123",
	}

	spec := buildAgentSessionSpec(agentState, "/repo/.sprawl/agents/finn/SYSTEM.md", "/repo")

	if spec.WorkDir != "/repo/.sprawl/worktrees/finn" {
		t.Errorf("WorkDir = %q, want agent worktree", spec.WorkDir)
	}
	if spec.Identity != "finn" {
		t.Errorf("Identity = %q, want finn", spec.Identity)
	}
	if spec.SprawlRoot != "/repo" {
		t.Errorf("SprawlRoot = %q, want /repo", spec.SprawlRoot)
	}
	if spec.SessionID != "sess-123" {
		t.Errorf("SessionID = %q, want sess-123", spec.SessionID)
	}
	if spec.PromptFile != "/repo/.sprawl/agents/finn/SYSTEM.md" {
		t.Errorf("PromptFile = %q, want system prompt path", spec.PromptFile)
	}
	if spec.Model != rootinit.DefaultModel {
		t.Errorf("Model = %q, want %q", spec.Model, rootinit.DefaultModel)
	}
	if spec.PermissionMode != "bypassPermissions" {
		t.Errorf("PermissionMode = %q, want bypassPermissions", spec.PermissionMode)
	}
	if spec.Effort != "medium" {
		t.Errorf("Effort = %q, want medium", spec.Effort)
	}
	if spec.Resume {
		t.Error("child loop launches should be fresh process sessions")
	}
	if spec.OnResumeFailure != nil {
		t.Error("child sessions should not install the root resume watcher")
	}
}

func TestRunAgentLoop_BackendProcessCrash_RestartUsesResumeSpec(t *testing.T) {
	deps, tmpDir, _ := newTestAgentLoopDeps(t)
	deps.newProcess = nil

	if _, err := state.EnqueueTask(tmpDir, "finn", "do work"); err != nil {
		t.Fatalf("creating task: %v", err)
	}

	callCount := 0
	var specs []backend.SessionSpec
	ctx, cancel := context.WithCancel(context.Background())

	deps.newBackendProcess = func(spec backend.SessionSpec, observer agentloop.Observer) processManager {
		specs = append(specs, spec)
		callCount++
		if callCount == 1 {
			return &mockProcessManager{
				sendResults: []*protocol.ResultMessage{
					{Type: "result", Result: "initial done"},
				},
				sendErrors: []error{nil, errors.New("process crashed")},
			}
		}
		deps.sleepFunc = func(time.Duration) { cancel() }
		return &mockProcessManager{
			sendResults: []*protocol.ResultMessage{
				{Type: "result", Result: "recovered"},
			},
		}
	}

	_ = runAgentLoop(ctx, deps, "finn")

	if len(specs) < 2 {
		t.Fatalf("expected at least 2 backend process creations, got %d", len(specs))
	}
	if specs[0].Resume {
		t.Error("initial backend session should not launch in resume mode")
	}
	if !specs[1].Resume {
		t.Error("restarted backend session should have Resume=true")
	}
	if specs[1].SessionID != specs[0].SessionID {
		t.Errorf("restarted SessionID = %q, want %q", specs[1].SessionID, specs[0].SessionID)
	}
}
