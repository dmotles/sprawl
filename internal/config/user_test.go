package config

import (
	"os"
	"path/filepath"
	"testing"
)

// stubUserConfigDir returns a func matching os.UserConfigDir's signature that
// always resolves to the given directory.
func stubUserConfigDir(dir string) func() (string, error) {
	return func() (string, error) { return dir, nil }
}

func TestLoadUserConfig_MissingFile_ReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadUserConfig(stubUserConfigDir(dir))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.HubURL != "" || cfg.HubToken != "" {
		t.Errorf("missing file should yield zero-value config, got %+v", cfg)
	}
}

func TestSaveUserConfig_RoundTrips(t *testing.T) {
	dir := t.TempDir()
	want := UserConfig{HubURL: "https://hub.example:443", HubToken: "sprawl_hub_abc_def"}
	if err := SaveUserConfig(stubUserConfigDir(dir), want); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}
	got, err := LoadUserConfig(stubUserConfigDir(dir))
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	if got != want {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, want)
	}
}

func TestSaveUserConfig_EnforcesTightModes(t *testing.T) {
	dir := t.TempDir()
	if err := SaveUserConfig(stubUserConfigDir(dir), UserConfig{HubToken: "sprawl_hub_x_y"}); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}

	// The sprawl/ directory must be 0700 (holds a credential file).
	sprawlDir := filepath.Join(dir, "sprawl")
	di, err := os.Stat(sprawlDir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Errorf("dir mode = %04o, want 0700", perm)
	}

	// The config file itself must be 0600 — it stores the hub token.
	fi, err := os.Stat(filepath.Join(sprawlDir, "config.yaml"))
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %04o, want 0600", perm)
	}
}

func TestLoadUserConfig_MalformedYAML_Errors(t *testing.T) {
	dir := t.TempDir()
	sprawlDir := filepath.Join(dir, "sprawl")
	if err := os.MkdirAll(sprawlDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sprawlDir, "config.yaml"), []byte("hub_url: [unterminated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadUserConfig(stubUserConfigDir(dir)); err == nil {
		t.Fatal("expected an error for malformed YAML")
	}
}

func TestLoadUserConfig_DirResolverError_Propagates(t *testing.T) {
	boom := func() (string, error) { return "", os.ErrPermission }
	if _, err := LoadUserConfig(boom); err == nil {
		t.Fatal("expected error when userConfigDir resolver fails")
	}
}

func TestSaveUserConfig_DirResolverError_Propagates(t *testing.T) {
	boom := func() (string, error) { return "", os.ErrPermission }
	if err := SaveUserConfig(boom, UserConfig{HubURL: "https://x"}); err == nil {
		t.Fatal("expected error when userConfigDir resolver fails")
	}
}

func TestSaveUserConfig_PreservesModeOnRewrite(t *testing.T) {
	dir := t.TempDir()
	if err := SaveUserConfig(stubUserConfigDir(dir), UserConfig{HubURL: "https://a"}); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if err := SaveUserConfig(stubUserConfigDir(dir), UserConfig{HubURL: "https://b"}); err != nil {
		t.Fatalf("second save: %v", err)
	}
	fi, err := os.Stat(filepath.Join(dir, "sprawl", "config.yaml"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode after rewrite = %04o, want 0600", perm)
	}
}
