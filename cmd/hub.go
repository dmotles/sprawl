// cmd/hub.go — `sprawl hub` command group. Client-side configuration for
// connecting this host to a sprawl hub (endpoint URL + bearer token). The hub
// server itself runs as the separate `hubd` binary, and all hub administration
// (token minting, instance listing, schema migration) is done server-side /
// via the web UI — the local `sprawl` binary is 100% DB-free.
package cmd

import "github.com/spf13/cobra"

var hubCmd = &cobra.Command{
	Use:   "hub",
	Short: "Configure this client's hub connection (URL and token)",
	Long: "Configure how this host connects to a sprawl hub. `sprawl hub url " +
		"set` and `sprawl hub token set` persist the endpoint and bearer token " +
		"to user-level config (~/.config/sprawl/config.yaml). The hub server " +
		"runs as its own `hubd` process; this binary only dials out over RPC " +
		"and never touches the hub database directly.",
}

func init() {
	rootCmd.AddCommand(hubCmd)
}
