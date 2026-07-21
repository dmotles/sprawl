// cmd/hub.go — `sprawl hub` command group. Ops-facing tooling for the hub
// deployable (schema migrations here; more may follow). The hub server itself
// runs as the separate `hubd` binary, not a sprawl subcommand.
package cmd

import "github.com/spf13/cobra"

var hubCmd = &cobra.Command{
	Use:   "hub",
	Short: "Hub server operations (schema migrations, etc.)",
	Long: "Operational tooling for the sprawl hub deployable. The hub server " +
		"runs as its own `hubd` process; these subcommands manage its durable " +
		"state (e.g. applying Postgres schema migrations).",
}

func init() {
	rootCmd.AddCommand(hubCmd)
}
