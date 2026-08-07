package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dmotles/sprawl/internal/buildinfo"
)

// SetVersionInfo records the linker stamp. It forwards into internal/buildinfo
// rather than storing its own copy: the MCP server's running-vs-on-disk check
// (QUM-1154) cannot import package cmd, and a second copy of the stamp is a
// second thing that can be wrong.
func SetVersionInfo(version, commit, date string) {
	buildinfo.Set(version, commit, date)
}

func init() {
	rootCmd.AddCommand(versionCmd)
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		fmt.Fprintf(cmd.OutOrStdout(), "sprawl version %s\ncommit: %s\nbuilt: %s\n", buildinfo.Version(), buildinfo.Commit(), buildinfo.Date())
		return nil
	},
}
