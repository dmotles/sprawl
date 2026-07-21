package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config represents .sprawl/config.yaml project-level settings.
type Config struct {
	Validate                  string             `yaml:"validate"`
	ValidateTimeout           string             `yaml:"validate_timeout"`
	ValidatePopupAfterSeconds int                `yaml:"validate_popup_after_seconds"`
	Liveness                  *LivenessConfigRaw `yaml:"liveness"`
	// PauseTimeoutSeconds is the default escalation budget (in seconds) for
	// the `pause` MCP tool. QUM-722. Defaults to DefaultPauseTimeoutSeconds
	// when not present or non-positive.
	PauseTimeoutSeconds int `yaml:"pause_timeout_seconds"`
	// HubURL is the lowest-precedence source for the hub endpoint resolver
	// (flag > env > this). Default empty: there is NO baked-in hub endpoint
	// (public-repo hygiene). QUM-875.
	HubURL string `yaml:"hub_url"`
	// HubTokenFile is the lowest-precedence source for the host bearer token
	// (env SPRAWL_HUB_TOKEN wins). Path to a 0600 file holding the token; the
	// token is NEVER placed on a CLI flag or in a URL. QUM-877.
	HubTokenFile string `yaml:"hub_token_file"`
	sprawlRoot   string
	values       map[string]string
}

// DefaultPauseTimeoutSeconds is the fallback pause-escalation budget. QUM-722.
const DefaultPauseTimeoutSeconds = 30

// PauseTimeout returns the configured pause timeout, or the default when
// unset/non-positive.
func (c *Config) PauseTimeout() time.Duration {
	if c.PauseTimeoutSeconds <= 0 {
		return time.Duration(DefaultPauseTimeoutSeconds) * time.Second
	}
	return time.Duration(c.PauseTimeoutSeconds) * time.Second
}

// LivenessConfigRaw mirrors supervisor.LivenessConfigRaw so the YAML
// shape can be decoded inside the config package without an import cycle.
// The supervisor package re-uses this shape verbatim via ResolveLivenessConfig
// (see internal/supervisor/heartbeat.go). QUM-730.
type LivenessConfigRaw struct {
	// Enabled is *bool so an unset YAML value (partial `liveness:` block
	// with only some keys present) doesn't silently disable the
	// heartbeat. nil → use default (enabled); non-nil → take at face
	// value.
	Enabled               *bool  `yaml:"enabled"`
	HeartbeatInterval     string `yaml:"heartbeat_interval"`
	IdleThreshold         string `yaml:"idle_threshold"`
	Tier2ConsecutiveTicks int    `yaml:"tier2_consecutive_ticks"`
	EscalationThreshold   int    `yaml:"escalation_threshold"`
}

// DefaultValidatePopupAfterSeconds is the default threshold after which the
// TUI validate-output popup auto-opens for a running merge validate (QUM-588).
const DefaultValidatePopupAfterSeconds = 10

// ValidatePopupAfter returns the configured popup-open threshold or the
// default when unset (zero or negative).
func (c *Config) ValidatePopupAfter() time.Duration {
	if c.ValidatePopupAfterSeconds <= 0 {
		return time.Duration(DefaultValidatePopupAfterSeconds) * time.Second
	}
	return time.Duration(c.ValidatePopupAfterSeconds) * time.Second
}

// ValidateTimeoutDuration returns the parsed validate_timeout, or 0 if unset
// or unparseable. Callers should layer their own default on top. QUM-496.
func (c *Config) ValidateTimeoutDuration() time.Duration {
	v := strings.TrimSpace(c.ValidateTimeout)
	if v == "" {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0
	}
	return d
}

// Load reads .sprawl/config.yaml from the given sprawl root directory.
// Returns a zero-value Config (no error) if the file does not exist.
func Load(sprawlRoot string) (*Config, error) {
	cfg := &Config{
		sprawlRoot:          sprawlRoot,
		values:              make(map[string]string),
		PauseTimeoutSeconds: DefaultPauseTimeoutSeconds,
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

	cfg.ValidateTimeout = strings.TrimSpace(cfg.ValidateTimeout)
	if cfg.ValidateTimeout != "" {
		cfg.values["validate_timeout"] = cfg.ValidateTimeout
	} else {
		delete(cfg.values, "validate_timeout")
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
	switch key {
	case "validate":
		c.Validate = value
	case "validate_timeout":
		c.ValidateTimeout = value
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
