package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_FileNotExist_ReturnsEmptyConfig(t *testing.T) {
	tmpDir := t.TempDir()

	cfg, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("config should not be nil")
	}
	if cfg.Validate != "" {
		t.Errorf("Validate should be empty when no config file, got %q", cfg.Validate)
	}
}

func TestLoad_ValidConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, ".sprawl")
	os.MkdirAll(configDir, 0o755)

	content := "validate: \"make test\"\n"
	os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(content), 0o644)

	cfg, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Validate != "make test" {
		t.Errorf("Validate = %q, want %q", cfg.Validate, "make test")
	}
}

func TestLoad_EmptyValidate(t *testing.T) {
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, ".sprawl")
	os.MkdirAll(configDir, 0o755)

	content := "validate: \"\"\n"
	os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(content), 0o644)

	cfg, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Validate != "" {
		t.Errorf("Validate should be empty, got %q", cfg.Validate)
	}
}

func TestLoad_NoValidateKey(t *testing.T) {
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, ".sprawl")
	os.MkdirAll(configDir, 0o755)

	content := "# empty config\n"
	os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(content), 0o644)

	cfg, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Validate != "" {
		t.Errorf("Validate should be empty when key absent, got %q", cfg.Validate)
	}
}

func TestLoad_MalformedYAML(t *testing.T) {
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, ".sprawl")
	os.MkdirAll(configDir, 0o755)

	content := "validate: [invalid yaml\n"
	os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(content), 0o644)

	_, err := Load(tmpDir)
	if err == nil {
		t.Fatal("expected error for malformed YAML")
	}
}

func TestLoad_UnquotedCommand(t *testing.T) {
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, ".sprawl")
	os.MkdirAll(configDir, 0o755)

	content := "validate: make validate\n"
	os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(content), 0o644)

	cfg, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Validate != "make validate" {
		t.Errorf("Validate = %q, want %q", cfg.Validate, "make validate")
	}
}
