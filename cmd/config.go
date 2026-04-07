package cmd

import (
	"fmt"
	"io"
	"os"

	"github.com/dmotles/sprawl/internal/config"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

type configDeps struct {
	getenv func(string) string
	stdout io.Writer
	stderr io.Writer
}

func resolveConfigDeps() *configDeps {
	return &configDeps{
		getenv: os.Getenv,
		stdout: os.Stdout,
		stderr: os.Stderr,
	}
}

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage project configuration",
	Long: `Manage project-level configuration stored in .sprawl/config.yaml.

Known keys:
  validate    Shell command to run for post-merge validation (e.g. "make validate", "npm test")

Examples:
  sprawl config set validate "make test"
  sprawl config get validate
  sprawl config show`,
}

var configSetCmd = &cobra.Command{
	Use:   "set <key> <value>",
	Short: "Set a configuration value",
	Args:  cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		return runConfigSet(resolveConfigDeps(), args[0], args[1])
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

	cfg.Set(key, value)

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

	val, ok := cfg.Get(key)
	if ok {
		fmt.Fprintln(deps.stdout, val)
	}
	return nil
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
