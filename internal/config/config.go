package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config represents .sprawl/config.yaml project-level settings.
type Config struct {
	Validate   string `yaml:"validate"`
	sprawlRoot string
	values     map[string]string
}

// Load reads .sprawl/config.yaml from the given sprawl root directory.
// Returns a zero-value Config (no error) if the file does not exist.
func Load(sprawlRoot string) (*Config, error) {
	cfg := &Config{
		sprawlRoot: sprawlRoot,
		values:     make(map[string]string),
	}

	path := filepath.Join(sprawlRoot, ".sprawl", "config.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	// Unmarshal into the struct for typed fields
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	cfg.Validate = strings.TrimSpace(cfg.Validate)

	// Also unmarshal into the map for arbitrary key access
	if err := yaml.Unmarshal(data, &cfg.values); err != nil {
		// If map unmarshal fails, populate from struct fields
		cfg.values = make(map[string]string)
	}

	// Sync the trimmed validate value into the map
	if cfg.Validate != "" {
		cfg.values["validate"] = cfg.Validate
	} else {
		delete(cfg.values, "validate")
	}

	return cfg, nil
}

// Get returns the value for the given config key and whether it exists.
func (c *Config) Get(key string) (string, bool) {
	val, ok := c.values[key]
	return val, ok
}

// Set updates the value for the given config key.
func (c *Config) Set(key, value string) {
	if c.values == nil {
		c.values = make(map[string]string)
	}
	c.values[key] = value
	if key == "validate" {
		c.Validate = value
	}
}

// Keys returns all config keys in sorted order.
func (c *Config) Keys() []string {
	keys := make([]string, 0, len(c.values))
	for k := range c.values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Save writes the config back to .sprawl/config.yaml, creating the directory if needed.
func (c *Config) Save() error {
	dir := filepath.Join(c.sprawlRoot, ".sprawl")
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // G301: world-readable .sprawl dir is intentional
		return fmt.Errorf("creating .sprawl directory: %w", err)
	}

	data, err := yaml.Marshal(c.values)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // G306: config.yaml is checked into git, world-readable is intentional
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
