package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "sprawl",
	Short: "Tree-governance for AI agents",
	Long:  "Sprawl — a self-organizing AI agent orchestration system built on Claude Code.",
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
