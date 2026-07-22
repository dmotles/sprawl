package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// UserConfig holds user-level (per-machine) client settings for talking to a
// sprawl hub. It lives at <os.UserConfigDir>/sprawl/config.yaml — distinct from
// the per-repo project config (.sprawl/config.yaml). Because HubToken is a
// credential, the file is written 0600 in a 0700 directory. QUM-886.
type UserConfig struct {
	// HubURL is the hub endpoint. Sits between env and project config in the
	// hub-URL precedence chain (flag > env > user > project).
	HubURL string `yaml:"hub_url"`
	// HubToken is the host bearer token VALUE (not a file path — the project
	// config's hub_token_file holds a path instead). Never logged.
	HubToken string `yaml:"hub_token"`
}

// userConfigPath returns <userConfigDir()>/sprawl/config.yaml. The
// userConfigDir func is injected (os.UserConfigDir in production) for testing.
func userConfigPath(userConfigDir func() (string, error)) (string, error) {
	dir, err := userConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolving user config dir: %w", err)
	}
	return filepath.Join(dir, "sprawl", "config.yaml"), nil
}

// LoadUserConfig reads the user-level config. A missing file yields a
// zero-value UserConfig and no error (nothing configured yet).
func LoadUserConfig(userConfigDir func() (string, error)) (UserConfig, error) {
	var cfg UserConfig
	path, err := userConfigPath(userConfigDir)
	if err != nil {
		return cfg, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("reading %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parsing %s: %w", path, err)
	}
	return cfg, nil
}

// SaveUserConfig writes the user-level config, creating the directory 0700 and
// the file 0600 (it holds a credential). The write is atomic: the payload is
// written to a temp file (created 0600 by os.CreateTemp) in the same directory
// and renamed over the target, so a concurrent reader never sees a partial
// write and the secret never lands in a loose-mode file — not even briefly on
// a rewrite of a pre-existing 0644 file.
func SaveUserConfig(userConfigDir func() (string, error), cfg UserConfig) error {
	path, err := userConfigPath(userConfigDir)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshaling user config: %w", err)
	}

	tmp, err := os.CreateTemp(dir, "config-*.yaml.tmp") // created 0600
	if err != nil {
		return fmt.Errorf("creating temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if we bail before the rename succeeds.
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("renaming %s to %s: %w", tmpName, path, err)
	}
	return nil
}
