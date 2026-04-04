package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PersistentKnowledgeConfig controls the persistent knowledge update behavior.
type PersistentKnowledgeConfig struct {
	MaxItems     int
	MaxSizeChars int
}

// DefaultPersistentKnowledgeConfig returns sensible defaults.
func DefaultPersistentKnowledgeConfig() PersistentKnowledgeConfig {
	return PersistentKnowledgeConfig{
		MaxItems:     20,
		MaxSizeChars: 10000,
	}
}

func persistentKnowledgePath(dendraRoot string) string {
	return filepath.Join(memoryDir(dendraRoot), "persistent.md")
}

// ReadPersistentKnowledge reads .dendra/memory/persistent.md and returns its
// contents. Returns an empty string (not error) if the file doesn't exist.
func ReadPersistentKnowledge(dendraRoot string) (string, error) {
	data, err := os.ReadFile(persistentKnowledgePath(dendraRoot))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("reading persistent knowledge: %w", err)
	}
	return string(data), nil
}

// writePersistentKnowledge writes items as a markdown bullet list to persistent.md.
func writePersistentKnowledge(dendraRoot string, items []string) error {
	dir := filepath.Dir(persistentKnowledgePath(dendraRoot))
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // G301: world-readable memory dir is intentional
		return fmt.Errorf("creating memory directory: %w", err)
	}

	var b strings.Builder
	for _, item := range items {
		fmt.Fprintf(&b, "- %s\n", item)
	}

	if err := os.WriteFile(persistentKnowledgePath(dendraRoot), []byte(b.String()), 0o644); err != nil { //nolint:gosec // G306: world-readable knowledge file is intentional
		return fmt.Errorf("writing persistent knowledge: %w", err)
	}
	return nil
}

// buildPersistentPrompt constructs the prompt sent to Claude for knowledge
// extraction and curation.
func buildPersistentPrompt(existingKnowledge, sessionSummary, timelineBullets string, maxItems int) string {
	var b strings.Builder

	b.WriteString("You are a knowledge curator for the Dendra AI orchestration system.\n\n")
	b.WriteString("Your job is to maintain a concise list of persistent knowledge — important facts, preferences, conventions, and patterns that should be remembered across sessions.\n\n")

	b.WriteString("## Current Persistent Knowledge\n\n")
	if strings.TrimSpace(existingKnowledge) == "" {
		b.WriteString("No existing persistent knowledge.\n\n")
	} else {
		b.WriteString(existingKnowledge)
		b.WriteString("\n\n")
	}

	b.WriteString("## Latest Session Summary\n\n")
	b.WriteString(sessionSummary)
	b.WriteString("\n\n")

	if strings.TrimSpace(timelineBullets) != "" {
		b.WriteString("## Recent Timeline\n\n")
		b.WriteString(timelineBullets)
		b.WriteString("\n\n")
	}

	b.WriteString("## Instructions\n\n")
	b.WriteString("Review the latest session and extract important persistent facts: user preferences, project conventions, known issues, behavioral patterns, recurring pain points.\n\n")
	b.WriteString("Merge new facts into the existing persistent knowledge list.\n")
	b.WriteString("Drop items that haven't come up recently or add little value — the list should stay fresh and relevant.\n")
	fmt.Fprintf(&b, "Cap at %d items maximum.\n", maxItems)
	b.WriteString("Each item should be a concise, actionable statement (1-2 sentences max).\n\n")
	b.WriteString("Each item MUST be on its own line in this exact format:\n\n")
	b.WriteString("- {knowledge item}\n\n")
	b.WriteString("Output ONLY the bullet lines. No headers, no explanation, no other text.\n")

	return b.String()
}

// parsePersistentOutput parses Claude's raw output into individual knowledge items.
// Lines matching "- {text}" are parsed; others are silently skipped.
func parsePersistentOutput(raw string) []string {
	var items []string
	for line := range strings.SplitSeq(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		items = append(items, line[2:])
	}
	return items
}

// UpdatePersistentKnowledge reads the current persistent knowledge, sends a
// prompt to Claude to extract and merge new knowledge from the latest session,
// and writes the updated knowledge back. It follows the same dependency
// injection pattern as Consolidate().
func UpdatePersistentKnowledge(ctx context.Context, dendraRoot string, invoker ClaudeInvoker, cfg *PersistentKnowledgeConfig, sessionSummary string, timelineBullets string) error {
	if cfg == nil {
		c := DefaultPersistentKnowledgeConfig()
		cfg = &c
	}

	existing, err := ReadPersistentKnowledge(dendraRoot)
	if err != nil {
		return fmt.Errorf("reading existing knowledge: %w", err)
	}

	prompt := buildPersistentPrompt(existing, sessionSummary, timelineBullets, cfg.MaxItems)

	output, err := invoker.Invoke(ctx, prompt)
	if err != nil {
		return fmt.Errorf("invoking claude for persistent knowledge: %w", err)
	}

	items := parsePersistentOutput(output)

	// Safety: don't overwrite existing knowledge with empty result.
	if len(items) == 0 {
		return nil
	}

	// Enforce item cap deterministically.
	if len(items) > cfg.MaxItems {
		items = items[:cfg.MaxItems]
	}

	if err := writePersistentKnowledge(dendraRoot, items); err != nil {
		return fmt.Errorf("writing persistent knowledge: %w", err)
	}

	return nil
}
