package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config represents .sprawl/config.yaml project-level settings.
type Config struct {
	Validate string `yaml:"validate"`
}

// Load reads .sprawl/config.yaml from the given sprawl root directory.
// Returns a zero-value Config (no error) if the file does not exist.
func Load(sprawlRoot string) (*Config, error) {
	path := filepath.Join(sprawlRoot, ".sprawl", "config.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	cfg.Validate = strings.TrimSpace(cfg.Validate)
	return &cfg, nil
}
