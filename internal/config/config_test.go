package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestLoad_HubTokenFile(t *testing.T) {
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, ".sprawl")
	os.MkdirAll(configDir, 0o755)

	content := "hub_url: \"http://localhost:8080\"\nhub_token_file: \".sprawl/secrets/hub-token\"\n"
	os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(content), 0o644)

	cfg, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.HubTokenFile != ".sprawl/secrets/hub-token" {
		t.Errorf("HubTokenFile = %q, want %q", cfg.HubTokenFile, ".sprawl/secrets/hub-token")
	}
	if cfg.HubURL != "http://localhost:8080" {
		t.Errorf("HubURL = %q, want %q", cfg.HubURL, "http://localhost:8080")
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

// QUM-1086 CONTRACT CHANGE: Set no longer accepts an arbitrary key. It used to
// persist anything, which is how a misspelled `worktree_setup` could leave an
// operator believing the commit guards were installed. It now returns an error
// and stores nothing.
func TestSet_UnknownKeyIsRejected(t *testing.T) {
	tmpDir := t.TempDir()

	cfg, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := cfg.Set("foo", "bar"); err == nil {
		t.Fatal("Set on an unrecognized key must return an error")
	}

	val, ok := cfg.Get("foo")
	if ok || val != "" {
		t.Errorf("Get(\"foo\") = (%q, %v) after a rejected Set, want (\"\", false)", val, ok)
	}
}

func TestSet_ValidateKey(t *testing.T) {
	tmpDir := t.TempDir()

	cfg, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := cfg.Set("validate", "npm test"); err != nil {
		t.Fatalf("Set: %v", err)
	}

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

	// A recognized key: `foo` is no longer accepted (see
	// TestSet_UnknownKeyIsRejected).
	for _, v := range []string{"first", "second"} {
		if err := cfg.Set("memory_model", v); err != nil {
			t.Fatalf("Set(memory_model, %q): %v", v, err)
		}
	}

	val, ok := cfg.Get("memory_model")
	if !ok {
		t.Error("Get(\"memory_model\") should return ok=true")
	}
	if val != "second" {
		t.Errorf("Get(\"memory_model\") = %q, want %q (second Set should win)", val, "second")
	}
}

func TestSave_CreatesDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	// Do NOT create .sprawl/ dir — Save should create it

	cfg, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := cfg.Set("validate", "bar"); err != nil {
		t.Fatalf("Set: %v", err)
	}

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

	// `custom-key` is gone: QUM-1086 removed arbitrary keys. worktree.setup is
	// the key that actually matters on this path — it was map-only before, and
	// Save marshalling only the map is what made `config set` a data-loss path.
	for _, kv := range [][2]string{{"validate", "make test"}, {"worktree.setup", "echo hi"}} {
		if err := cfg.Set(kv[0], kv[1]); err != nil {
			t.Fatalf("Set(%q): %v", kv[0], err)
		}
	}

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

	val2, ok2 := cfg2.Get("worktree.setup")
	if !ok2 || val2 != "echo hi" {
		t.Errorf("round-trip Get(\"worktree.setup\") = (%q, %v), want (\"echo hi\", true)", val2, ok2)
	}
}

// QUM-722: the pause MCP tool gets a 30s default escalation budget.
//
// QUM-1086 CONTRACT CHANGE: Load no longer PREFILLS that default into the
// struct field. Defaults now live only in the accessor, because Save marshals
// the struct — a prefilled field would be written back to the user's file on the
// next `sprawl config set`, freezing today's default as if they had chosen it.
// The observable contract is therefore: field zero, accessor 30s.
func TestConfig_PauseTimeoutDefaultLivesInTheAccessor(t *testing.T) {
	tmpDir := t.TempDir()
	cfg, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PauseTimeoutSeconds != 0 {
		t.Errorf("PauseTimeoutSeconds = %d, want 0: Load must not prefill defaults", cfg.PauseTimeoutSeconds)
	}
	if got := cfg.PauseTimeout(); got != 30*time.Second {
		t.Errorf("PauseTimeout() = %v, want 30s (the accessor applies the default)", got)
	}
}

func TestConfig_PauseTimeoutSecondsOverride(t *testing.T) {
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, ".sprawl")
	os.MkdirAll(configDir, 0o755)
	content := "pause_timeout_seconds: 5\n"
	os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(content), 0o644)

	cfg, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PauseTimeoutSeconds != 5 {
		t.Errorf("PauseTimeoutSeconds = %d, want 5 (yaml override)", cfg.PauseTimeoutSeconds)
	}
}

// QUM-1086 CONTRACT CHANGE: arbitrary keys (zebra/apple/mango) are gone, so
// this now exercises sorting over real keys. Keys() reports only keys that are
// SET; the full list is KnownKeys().
func TestKeys_ReturnsSorted(t *testing.T) {
	tmpDir := t.TempDir()

	cfg, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Set in deliberately non-sorted order.
	for _, kv := range [][2]string{{"worktree.setup", "w"}, {"hub_url", "h"}, {"memory_model", "m"}} {
		if err := cfg.Set(kv[0], kv[1]); err != nil {
			t.Fatalf("Set(%q): %v", kv[0], err)
		}
	}

	keys := cfg.Keys()
	want := []string{"hub_url", "memory_model", "worktree.setup"}
	if len(keys) != len(want) {
		t.Fatalf("Keys() = %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Errorf("keys[%d] = %q, want %q", i, keys[i], want[i])
		}
	}
}
