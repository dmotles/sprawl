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
