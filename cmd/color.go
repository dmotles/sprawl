package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/tmux"
	"github.com/spf13/cobra"
)

type colorDeps struct {
	getenv     func(string) string
	stdout     io.Writer
	stderr     io.Writer
	tmuxRunner tmux.Runner
}

var defaultColorDeps *colorDeps

func resolveColorDeps() (*colorDeps, error) {
	if defaultColorDeps != nil {
		return defaultColorDeps, nil
	}
	tmuxPath, err := tmux.FindTmux()
	if err != nil {
		return nil, fmt.Errorf("tmux is required but not found")
	}
	return &colorDeps{
		getenv:     os.Getenv,
		stdout:     os.Stdout,
		stderr:     os.Stderr,
		tmuxRunner: &tmux.RealRunner{TmuxPath: tmuxPath},
	}, nil
}

var colorCmd = &cobra.Command{
	Use:   "color",
	Short: "Manage the tmux accent color",
	Long: `View or change the tmux accent color used for the Sprawl status bar.

Without subcommands, shows the current accent color.

Examples:
  sprawl color              Show current color
  sprawl color list         List all available colors
  sprawl color rotate       Pick a new random color
  sprawl color set cyan     Set a specific color by name or alias`,
	Args: cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		deps, err := resolveColorDeps()
		if err != nil {
			return err
		}
		return runColorShow(deps)
	},
}

var colorListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all available accent colors",
	Args:  cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		deps, err := resolveColorDeps()
		if err != nil {
			return err
		}
		return runColorList(deps)
	},
}

var colorRotateCmd = &cobra.Command{
	Use:   "rotate",
	Short: "Pick a new random accent color",
	Args:  cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		deps, err := resolveColorDeps()
		if err != nil {
			return err
		}
		return runColorRotate(deps)
	},
}

var colorSetCmd = &cobra.Command{
	Use:   "set <color>",
	Short: "Set a specific accent color by name or alias",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		deps, err := resolveColorDeps()
		if err != nil {
			return err
		}
		return runColorSet(deps, args[0])
	},
}

func init() {
	colorCmd.AddCommand(colorListCmd)
	colorCmd.AddCommand(colorRotateCmd)
	colorCmd.AddCommand(colorSetCmd)
	rootCmd.AddCommand(colorCmd)
}

func resolveColorRoot(deps *colorDeps) (string, error) {
	root := deps.getenv("SPRAWL_ROOT")
	if root != "" {
		return root, nil
	}
	return "", fmt.Errorf("SPRAWL_ROOT environment variable is not set; run from within a sprawl project or set SPRAWL_ROOT")
}

func runColorShow(deps *colorDeps) error {
	deprecationWarningCustom("color", "this CLI form will be removed in a future release.")
	root, err := resolveColorRoot(deps)
	if err != nil {
		return err
	}

	color := state.ReadAccentColor(root)
	if color == "" {
		return fmt.Errorf("no accent color set; run sprawl enter first")
	}

	c, ok := tmux.FindColor(color)
	if ok {
		fmt.Fprintf(deps.stdout, "%s (%s)\n", c.Name, strings.Join(c.Aliases, ", "))
	} else {
		fmt.Fprintln(deps.stdout, color)
	}
	return nil
}

func runColorList(deps *colorDeps) error {
	deprecationWarningCustom("color", "this CLI form will be removed in a future release.")
	root, err := resolveColorRoot(deps)
	if err != nil {
		return err
	}

	current := state.ReadAccentColor(root)

	for _, c := range tmux.AccentColors {
		marker := "  "
		if c.Name == current {
			marker = "* "
		}
		fmt.Fprintf(deps.stdout, "%s%-11s %s\n", marker, c.Name, strings.Join(c.Aliases, ", "))
	}
	return nil
}

func runColorRotate(deps *colorDeps) error {
	deprecationWarningCustom("color", "this CLI form will be removed in a future release.")
	root, err := resolveColorRoot(deps)
	if err != nil {
		return err
	}

	current := state.ReadAccentColor(root)
	newColor := tmux.PickAccentColorExcluding(current)

	if err := applyColor(deps, root, newColor); err != nil {
		return err
	}

	fmt.Fprintf(deps.stdout, "Accent color changed: %s -> %s\n", current, newColor)
	return nil
}

func runColorSet(deps *colorDeps, nameOrAlias string) error {
	deprecationWarningCustom("color", "this CLI form will be removed in a future release.")
	root, err := resolveColorRoot(deps)
	if err != nil {
		return err
	}

	c, ok := tmux.FindColor(nameOrAlias)
	if !ok {
		var available []string
		for _, ac := range tmux.AccentColors {
			available = append(available, fmt.Sprintf("%s (%s)", ac.Name, strings.Join(ac.Aliases, ", ")))
		}
		return fmt.Errorf("unknown color %q; available colors:\n  %s", nameOrAlias, strings.Join(available, "\n  "))
	}

	if err := applyColor(deps, root, c.Name); err != nil {
		return err
	}

	fmt.Fprintf(deps.stdout, "Accent color set to %s (%s)\n", c.Name, strings.Join(c.Aliases, ", "))
	return nil
}

func applyColor(deps *colorDeps, root, color string) error {
	if err := state.WriteAccentColor(root, color); err != nil {
		return fmt.Errorf("persisting accent color: %w", err)
	}

	namespace := state.ReadNamespace(root)
	if namespace == "" {
		namespace = tmux.DefaultNamespace
	}
	version := state.ReadVersion(root)
	if version == "" {
		version = "dev"
	}

	confPath, err := tmux.WriteConfig(tmux.ConfigParams{
		AccentColor: color,
		Namespace:   namespace,
		Version:     version,
		SprawlRoot:  root,
	})
	if err != nil {
		return fmt.Errorf("generating tmux config: %w", err)
	}

	session := tmux.RootSessionName(namespace)
	if err := deps.tmuxRunner.SourceFile(session, confPath); err != nil {
		fmt.Fprintf(deps.stderr, "Warning: could not apply tmux config (session may not be running): %v\n", err)
	}

	return nil
}
