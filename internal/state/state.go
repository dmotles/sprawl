package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AgentState holds the persistent metadata for a spawned agent.
type AgentState struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Family      string `json:"family"`
	Parent      string `json:"parent"`
	Prompt      string `json:"prompt"`
	Branch      string `json:"branch"`
	Worktree    string `json:"worktree"`
	TmuxSession string `json:"tmux_session"`
	TmuxWindow  string `json:"tmux_window"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"`
	SessionID   string `json:"session_id,omitempty"`
	Subagent    bool   `json:"subagent,omitempty"`
	TreePath    string `json:"tree_path,omitempty"`

	// Report fields — populated by "dendra report" subcommands.
	LastReportType    string `json:"last_report_type,omitempty"` // status, done, problem
	LastReportMessage string `json:"last_report_message,omitempty"`
	LastReportAt      string `json:"last_report_at,omitempty"` // RFC3339
}

// AgentsDir returns the path to the agents state directory under the given dendra root.
func AgentsDir(dendraRoot string) string {
	return filepath.Join(dendraRoot, ".sprawl", "agents")
}

// SaveAgent writes the agent state to a JSON file in the agents directory.
func SaveAgent(dendraRoot string, agent *AgentState) error {
	dir := AgentsDir(dendraRoot)
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // G301: world-readable agents dir is intentional
		return fmt.Errorf("creating agents directory: %w", err)
	}

	data, err := json.MarshalIndent(agent, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling agent state: %w", err)
	}

	path := filepath.Join(dir, agent.Name+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // G306: world-readable state file is intentional
		return fmt.Errorf("writing agent state: %w", err)
	}
	return nil
}

// LoadAgent reads the agent state from a JSON file.
func LoadAgent(dendraRoot string, name string) (*AgentState, error) {
	path := filepath.Join(AgentsDir(dendraRoot), name+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading agent state for %q: %w", name, err)
	}

	var agent AgentState
	if err := json.Unmarshal(data, &agent); err != nil {
		return nil, fmt.Errorf("parsing agent state for %q: %w", name, err)
	}
	return &agent, nil
}

// ListAgents returns all agent states from the agents directory.
func ListAgents(dendraRoot string) ([]*AgentState, error) {
	dir := AgentsDir(dendraRoot)
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
		agent, err := LoadAgent(dendraRoot, name)
		if err != nil {
			return nil, err
		}
		agents = append(agents, agent)
	}
	return agents, nil
}

// DeleteAgent removes the agent state file, freeing the name.
func DeleteAgent(dendraRoot string, name string) error {
	path := filepath.Join(AgentsDir(dendraRoot), name+".json")
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return nil // already gone
		}
		return fmt.Errorf("removing agent state for %q: %w", name, err)
	}
	return nil
}

// SprawlDir returns the path to the .sprawl directory under the given root.
func SprawlDir(dendraRoot string) string {
	return filepath.Join(dendraRoot, ".sprawl")
}

// WriteNamespace persists the selected namespace to .sprawl/namespace.
func WriteNamespace(dendraRoot, namespace string) error {
	dir := SprawlDir(dendraRoot)
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // G301: world-readable .sprawl dir is intentional
		return fmt.Errorf("creating .sprawl directory: %w", err)
	}
	path := filepath.Join(dir, "namespace")
	return os.WriteFile(path, []byte(namespace), 0o644) //nolint:gosec // G306: world-readable namespace file is intentional
}

// ReadNamespace reads the persisted namespace from .sprawl/namespace.
// Returns empty string if the file doesn't exist.
func ReadNamespace(dendraRoot string) string {
	path := filepath.Join(SprawlDir(dendraRoot), "namespace")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// WriteRootName persists the root agent name to .sprawl/root-name.
func WriteRootName(dendraRoot, rootName string) error {
	dir := SprawlDir(dendraRoot)
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // G301: world-readable .sprawl dir is intentional
		return fmt.Errorf("creating .sprawl directory: %w", err)
	}
	path := filepath.Join(dir, "root-name")
	return os.WriteFile(path, []byte(rootName), 0o644) //nolint:gosec // G306: world-readable root-name file is intentional
}

// ReadRootName reads the persisted root name from .sprawl/root-name.
// Returns empty string if the file doesn't exist.
func ReadRootName(dendraRoot string) string {
	path := filepath.Join(SprawlDir(dendraRoot), "root-name")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// WriteSystemPrompt writes the system prompt to .sprawl/agents/{agentName}/SYSTEM.md
// and returns the absolute path to the file.
func WriteSystemPrompt(dendraRoot, agentName, content string) (string, error) {
	dir := filepath.Join(AgentsDir(dendraRoot), agentName)
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // G301: world-readable agent dir is intentional
		return "", fmt.Errorf("creating agent directory: %w", err)
	}
	path := filepath.Join(dir, "SYSTEM.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil { //nolint:gosec // G306: world-readable prompt file is intentional
		return "", fmt.Errorf("writing system prompt: %w", err)
	}
	return path, nil
}

// UsedNames returns a set of agent names that have state files.
func UsedNames(dendraRoot string) (map[string]bool, error) {
	agents, err := ListAgents(dendraRoot)
	if err != nil {
		return nil, err
	}

	used := make(map[string]bool)
	for _, a := range agents {
		used[a.Name] = true
	}
	return used, nil
}
