package cmd

import (
	"context"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

func init() {
	hubInstancesCmd.Flags().StringVar(&hubMigrateDSN, "dsn", "",
		"Postgres DSN (or set SPRAWL_HUB_DSN). Injected at runtime; never committed.")
	hubCmd.AddCommand(hubInstancesCmd)
}

var hubInstancesCmd = &cobra.Command{
	Use:   "instances",
	Short: "List registered host instances (hub-side admin, reads the Store)",
	Long: "List the instances registered with the hub, read directly from the " +
		"Store (not via an authenticated RPC) for operator/QA convenience. " +
		"Shows metadata only; no secret material.",
	Args: cobra.NoArgs,
	RunE: func(c *cobra.Command, _ []string) error {
		return runHubInstances(c.Context(), resolveHubTokenDeps(), hubMigrateDSN)
	},
}

func runHubInstances(ctx context.Context, deps *hubTokenDeps, dsnFlag string) error {
	dsn, err := resolveHubDSN(deps.Getenv, dsnFlag)
	if err != nil {
		return err
	}
	st, err := deps.OpenStore(ctx, dsn)
	if err != nil {
		return fmt.Errorf("opening hub store: %w", err)
	}
	defer func() { _ = st.Close() }()

	insts, err := st.ListInstances(ctx)
	if err != nil {
		return fmt.Errorf("listing instances: %w", err)
	}
	if len(insts) == 0 {
		fmt.Fprintln(deps.Stderr, "No instances registered yet.")
		return nil
	}
	tw := tabwriter.NewWriter(deps.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "HOST_ID\tREPO\tACTIVE\tCLIENTS\tLAST_SEEN")
	for _, in := range insts {
		lastSeen := time.UnixMilli(in.LastSeenUnixMs).UTC().Format(time.RFC3339)
		fmt.Fprintf(tw, "%s\t%s\t%t\t%d\t%s\n",
			in.HostID, in.RepoLabel, in.Active, in.ClientsConnected, lastSeen)
	}
	return tw.Flush()
}
