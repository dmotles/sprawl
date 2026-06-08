package cmd

import (
	"fmt"
	"image/color"

	"charm.land/lipgloss/v2"
	"github.com/spf13/cobra"

	"github.com/dmotles/sprawl/internal/tui"
)

var debugColorsCmd = &cobra.Command{
	Use:   "colors",
	Short: "Print the TUI palette × visual treatments grid",
	RunE:  runDebugColors,
}

func init() {
	debugCmd.AddCommand(debugColorsCmd)
}

func runDebugColors(cmd *cobra.Command, _ []string) error {
	pal := tui.NewTheme("").Palette
	sample := "interrupt sent to weave"

	type roleEntry struct {
		name string
		c    color.Color
	}
	roles := []roleEntry{
		{"Primary", pal.Primary},
		{"Accent", pal.Accent},
		{"Info", pal.Info},
		{"Success", pal.Success},
		{"Warning", pal.Warning},
		{"Error", pal.Error},
		{"Busy", pal.Busy},
		{"System", pal.System},
		{"FgSubtle", pal.FgSubtle},
	}

	treatments := []string{"plain-fg", "bold", "inverse", "bg-fill", "bordered"}

	out := cmd.OutOrStdout()

	// Header: role label column + inline treatment headers (skip bordered — rendered below).
	header := fmt.Sprintf("%-10s", "role")
	for _, t := range treatments {
		if t == "bordered" {
			continue
		}
		header += "  " + t
	}
	fmt.Fprintln(out, header)

	for _, r := range roles {
		plain := lipgloss.NewStyle().Foreground(r.c).Render(sample)
		bold := lipgloss.NewStyle().Foreground(r.c).Bold(true).Render(sample)
		inverse := lipgloss.NewStyle().Background(r.c).Foreground(pal.BgBase).Render(sample)
		bgfill := lipgloss.NewStyle().Background(r.c).Foreground(pal.FgBase).Render(sample)
		fmt.Fprintln(out, fmt.Sprintf("%-10s", r.name)+"  "+plain+"  "+bold+"  "+inverse+"  "+bgfill)
	}

	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "bordered:")
	for _, r := range roles {
		bordered := lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(r.c).
			Foreground(r.c).
			Render(sample)
		fmt.Fprintln(out, r.name)
		fmt.Fprintln(out, bordered)
	}

	return nil
}
