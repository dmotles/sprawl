package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPromptsDir(t *testing.T) {
	got := PromptsDir("/home/user/project", "frank")
	want := "/home/user/project/.sprawl/agents/frank/prompts"
	if got != want {
		t.Errorf("PromptsDir = %q, want %q", got, want)
	}
}

func TestWritePromptFile_CreatesFileWithContent(t *testing.T) {
	dir := t.TempDir()
	content := "Work on QUM-43. Read the issue for full details."

	path, err := WritePromptFile(dir, "finn", "initial", content)
	if err != nil {
		t.Fatalf("WritePromptFile: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	if string(data) != content {
		t.Errorf("content = %q, want %q", string(data), content)
	}
}

func TestWritePromptFile_CreatesPromptsDirectory(t *testing.T) {
	dir := t.TempDir()

	promptsPath := PromptsDir(dir, "newagent")
	if _, err := os.Stat(promptsPath); !os.IsNotExist(err) {
		t.Fatal("prompts dir should not exist before WritePromptFile")
	}

	path, err := WritePromptFile(dir, "newagent", "initial", "do something")
	if err != nil {
		t.Fatalf("WritePromptFile: %v", err)
	}

	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat prompts dir: %v", err)
	}
	if !info.IsDir() {
		t.Error("expected prompts directory to be created")
	}
}

func TestWritePromptFile_ReturnsAbsolutePath(t *testing.T) {
	dir := t.TempDir()

	path, err := WritePromptFile(dir, "frank", "test-uuid-123", "task content")
	if err != nil {
		t.Fatalf("WritePromptFile: %v", err)
	}

	expected := filepath.Join(dir, ".sprawl", "agents", "frank", "prompts", "test-uuid-123.md")
	if path != expected {
		t.Errorf("path = %q, want %q", path, expected)
	}
}

func TestWritePromptFile_OverwritesExisting(t *testing.T) {
	dir := t.TempDir()

	_, err := WritePromptFile(dir, "finn", "initial", "first content")
	if err != nil {
		t.Fatalf("first WritePromptFile: %v", err)
	}

	path, err := WritePromptFile(dir, "finn", "initial", "updated content")
	if err != nil {
		t.Fatalf("second WritePromptFile: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	if string(data) != "updated content" {
		t.Errorf("content = %q, want %q", string(data), "updated content")
	}
}
