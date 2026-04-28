package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

var codexSkillWhitelist = map[string]string{
	"handoff": "depends on sprawl-ops MCP tools that are not available in Codex sessions yet",
}

func TestClaudeSkillsHaveCodexCounterparts(t *testing.T) {
	repoRoot := repoRootFromTest(t)
	claudeSkills := listSkillNames(t, filepath.Join(repoRoot, ".claude", "skills"))
	agentsSkills := listSkillNames(t, filepath.Join(repoRoot, ".agents", "skills"))

	for name := range codexSkillWhitelist {
		if _, ok := claudeSkills[name]; !ok {
			t.Errorf("whitelisted skill %q is not present under .claude/skills", name)
		}
	}

	for name := range claudeSkills {
		if _, ok := codexSkillWhitelist[name]; ok {
			continue
		}
		if _, ok := agentsSkills[name]; !ok {
			t.Errorf("skill %q exists in .claude/skills but not .agents/skills; add .agents/skills/%s/SKILL.md or whitelist it in codexSkillWhitelist", name, name)
			continue
		}

		meta := readSkillMetadata(t, filepath.Join(repoRoot, ".agents", "skills", name, "SKILL.md"))
		if meta.name == "" {
			t.Errorf(".agents skill %q is missing frontmatter name", name)
		}
		if meta.description == "" {
			t.Errorf(".agents skill %q is missing frontmatter description", name)
		}
		if meta.name != "" && meta.name != name {
			t.Errorf(".agents skill %q declares name %q; want %q", name, meta.name, name)
		}
	}
}

type skillMetadata struct {
	name        string
	description string
}

func repoRootFromTest(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), ".."))
}

func listSkillNames(t *testing.T, base string) map[string]struct{} {
	t.Helper()

	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("reading %s: %v", base, err)
	}

	names := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		name := entry.Name()
		skillPath := filepath.Join(base, name, "SKILL.md")
		if _, err := os.Stat(skillPath); err != nil {
			t.Errorf("skill directory %s is missing SKILL.md: %v", filepath.Join(base, name), err)
			continue
		}
		names[name] = struct{}{}
	}
	return names
}

func readSkillMetadata(t *testing.T, path string) skillMetadata {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		t.Fatalf("%s is missing YAML frontmatter", path)
	}

	var meta skillMetadata
	for _, line := range lines[1:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			return meta
		}

		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(strings.Trim(value, `"'`))
		switch key {
		case "name":
			meta.name = value
		case "description":
			meta.description = value
		}
	}

	t.Fatalf("%s frontmatter is missing closing ---", path)
	return skillMetadata{}
}
