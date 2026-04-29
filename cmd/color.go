package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/dmotles/sprawl/internal/runtimecfg"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/spf13/cobra"
)

type colorDeps struct {
	getenv func(string) string
	stdout io.Writer
	stderr io.Writer
}

var defaultColorDeps *colorDeps

func resolveColorDeps() *colorDeps {
	if defaultColorDeps != nil {
		return defaultColorDeps
	}
	return &colorDeps{
		getenv: os.Getenv,
		stdout: os.Stdout,
		stderr: os.Stderr,
	}
}

var colorCmd = &cobra.Command{
	Use:   "color",
	Short: "Manage the TUI accent color",
	Long: `View or change the accent color used for the Sprawl TUI.

Without subcommands, shows the current accent color.

Examples:
  sprawl color              Show current color
  sprawl color list         List all available colors
  sprawl color rotate       Pick a new random color
  sprawl color set cyan     Set a specific color by name or alias`,
	Args: cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		deps := resolveColorDeps()
		return runColorShow(deps)
	},
}

var colorListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all available accent colors",
	Args:  cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		deps := resolveColorDeps()
		return runColorList(deps)
	},
}

var colorRotateCmd = &cobra.Command{
	Use:   "rotate",
	Short: "Pick a new random accent color",
	Args:  cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		deps := resolveColorDeps()
		return runColorRotate(deps)
	},
}

var colorSetCmd = &cobra.Command{
	Use:   "set <color>",
	Short: "Set a specific accent color by name or alias",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		deps := resolveColorDeps()
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

	c, ok := runtimecfg.FindAccentColor(color)
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

	for _, c := range runtimecfg.AccentColors {
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
	newColor := runtimecfg.PickAccentColorExcluding(current)

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

	c, ok := runtimecfg.FindAccentColor(nameOrAlias)
	if !ok {
		var available []string
		for _, ac := range runtimecfg.AccentColors {
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
	fmt.Fprintln(deps.stderr, "Accent color will take effect the next time you run `sprawl enter`.")
	return nil
}
