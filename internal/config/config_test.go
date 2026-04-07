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

func TestGet_ExistingKey(t *testing.T) {
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, ".sprawl")
	os.MkdirAll(configDir, 0o755)

	content := "validate: \"make test\"\n"
	os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(content), 0o644)

	cfg, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	val, ok := cfg.Get("validate")
	if !ok {
		t.Error("Get(\"validate\") should return ok=true for existing key")
	}
	if val != "make test" {
		t.Errorf("Get(\"validate\") = %q, want %q", val, "make test")
	}
}

func TestGet_MissingKey(t *testing.T) {
	tmpDir := t.TempDir()

	cfg, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	val, ok := cfg.Get("nonexistent")
	if ok {
		t.Error("Get(\"nonexistent\") should return ok=false for missing key")
	}
	if val != "" {
		t.Errorf("Get(\"nonexistent\") = %q, want empty string", val)
	}
}

func TestSet_NewKey(t *testing.T) {
	tmpDir := t.TempDir()

	cfg, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg.Set("foo", "bar")

	val, ok := cfg.Get("foo")
	if !ok {
		t.Error("Get(\"foo\") should return ok=true after Set")
	}
	if val != "bar" {
		t.Errorf("Get(\"foo\") = %q, want %q", val, "bar")
	}
}

func TestSet_ValidateKey(t *testing.T) {
	tmpDir := t.TempDir()

	cfg, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg.Set("validate", "npm test")

	val, ok := cfg.Get("validate")
	if !ok {
		t.Error("Get(\"validate\") should return ok=true after Set")
	}
	if val != "npm test" {
		t.Errorf("Get(\"validate\") = %q, want %q", val, "npm test")
	}
	if cfg.Validate != "npm test" {
		t.Errorf("cfg.Validate = %q, want %q", cfg.Validate, "npm test")
	}
}

func TestSet_OverwriteKey(t *testing.T) {
	tmpDir := t.TempDir()

	cfg, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg.Set("foo", "first")
	cfg.Set("foo", "second")

	val, ok := cfg.Get("foo")
	if !ok {
		t.Error("Get(\"foo\") should return ok=true")
	}
	if val != "second" {
		t.Errorf("Get(\"foo\") = %q, want %q (second Set should win)", val, "second")
	}
}

func TestSave_CreatesDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	// Do NOT create .sprawl/ dir — Save should create it

	cfg, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg.Set("foo", "bar")

	if err := cfg.Save(); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	configPath := filepath.Join(tmpDir, ".sprawl", "config.yaml")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Errorf("config file should exist after Save, got not-exist")
	}
}

func TestSave_RoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, ".sprawl")
	os.MkdirAll(configDir, 0o755)

	// Load, set keys, save
	cfg, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error loading: %v", err)
	}

	cfg.Set("validate", "make test")
	cfg.Set("custom-key", "custom-value")

	if err := cfg.Save(); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	// Load again and verify
	cfg2, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error re-loading: %v", err)
	}

	val, ok := cfg2.Get("validate")
	if !ok || val != "make test" {
		t.Errorf("round-trip Get(\"validate\") = (%q, %v), want (\"make test\", true)", val, ok)
	}

	val2, ok2 := cfg2.Get("custom-key")
	if !ok2 || val2 != "custom-value" {
		t.Errorf("round-trip Get(\"custom-key\") = (%q, %v), want (\"custom-value\", true)", val2, ok2)
	}
}

func TestKeys_ReturnsSorted(t *testing.T) {
	tmpDir := t.TempDir()

	cfg, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg.Set("zebra", "z")
	cfg.Set("apple", "a")
	cfg.Set("mango", "m")

	keys := cfg.Keys()
	if len(keys) != 3 {
		t.Fatalf("Keys() returned %d keys, want 3", len(keys))
	}
	if keys[0] != "apple" {
		t.Errorf("keys[0] = %q, want \"apple\"", keys[0])
	}
	if keys[1] != "mango" {
		t.Errorf("keys[1] = %q, want \"mango\"", keys[1])
	}
	if keys[2] != "zebra" {
		t.Errorf("keys[2] = %q, want \"zebra\"", keys[2])
	}
}
