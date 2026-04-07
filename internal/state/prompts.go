package state

import (
	"fmt"
	"os"
	"path/filepath"
)

// PromptsDir returns the path to the prompts directory for a given agent.
func PromptsDir(sprawlRoot, agentName string) string {
	return filepath.Join(sprawlRoot, ".sprawl", "agents", agentName, "prompts")
}

// WritePromptFile writes a prompt to .sprawl/agents/{agentName}/prompts/{id}.md
// and returns the absolute path to the file.
func WritePromptFile(sprawlRoot, agentName, id, content string) (string, error) {
	dir := PromptsDir(sprawlRoot, agentName)
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // G301: world-readable prompts dir is intentional
		return "", fmt.Errorf("creating prompts directory: %w", err)
	}
	path := filepath.Join(dir, id+".md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil { //nolint:gosec // G306: world-readable prompt file is intentional
		return "", fmt.Errorf("writing prompt file: %w", err)
	}
	return path, nil
}
