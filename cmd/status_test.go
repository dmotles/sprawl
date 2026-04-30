package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/observe"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/supervisor"
)

func TestRunStatus_JSONUsesRuntimeProcessState(t *testing.T) {
	var stdout bytes.Buffer
	alive := true
	deps := &statusDeps{
		getenv:       func(string) string { return t.TempDir() },
		stdout:       &stdout,
		readRootName: func(string) string { return "weave" },
		listAgents: func(context.Context) ([]supervisor.AgentInfo, error) {
			return []supervisor.AgentInfo{{
				Name:              "alice",
				Type:              "engineer",
				Family:            "engineering",
				Parent:            "weave",
				Status:            "active",
				ProcessAlive:      &alive,
				LastReportType:    "status",
				LastReportMessage: "working",
			}}, nil
		},
	}

	if err := runStatus(deps, true, "", "", "", ""); err != nil {
		t.Fatalf("runStatus() error: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "\"name\": \"alice\"") || !strings.Contains(out, "\"process\": \"alive\"") {
		t.Fatalf("json output = %q, want alice + alive", out)
	}
}

func TestRunStatus_TableFiltersByStatus(t *testing.T) {
	var stdout bytes.Buffer
	alive := true
	deps := &statusDeps{
		getenv:       func(string) string { return t.TempDir() },
		stdout:       &stdout,
		readRootName: func(string) string { return "weave" },
		listAgents: func(context.Context) ([]supervisor.AgentInfo, error) {
			return []supervisor.AgentInfo{
				{Name: "alice", Status: "active", Parent: "weave", ProcessAlive: &alive},
				{Name: "birch", Status: "killed", Parent: "weave"},
			}, nil
		},
	}

	if err := runStatus(deps, false, "", "", "", "active"); err != nil {
		t.Fatalf("runStatus() error: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "alice") || strings.Contains(out, "birch") {
		t.Fatalf("table output = %q, want filtered alice only", out)
	}
}

func TestTolerantListAgents_SkipsCorruptState(t *testing.T) {
	sprawlRoot := t.TempDir()
	if err := os.MkdirAll(state.AgentsDir(sprawlRoot), 0o755); err != nil {
		t.Fatalf("mkdir agents dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(state.AgentsDir(sprawlRoot), "good.json"), []byte(`{"name":"good"}`), 0o644); err != nil {
		t.Fatalf("write good.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(state.AgentsDir(sprawlRoot), "bad.json"), []byte(`{`), 0o644); err != nil {
		t.Fatalf("write bad.json: %v", err)
	}

	var stderr bytes.Buffer
	agents, err := tolerantListAgents(&stderr)(sprawlRoot)
	if err != nil {
		t.Fatalf("tolerantListAgents() error: %v", err)
	}
	if len(agents) != 1 || agents[0].Name != "good" {
		t.Fatalf("agents = %+v, want only good", agents)
	}
	if !strings.Contains(stderr.String(), "skipping corrupt agent state") {
		t.Fatalf("stderr = %q, want corrupt-state warning", stderr.String())
	}
}

func TestRunStatus_TableShowsCostAndAggregate(t *testing.T) {
	var stdout bytes.Buffer
	alive := true
	deps := &statusDeps{
		getenv:       func(string) string { return t.TempDir() },
		stdout:       &stdout,
		readRootName: func(string) string { return "weave" },
		listAgents: func(context.Context) ([]supervisor.AgentInfo, error) {
			return []supervisor.AgentInfo{
				{Name: "alice", Status: "active", Parent: "weave", ProcessAlive: &alive, TotalCostUsd: 0.0312},
				{Name: "bob", Status: "active", Parent: "weave", ProcessAlive: &alive, TotalCostUsd: 0.0188},
			}, nil
		},
	}

	if err := runStatus(deps, false, "", "", "", ""); err != nil {
		t.Fatalf("runStatus() error: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "$0.0312") {
		t.Errorf("table output should contain alice's cost '$0.0312', got:\n%s", out)
	}
	if !strings.Contains(out, "$0.0188") {
		t.Errorf("table output should contain bob's cost '$0.0188', got:\n%s", out)
	}
	if !strings.Contains(out, "Total cost: $0.0500") {
		t.Errorf("table output should contain aggregate 'Total cost: $0.0500', got:\n%s", out)
	}
}

func TestRunStatus_JSONIncludesCost(t *testing.T) {
	var stdout bytes.Buffer
	alive := true
	deps := &statusDeps{
		getenv:       func(string) string { return t.TempDir() },
		stdout:       &stdout,
		readRootName: func(string) string { return "weave" },
		listAgents: func(context.Context) ([]supervisor.AgentInfo, error) {
			return []supervisor.AgentInfo{
				{Name: "alice", Status: "active", Parent: "weave", ProcessAlive: &alive, TotalCostUsd: 0.05},
			}, nil
		},
	}

	if err := runStatus(deps, true, "", "", "", ""); err != nil {
		t.Fatalf("runStatus() error: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "\"total_cost_usd\": 0.05") {
		t.Errorf("json output should contain total_cost_usd, got:\n%s", out)
	}
}

func TestCostDisplay(t *testing.T) {
	if got := costDisplay(0); got != "-" {
		t.Errorf("costDisplay(0) = %q, want %q", got, "-")
	}
	if got := costDisplay(0.0312); got != "$0.0312" {
		t.Errorf("costDisplay(0.0312) = %q, want %q", got, "$0.0312")
	}
}

func TestRunStatusAgent_ShowsCost(t *testing.T) {
	sprawlRoot := t.TempDir()
	agent := &state.AgentState{
		Name:         "alice",
		Type:         "engineer",
		Status:       "active",
		TotalCostUsd: 0.0523,
	}
	if err := state.SaveAgent(sprawlRoot, agent); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}
	// Create activity dir so runStatusAgent doesn't fail on missing activity file.
	actDir := filepath.Join(state.AgentsDir(sprawlRoot), "alice")
	if err := os.MkdirAll(actDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	var stdout bytes.Buffer
	deps := &statusDeps{
		getenv: func(string) string { return sprawlRoot },
		stdout: &stdout,
		stderr: &bytes.Buffer{},
	}

	if err := runStatusAgent(deps, "alice", 0); err != nil {
		t.Fatalf("runStatusAgent() error: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "$0.0523") {
		t.Errorf("single-agent output should contain cost '$0.0523', got:\n%s", out)
	}
}

func TestProcessDisplay(t *testing.T) {
	alive := true
	dead := false
	tests := []struct {
		name string
		info *observe.AgentInfo
		want string
	}{
		{name: "unknown", info: &observe.AgentInfo{}, want: "?"},
		{name: "alive", info: &observe.AgentInfo{ProcessAlive: &alive}, want: "alive"},
		{name: "dead", info: &observe.AgentInfo{ProcessAlive: &dead}, want: "DEAD"},
	}
	for _, tt := range tests {
		if got := processDisplay(tt.info); got != tt.want {
			t.Errorf("%s: processDisplay() = %q, want %q", tt.name, got, tt.want)
		}
	}
}
