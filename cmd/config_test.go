package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/config"
)

func newTestConfigDeps(t *testing.T) (*configDeps, string) {
	t.Helper()
	tmpDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	deps := &configDeps{
		getenv: func(key string) string {
			if key == "SPRAWL_ROOT" {
				return tmpDir
			}
			return ""
		},
		stdout: &stdout,
		stderr: &stderr,
	}
	return deps, tmpDir
}

func TestConfigSet_WritesValue(t *testing.T) {
	deps, tmpDir := newTestConfigDeps(t)

	// Create .sprawl/ directory
	os.MkdirAll(filepath.Join(tmpDir, ".sprawl"), 0o755)

	err := runConfigSet(deps, "validate", "make test")
	if err != nil {
		t.Fatalf("runConfigSet error: %v", err)
	}

	// Verify by loading config
	cfg, err := config.Load(tmpDir)
	if err != nil {
		t.Fatalf("config.Load error: %v", err)
	}

	val, ok := cfg.Get("validate")
	if !ok {
		t.Error("expected key 'validate' to exist after Set")
	}
	if val != "make test" {
		t.Errorf("Get(\"validate\") = %q, want %q", val, "make test")
	}
}

func TestConfigSet_CreatesSprawlDir(t *testing.T) {
	deps, tmpDir := newTestConfigDeps(t)

	// Do NOT create .sprawl/ dir — runConfigSet should handle it
	err := runConfigSet(deps, "foo", "bar")
	if err != nil {
		t.Fatalf("runConfigSet error: %v", err)
	}

	configPath := filepath.Join(tmpDir, ".sprawl", "config.yaml")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Error("config file should exist after runConfigSet, even without pre-existing .sprawl/ dir")
	}
}

func TestConfigGet_ExistingKey(t *testing.T) {
	deps, tmpDir := newTestConfigDeps(t)

	// Pre-write config file
	configDir := filepath.Join(tmpDir, ".sprawl")
	os.MkdirAll(configDir, 0o755)
	content := "validate: \"make test\"\n"
	os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(content), 0o644)

	err := runConfigGet(deps, "validate")
	if err != nil {
		t.Fatalf("runConfigGet error: %v", err)
	}

	stdout := deps.stdout.(*bytes.Buffer).String()
	if !strings.Contains(stdout, "make test") {
		t.Errorf("stdout should contain value 'make test', got: %q", stdout)
	}
}

func TestConfigGet_MissingKey(t *testing.T) {
	deps, tmpDir := newTestConfigDeps(t)

	// Create empty config
	configDir := filepath.Join(tmpDir, ".sprawl")
	os.MkdirAll(configDir, 0o755)
	os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(""), 0o644)

	err := runConfigGet(deps, "nonexistent")
	if err != nil {
		t.Fatalf("runConfigGet error: %v", err)
	}

	stdout := deps.stdout.(*bytes.Buffer).String()
	if stdout != "" {
		t.Errorf("stdout should be empty for missing key, got: %q", stdout)
	}
}

func TestConfigShow_DumpsConfig(t *testing.T) {
	deps, tmpDir := newTestConfigDeps(t)

	// Pre-write config file with multiple keys
	configDir := filepath.Join(tmpDir, ".sprawl")
	os.MkdirAll(configDir, 0o755)
	content := "validate: \"make test\"\n"
	os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(content), 0o644)

	err := runConfigShow(deps)
	if err != nil {
		t.Fatalf("runConfigShow error: %v", err)
	}

	stdout := deps.stdout.(*bytes.Buffer).String()
	if !strings.Contains(stdout, "validate") {
		t.Errorf("stdout should contain key 'validate', got: %q", stdout)
	}
	if !strings.Contains(stdout, "make test") {
		t.Errorf("stdout should contain value 'make test', got: %q", stdout)
	}
}

func TestConfigShow_NoConfigFile(t *testing.T) {
	deps, _ := newTestConfigDeps(t)

	// No .sprawl/ dir, no config file
	err := runConfigShow(deps)
	if err != nil {
		t.Fatalf("runConfigShow error: %v", err)
	}

	stdout := deps.stdout.(*bytes.Buffer).String()
	stderr := deps.stderr.(*bytes.Buffer).String()
	combined := stdout + stderr
	// Should show empty or no-config message (not crash)
	if strings.Contains(combined, "panic") {
		t.Error("should not panic when no config file exists")
	}
}

func TestConfigSet_MissingSprawlRoot(t *testing.T) {
	var stdout, stderr bytes.Buffer
	deps := &configDeps{
		getenv: func(string) string { return "" },
		stdout: &stdout,
		stderr: &stderr,
	}

	err := runConfigSet(deps, "foo", "bar")
	if err == nil {
		t.Fatal("expected error when SPRAWL_ROOT is not set")
	}
	if !strings.Contains(err.Error(), "SPRAWL_ROOT") {
		t.Errorf("error should mention SPRAWL_ROOT, got: %v", err)
	}
}
