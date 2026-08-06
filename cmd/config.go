package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/dmotles/sprawl/internal/config"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

type configDeps struct {
	getenv   func(string) string
	stdout   io.Writer
	stderr   io.Writer
	readFile func(string) ([]byte, error)
}

func resolveConfigDeps() *configDeps {
	return &configDeps{
		getenv:   os.Getenv,
		stdout:   os.Stdout,
		stderr:   os.Stderr,
		readFile: os.ReadFile,
	}
}

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage project configuration",
	// The key reference is GENERATED from the Config struct (QUM-1086), not
	// hand-maintained — the list that used to live here documented 3 of the 9
	// keys. Embedding config.Reference() verbatim means the same text a user sees
	// after breaking the file is reachable before they break it.
	Long: `Manage project-level configuration stored in .sprawl/config.yaml.

` + config.Reference() + `
Examples:
  sprawl config set validate "make test"
  sprawl config set worktree.setup "npm install"
  sprawl config get validate
  sprawl config show`,
}

var configSetFile string

var configSetCmd = &cobra.Command{
	Use:   "set <key> <value>",
	Short: "Set a configuration value",
	Args: func(_ *cobra.Command, args []string) error {
		if configSetFile != "" {
			if len(args) != 1 {
				return fmt.Errorf("with --file, exactly 1 argument (key) is required")
			}
			return nil
		}
		if len(args) != 2 {
			return fmt.Errorf("exactly 2 arguments (key and value) are required")
		}
		return nil
	},
	RunE: func(_ *cobra.Command, args []string) error {
		deps := resolveConfigDeps()
		key, value, err := resolveConfigSetValue(deps, args, configSetFile)
		if err != nil {
			return err
		}
		return runConfigSet(deps, key, value)
	},
}

var configGetCmd = &cobra.Command{
	Use:   "get <key>",
	Short: "Get a configuration value",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		return runConfigGet(resolveConfigDeps(), args[0])
	},
}

var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show all configuration",
	Args:  cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		return runConfigShow(resolveConfigDeps())
	},
}

func init() {
	configSetCmd.Flags().StringVar(&configSetFile, "file", "", "Read value from file instead of argument")
	configCmd.AddCommand(configSetCmd)
	configCmd.AddCommand(configGetCmd)
	configCmd.AddCommand(configShowCmd)
	rootCmd.AddCommand(configCmd)
}

func resolveSprawlRoot(deps *configDeps) (string, error) {
	root := deps.getenv("SPRAWL_ROOT")
	if root != "" {
		return root, nil
	}
	return "", fmt.Errorf("SPRAWL_ROOT environment variable is not set; run from within a sprawl project or set SPRAWL_ROOT")
}

func runConfigSet(deps *configDeps, key, value string) error {
	root, err := resolveSprawlRoot(deps)
	if err != nil {
		return err
	}

	cfg, err := config.Load(root)
	if err != nil {
		return err
	}

	if err := cfg.Set(key, value); err != nil {
		return err
	}

	if err := cfg.Save(); err != nil {
		return err
	}

	fmt.Fprintf(deps.stderr, "Set %s = %s in .sprawl/config.yaml\n", key, value)
	return nil
}

func runConfigGet(deps *configDeps, key string) error {
	root, err := resolveSprawlRoot(deps)
	if err != nil {
		return err
	}

	cfg, err := config.Load(root)
	if err != nil {
		return err
	}

	if !isKnownKey(key) {
		// Printing nothing and exiting 0 (the previous behaviour) reads as "the
		// key is set to empty".
		//
		// Built rather than hand-formatted so this path inherits the ordering the
		// error type documents as load-bearing — the offending key last, nearest
		// the prompt, or it scrolls off at 80x24 behind the reference table. It
		// also picks up the did-you-mean, which the hand-rolled message lacked.
		// FromSet because the wrong thing is the ARGUMENT, not the file.
		return &config.UnknownKeysError{FromSet: true, Problems: []config.KeyProblem{{Key: key}}}
	}
	val, _ := cfg.Get(key)
	fmt.Fprintln(deps.stdout, val)
	return nil
}

// isKnownKey reports whether key is a recognized config key.
func isKnownKey(key string) bool {
	for _, k := range config.KnownKeys() {
		if k == key {
			return true
		}
	}
	return false
}

// resolveConfigSetValue resolves the key and value for config set.
// If filePath is non-empty, the value is read from the file (trailing newline trimmed).
// Otherwise, key=args[0] and value=args[1].
func resolveConfigSetValue(deps *configDeps, args []string, filePath string) (string, string, error) {
	if filePath != "" {
		data, err := deps.readFile(filePath)
		if err != nil {
			return "", "", fmt.Errorf("reading file %s: %w", filePath, err)
		}
		value := strings.TrimRight(string(data), "\n")
		return args[0], value, nil
	}
	if len(args) < 2 {
		return "", "", fmt.Errorf("value argument is required when --file is not used")
	}
	return args[0], args[1], nil
}

func runConfigShow(deps *configDeps) error {
	root, err := resolveSprawlRoot(deps)
	if err != nil {
		return err
	}

	cfg, err := config.Load(root)
	if err != nil {
		return err
	}

	keys := cfg.Keys()
	if len(keys) == 0 {
		fmt.Fprintf(deps.stderr, "No configuration set in .sprawl/config.yaml\n")
		fmt.Fprintf(deps.stderr, "  Set a value with: sprawl config set <key> <value>\n")
		return nil
	}

	// Build a map for YAML output
	m := make(map[string]string, len(keys))
	for _, k := range keys {
		v, _ := cfg.Get(k)
		m[k] = v
	}

	fmt.Fprintf(deps.stdout, "# Config from .sprawl/config.yaml\n")
	data, err := yaml.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}
	fmt.Fprint(deps.stdout, string(data))
	return nil
}
