package state

import (
	"fmt"
	"os"
	"path/filepath"
)

// PromptsDir returns the path to the prompts directory for a given agent.
func PromptsDir(dendraRoot, agentName string) string {
	return filepath.Join(dendraRoot, ".dendra", "agents", agentName, "prompts")
}

// WritePromptFile writes a prompt to .dendra/agents/{agentName}/prompts/{id}.md
// and returns the absolute path to the file.
func WritePromptFile(dendraRoot, agentName, id, content string) (string, error) {
	dir := PromptsDir(dendraRoot, agentName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("creating prompts directory: %w", err)
	}
	path := filepath.Join(dir, id+".md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("writing prompt file: %w", err)
	}
	return path, nil
}
