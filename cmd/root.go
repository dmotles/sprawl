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
	// Execute() below already prints the error, so letting cobra print it too
	// emitted every error TWICE. Harmless while errors were one line; QUM-1086's
	// config error is a 24-line key reference, so the duplicate was ~48 lines for
	// one typo. Suppressing cobra's copy also puts the error LAST, nearest the
	// prompt, instead of above the usage block.
	//
	// Deliberately NOT also setting SilenceUsage: usage is genuinely useful for a
	// wrong-arguments error, and suppressing it would degrade every command to
	// save noise on one. That leaves a usage block between the header and the
	// error for RunE failures — a wider fix, and a repo-wide output decision
	// rather than something to fold into this issue.
	SilenceErrors: true,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
