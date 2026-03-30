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
}

// AgentsDir returns the path to the agents state directory under the given dendra root.
func AgentsDir(dendraRoot string) string {
	return filepath.Join(dendraRoot, ".dendra", "agents")
}

// SaveAgent writes the agent state to a JSON file in the agents directory.
func SaveAgent(dendraRoot string, agent *AgentState) error {
	dir := AgentsDir(dendraRoot)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating agents directory: %w", err)
	}

	data, err := json.MarshalIndent(agent, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling agent state: %w", err)
	}

	path := filepath.Join(dir, agent.Name+".json")
	if err := os.WriteFile(path, data, 0644); err != nil {
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
