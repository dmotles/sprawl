package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Agent status string constants (QUM-372). These enumerate the universe of
// values that may appear in AgentState.Status. They are intentionally not
// enforced by the persistence layer — the field remains a free-form string for
// back-compat with older state files — but every new write-site in the
// codebase should reference one of these constants so the set stays closed.
const (
	StatusActive       = "active"
	StatusRunning      = "running"
	StatusSuspended    = "suspended"
	StatusKilled       = "killed"
	StatusRetired      = "retired"
	StatusRetiring     = "retiring"
	StatusDone         = "done"
	StatusResumeFailed = "resume_failed"
	StatusFaulted      = "faulted"
	StatusStopped      = "stopped"
)

// CurrentSchemaVersion is the schema version stamped onto agent state files by
// the current code. LoadAgent migrates older (v0) files forward on read and
// SaveAgent stamps this value (QUM-625 M4).
const CurrentSchemaVersion = 1

// AgentState holds the persistent metadata for a spawned agent.
type AgentState struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Family    string `json:"family"`
	Parent    string `json:"parent"`
	Prompt    string `json:"prompt"`
	Branch    string `json:"branch"`
	Worktree  string `json:"worktree"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	SessionID string `json:"session_id,omitempty"`
	Subagent  bool   `json:"subagent,omitempty"`
	TreePath  string `json:"tree_path,omitempty"`

	// SchemaVersion records the persisted schema version. Files written before
	// QUM-625 M4 lack this field and unmarshal as 0 (v0); LoadAgent migrates
	// them forward and stamps CurrentSchemaVersion.
	SchemaVersion int `json:"schema_version,omitempty"`

	// Cost fields — persisted from Claude's result message after each turn.
	TotalCostUsd     float64 `json:"total_cost_usd,omitempty"`
	LastCostUpdateAt string  `json:"last_cost_update_at,omitempty"` // RFC3339

	// Report fields — populated by the report_status MCP tool. See
	// docs/designs/messaging-overhaul.md §4.2.3.
	LastReportType    string `json:"last_report_type,omitempty"` // back-compat: status, done, problem
	LastReportMessage string `json:"last_report_message,omitempty"`
	LastReportAt      string `json:"last_report_at,omitempty"`    // RFC3339
	LastReportState   string `json:"last_report_state,omitempty"` // working, blocked, complete, failure
	LastReportDetail  string `json:"last_report_detail,omitempty"`
}

// AgentsDir returns the path to the agents state directory under the given sprawl root.
func AgentsDir(sprawlRoot string) string {
	return filepath.Join(sprawlRoot, ".sprawl", "agents")
}

// migrate brings a freshly-unmarshaled AgentState forward to the current
// schema version (QUM-625 M4). It is idempotent: states already at (or beyond)
// CurrentSchemaVersion are left untouched.
//
// The v0 -> v1 migration splits the legacy combined Status axis. Pre-M4 code
// overloaded Status with outcome tokens ("done"/"problem") that are not
// livenesses. The migration:
//
//  1. Derives LastReportState from the legacy outcome token (only when
//     LastReportState is empty, so an explicit report value is never clobbered).
//  2. Rewrites Status to a pure liveness when it currently holds a non-liveness
//     value ("done"/"problem"/""): suspended if a session exists, else stopped.
//     Any genuine liveness value (active/running/suspended/... ) is preserved.
func migrate(a *AgentState) {
	if a.SchemaVersion >= CurrentSchemaVersion {
		return
	}

	// (a) Derive LastReportState from the legacy outcome token, only if empty.
	if a.LastReportState == "" {
		switch a.Status {
		case StatusDone:
			a.LastReportState = "complete"
		case "problem":
			a.LastReportState = "failure"
		}
	}

	// (b) Rewrite Status only if it is a non-liveness value.
	switch a.Status {
	case StatusDone, "problem", "":
		if a.SessionID != "" {
			a.Status = StatusSuspended
		} else {
			a.Status = StatusStopped
		}
	}

	// (c) Stamp the current schema version.
	a.SchemaVersion = CurrentSchemaVersion
}

// SaveAgent writes the agent state to a JSON file in the agents directory.
// The write is atomic: data is marshaled and written to a sibling .tmp file
// first, then renamed into place. On marshal failure no disk write occurs.
func SaveAgent(sprawlRoot string, agent *AgentState) error {
	// Stamp the schema version on fresh (never-versioned) states so they
	// persist as v1 (QUM-625 M4).
	if agent.SchemaVersion == 0 {
		agent.SchemaVersion = CurrentSchemaVersion
	}

	data, err := json.MarshalIndent(agent, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling agent state: %w", err)
	}

	dir := AgentsDir(sprawlRoot)
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // G301: world-readable agents dir is intentional
		return fmt.Errorf("creating agents directory: %w", err)
	}

	path := filepath.Join(dir, agent.Name+".json")
	// Best-effort clean any stale literal `<name>.json.tmp` file so it does
	// not leak into the agents directory (QUM-372).
	staleTmp := path + ".tmp"
	_ = os.Remove(staleTmp)
	tmp, err := os.CreateTemp(dir, agent.Name+".json.tmp-*")
	if err != nil {
		return fmt.Errorf("creating temp agent state: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("writing agent state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("closing agent state: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil { //nolint:gosec // G302: world-readable state file is intentional
		_ = os.Remove(tmpName)
		return fmt.Errorf("chmod agent state: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("renaming agent state: %w", err)
	}
	return nil
}

// LoadAgent reads the agent state from a JSON file.
func LoadAgent(sprawlRoot string, name string) (*AgentState, error) {
	path := filepath.Join(AgentsDir(sprawlRoot), name+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading agent state for %q: %w", name, err)
	}

	var agent AgentState
	if err := json.Unmarshal(data, &agent); err != nil {
		return nil, fmt.Errorf("parsing agent state for %q: %w", name, err)
	}
	// Migrate older (v0) files forward on read. The migrated value is not
	// written back here — the next SaveAgent persists it (QUM-625 M4).
	migrate(&agent)
	return &agent, nil
}

// ListAgents returns all agent states from the agents directory.
func ListAgents(sprawlRoot string) ([]*AgentState, error) {
	dir := AgentsDir(sprawlRoot)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing agents directory: %w", err)
	}

	var agents []*AgentState
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".json")
		agent, err := LoadAgent(sprawlRoot, name)
		if err != nil {
			return nil, err
		}
		agents = append(agents, agent)
	}
	return agents, nil
}

// DeleteAgent removes the agent state file and the agent's directory under
// .sprawl/agents/<name>/, freeing the name. Removing the directory prevents
// orphaned per-agent artifacts (SYSTEM.md, prompts, tasks, activity logs)
// from accumulating across spawn/retire cycles and from being silently
// inherited when a name is reused (QUM-404).
func DeleteAgent(sprawlRoot string, name string) error {
	path := filepath.Join(AgentsDir(sprawlRoot), name+".json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing agent state for %q: %w", name, err)
	}
	dirPath := filepath.Join(AgentsDir(sprawlRoot), name)
	if err := os.RemoveAll(dirPath); err != nil {
		return fmt.Errorf("removing agent directory for %q: %w", name, err)
	}
	return nil
}

// StateDir returns the path to the .sprawl/state directory under the given root.
func StateDir(sprawlRoot string) string {
	return filepath.Join(sprawlRoot, ".sprawl", "state")
}

// WriteAccentColor persists the accent color to .sprawl/state/accent-color.
func WriteAccentColor(sprawlRoot, color string) error {
	dir := StateDir(sprawlRoot)
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // G301: world-readable state dir is intentional
		return fmt.Errorf("creating state directory: %w", err)
	}
	path := filepath.Join(dir, "accent-color")
	return os.WriteFile(path, []byte(color), 0o644) //nolint:gosec // G306: world-readable state file is intentional
}

// ReadAccentColor reads the persisted accent color from .sprawl/state/accent-color.
// Returns empty string if the file doesn't exist.
func ReadAccentColor(sprawlRoot string) string {
	path := filepath.Join(StateDir(sprawlRoot), "accent-color")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// ReadNamespace reads the persisted namespace from .sprawl/namespace.
// Returns empty string if the file doesn't exist.
func ReadNamespace(sprawlRoot string) string {
	path := filepath.Join(sprawlRoot, ".sprawl", "namespace")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// ReadRootName reads the persisted root name from .sprawl/root-name.
// Returns empty string if the file doesn't exist.
func ReadRootName(sprawlRoot string) string {
	path := filepath.Join(sprawlRoot, ".sprawl", "root-name")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// WriteSystemPrompt writes the system prompt to .sprawl/agents/{agentName}/SYSTEM.md
// and returns the absolute path to the file.
func WriteSystemPrompt(sprawlRoot, agentName, content string) (string, error) {
	dir := filepath.Join(AgentsDir(sprawlRoot), agentName)
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // G301: world-readable agent dir is intentional
		return "", fmt.Errorf("creating agent directory: %w", err)
	}
	path := filepath.Join(dir, "SYSTEM.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil { //nolint:gosec // G306: world-readable prompt file is intentional
		return "", fmt.Errorf("writing system prompt: %w", err)
	}
	return path, nil
}
