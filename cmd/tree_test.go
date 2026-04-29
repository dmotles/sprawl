package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/supervisor"
)

func TestRunTree_TextRendersHierarchyAndUnknownLiveness(t *testing.T) {
	var stdout bytes.Buffer
	deps := &treeDeps{
		getenv:       func(string) string { return t.TempDir() },
		readRootName: func(string) string { return "weave" },
		listAgents: func(context.Context) ([]supervisor.AgentInfo, error) {
			return []supervisor.AgentInfo{
				{Name: "alice", Type: "engineer", Family: "engineering", Parent: "weave", Status: "active"},
				{Name: "weave"},
			}, nil
		},
	}

	if err := runTree(deps, &stdout, false, ""); err != nil {
		t.Fatalf("runTree() error: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "weave (root, active, ?)") {
		t.Fatalf("output = %q, want root line with unknown liveness", out)
	}
	if !strings.Contains(out, "alice (engineer/engineering, active, ?)") {
		t.Fatalf("output = %q, want child line with unknown liveness", out)
	}
}

func TestRunTree_JSONIncludesOrphans(t *testing.T) {
	var stdout bytes.Buffer
	deps := &treeDeps{
		getenv:       func(string) string { return t.TempDir() },
		readRootName: func(string) string { return "weave" },
		listAgents: func(context.Context) ([]supervisor.AgentInfo, error) {
			return []supervisor.AgentInfo{
				{Name: "weave"},
				{Name: "alice", Parent: "weave", Status: "active"},
				{Name: "lost", Parent: "ghost", Status: "active"},
			}, nil
		},
	}

	if err := runTree(deps, &stdout, true, ""); err != nil {
		t.Fatalf("runTree() error: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "\"name\": \"weave\"") || !strings.Contains(out, "\"orphans\"") || !strings.Contains(out, "\"name\": \"lost\"") {
		t.Fatalf("json output = %q, want weave + orphan lost", out)
	}
}

func TestRunTree_SubtreeMissingReturnsError(t *testing.T) {
	deps := &treeDeps{
		getenv:       func(string) string { return t.TempDir() },
		readRootName: func(string) string { return "weave" },
		listAgents: func(context.Context) ([]supervisor.AgentInfo, error) {
			return []supervisor.AgentInfo{{Name: "weave"}}, nil
		},
	}

	err := runTree(deps, &bytes.Buffer{}, false, "ghost")
	if err == nil {
		t.Fatal("expected subtree not found error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %q, want not found", err)
	}
}
